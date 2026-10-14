import { randomInt, randomUUID } from 'node:crypto'
import type { MockModule } from '../types.ts'
import { BillingError, readStrict } from './billing.ts'
import { gate, portalState, scenario, type PortalState } from './fixtures.ts'

const DAY_MS = 86_400_000
const RATE_PERCENT = 20
const MIN_WITHDRAW = 10_000

// ---------------------------------------------------------------------------
// 状态：挂在 PortalState 对象上（WeakMap），切场景重建 PortalState 时这里跟着重建
// ---------------------------------------------------------------------------
interface EntryFixture {
  amount: number
  base: number
  status: 'pending' | 'available' | 'reversed'
  frozen_until: string | null
  created_at: string
  order_no: string
  from: string
}

interface WithdrawalFixture {
  id: string
  amount: number
  status: 'requested' | 'reviewing' | 'approved' | 'rejected' | 'processing' | 'paid' | 'failed' | 'returned'
  reject_reason: string
  requested_at: string
  completed_at: string | null
}

const MAX_CODES = 5

interface InviteCodeFixture {
  code: string
  status: 'active' | 'exhausted' | 'disabled'
  invited: number
  visits: number
  max_uses: number | null
  created_at: string
}

interface CommissionState {
  /** null = 还没有活动邀请码，GET v1/me/invite 时懒生成（与后端一致，是个会写库的 GET） */
  code: string | null
  codeCreatedAt: string
  maxUses: number | null
  codes: InviteCodeFixture[]
  invitees: Array<{ email: string; bound_at: string; risk_flag: 'none' | 'suspicious' | 'confirmed_fraud' }>
  entries: EntryFixture[]
  withdrawals: WithdrawalFixture[]
  transfers: Array<{ ledger_txn_id: string; amount: number; created_at: string }>
}

const states = new WeakMap<PortalState, CommissionState>()

function commissionState(userId: string): CommissionState {
  const portal = portalState(userId)
  let state = states.get(portal)
  if (!state) {
    state = build()
    states.set(portal, state)
  }
  return state
}

const ago = (days: number) => new Date(Date.now() - days * DAY_MS).toISOString()

function build(): CommissionState {
  const s = scenario()
  if (s === 'empty') return { code: null, codeCreatedAt: '', maxUses: null, codes: [], invitees: [], entries: [], withdrawals: [], transfers: [] }
  const entry = (daysAgo: number, amount: number, status: EntryFixture['status'], from: string, order: number, frozenInDays = -1): EntryFixture => ({
    amount,
    base: (amount * 100) / RATE_PERCENT,
    status,
    frozen_until: ago(-frozenInDays),
    created_at: ago(daysAgo),
    order_no: `PD-2${order}`,
    from,
  })
  const withdrawals: WithdrawalFixture[] = [
    { id: randomUUID(), amount: 12_000, status: 'rejected', reject_reason: '收款账号与实名不一致，请核对后重新申请', requested_at: ago(20), completed_at: ago(19) },
    { id: randomUUID(), amount: 10_000, status: 'paid', reject_reason: '', requested_at: ago(45), completed_at: ago(43) },
  ]
  // multi：一笔审核中的提现，申请表应当锁住，再申请回 409
  if (s === 'multi') withdrawals.unshift({ id: randomUUID(), amount: 3_000, status: 'reviewing', reject_reason: '', requested_at: ago(1), completed_at: null })
  const maxUses = s === 'multi' ? 6 : null
  const invitees = [
    { email: 'me***@gmail.com', bound_at: ago(3), risk_flag: 'none' as const },
    { email: 'ha***@icloud.com', bound_at: ago(4), risk_flag: 'none' as const },
    { email: 'to***@qq.com', bound_at: ago(8), risk_flag: 'none' as const },
    { email: 'k.***@outlook.com', bound_at: ago(31), risk_flag: 'suspicious' as const },
    { email: 'ya***@163.com', bound_at: ago(42), risk_flag: 'confirmed_fraud' as const },
    { email: 'li***@proton.me', bound_at: ago(72), risk_flag: 'none' as const },
  ]
  const codeStatus: InviteCodeFixture['status'] = maxUses !== null && invitees.length >= maxUses ? 'exhausted' : 'active'
  const codes: InviteCodeFixture[] = [
    {
      code: 'ZW8KQ4TM',
      status: codeStatus,
      invited: invitees.length,
      visits: 12,
      max_uses: maxUses,
      created_at: ago(90),
    },
  ]
  return {
    code: 'ZW8KQ4TM',
    codeCreatedAt: ago(90),
    maxUses,
    codes,
    invitees,
    entries: [
      entry(2, 1_180, 'pending', 'me***@gmail.com', 4101, 5),
      entry(6, 2_780, 'available', 'to***@qq.com', 4087),
      entry(29, 980, 'available', 'k.***@outlook.com', 3962),
      entry(40, 1_980, 'reversed', 'ya***@163.com', 3890),
      entry(50, 19_800, 'available', 'to***@qq.com', 3811),
      entry(70, 5_000, 'available', 'li***@proton.me', 3702),
    ],
    withdrawals,
    transfers: [{ ledger_txn_id: randomUUID(), amount: 5_000, created_at: ago(24) }],
  }
}

// ---------------------------------------------------------------------------
// 口径（5.A D-F-1）：账本余额 = 已解冻佣金 − 转出 − 已过账的提现（processing / paid）；
// 可用 = 账本余额 − 未过账的在途提现（requested / reviewing / approved）
// ---------------------------------------------------------------------------
const sum = <T>(list: readonly T[], pick: (x: T) => number) => list.reduce((acc, x) => acc + pick(x), 0)
const UNPOSTED = new Set(['requested', 'reviewing', 'approved'])
const IN_FLIGHT = new Set(['requested', 'reviewing', 'approved', 'processing'])

function ledgerBalance(c: CommissionState): number {
  const earned = sum(c.entries, (e) => (e.status === 'available' ? e.amount : 0))
  const posted = sum(c.withdrawals, (w) => (w.status === 'processing' || w.status === 'paid' ? w.amount : 0))
  return earned - sum(c.transfers, (t) => t.amount) - posted
}

const available = (c: CommissionState) => ledgerBalance(c) - sum(c.withdrawals, (w) => (UNPOSTED.has(w.status) ? w.amount : 0))

function summary(c: CommissionState) {
  const live = c.entries.filter((e) => e.status !== 'reversed')
  return {
    currency: 'CNY',
    pending: sum(c.entries, (e) => (e.status === 'pending' ? e.amount : 0)),
    available: available(c),
    withdrawing: sum(c.withdrawals, (w) => (IN_FLIGHT.has(w.status) ? w.amount : 0)),
    settled: sum(c.withdrawals, (w) => (w.status === 'paid' ? w.amount : 0)),
    invitees: c.invitees.length,
    orders: live.length,
    rate_percent: RATE_PERCENT,
    min_withdraw: MIN_WITHDRAW,
    // R81 / R114：计佣范围（真后端已上线，legacy 场景也照回）；multi 场景取「首单」，横幅写「好友首单付费」
    scope: scenario() === 'multi' ? 'first_order' : 'every_order',
    // R69：Go 的 CommissionSummary 无 omitempty，各场景恒在
    paid_invitees: new Set(live.map((e) => e.from)).size,
    total_earned: sum(live, (e) => e.amount),
  }
}

// 8 位，字母表 A–Z2–9 去掉 0/O/1/I/L（identity/invite.go）
const CODE_ALPHABET = 'ABCDEFGHJKMNPQRSTUVWXYZ23456789'
const newCode = () => Array.from({ length: 8 }, () => CODE_ALPHABET[randomInt(CODE_ALPHABET.length)]).join('')

function sortInviteCodes(codes: readonly InviteCodeFixture[]): InviteCodeFixture[] {
  return [...codes]
    .sort((a, b) => {
      if (a.status === 'active' && b.status !== 'active') return -1
      if (b.status === 'active' && a.status !== 'active') return 1
      return b.created_at.localeCompare(a.created_at)
    })
    .slice(0, 50)
}

function earliestActiveInvite(c: CommissionState): InviteCodeFixture | null {
  const actives = c.codes.filter((x) => x.status === 'active').sort((a, b) => a.created_at.localeCompare(b.created_at))
  return actives[0] ?? null
}

function ensurePrimaryCode(c: CommissionState): InviteCodeFixture {
  if (!c.code) {
    c.code = newCode()
    c.codeCreatedAt = new Date().toISOString()
    c.codes.push({
      code: c.code,
      status: 'active',
      invited: c.invitees.length,
      visits: 12,
      max_uses: c.maxUses,
      created_at: c.codeCreatedAt,
    })
  } else if (c.codes.length === 0) {
    c.codes.push({
      code: c.code,
      status: c.maxUses !== null && c.invitees.length >= c.maxUses ? 'exhausted' : 'active',
      invited: c.invitees.length,
      visits: 12,
      max_uses: c.maxUses,
      created_at: c.codeCreatedAt,
    })
  }
  const primary = earliestActiveInvite(c)
  if (primary) return primary
  return c.codes[0]!
}

export const referral: MockModule = {
  routes: {
    'GET /v1/me/invite': async (ctx) => {
      if (!(await gate(ctx))) return
      const c = commissionState(ctx.user.userId)
      const primary = ensurePrimaryCode(c)
      ctx.send(200, {
        invite: { code: primary.code, invited: primary.invited, max_uses: primary.max_uses, created_at: primary.created_at },
        invitees: c.invitees.slice(0, 100),
        codes: sortInviteCodes(c.codes),
        max_codes: MAX_CODES,
      })
    },

    'POST /v1/me/invite/codes': async (ctx) => {
      if (!(await gate(ctx))) return
      const body = await readStrict(ctx, [])
      if (!body) return
      const c = commissionState(ctx.user.userId)
      ensurePrimaryCode(c)
      const active = c.codes.filter((x) => x.status === 'active').length
      if (active >= MAX_CODES) return ctx.fail(409, 'conflict', '最多同时持有 5 个邀请码')
      const row: InviteCodeFixture = {
        code: newCode(),
        status: 'active',
        invited: 0,
        visits: 0,
        max_uses: null,
        created_at: new Date().toISOString(),
      }
      c.codes.push(row)
      ctx.send(200, row)
    },

    'GET /v1/me/commission': async (ctx) => {
      if (!(await gate(ctx))) return
      const c = commissionState(ctx.user.userId)
      ctx.send(200, {
        summary: summary(c),
        entries: c.entries.slice(0, 100).map((e) => ({ ...e, rate_percent: RATE_PERCENT, currency: 'CNY' })),
        withdrawals: c.withdrawals.slice(0, 50).map((w) => ({ ...w, currency: 'CNY' })),
        transfers: c.transfers.slice(0, 50).map((t) => ({ ...t, currency: 'CNY' })),
      })
    },

    // 修订 R5：幂等 commission_withdrawal_request；检查顺序照 RequestWithdrawal
    'POST /v1/me/withdrawals': async (ctx) => {
      const body = await readStrict(ctx, ['amount', 'payout_detail'])
      if (!body) return
      if ((body.amount !== undefined && !Number.isInteger(body.amount)) || (body.payout_detail !== undefined && typeof body.payout_detail !== 'string')) {
        return ctx.fail(400, 'bad_request', '请求体字段类型不对')
      }
      await ctx.idempotent('commission_withdrawal_request', () => {
        const c = commissionState(ctx.user.userId)
        const amount = (body.amount as number | undefined) ?? 0
        if (!body.payout_detail) return new BillingError(422, 'validation_failed', '参数不合法', { payout_detail: '请填写收款方式' }).result()
        if (amount < MIN_WITHDRAW) return new BillingError(422, 'validation_failed', '提现金额低于最低限额').result()
        if (c.entries.length === 0) return new BillingError(422, 'validation_failed', '没有可提现的佣金').result()
        if (c.withdrawals.some((w) => IN_FLIGHT.has(w.status))) return new BillingError(409, 'conflict', '还有正在处理的提现申请').result()
        const avail = available(c)
        if (avail <= 0) return new BillingError(422, 'validation_failed', '没有可提现的佣金').result()
        if (amount > avail) return new BillingError(409, 'conflict', '提现金额超过可提现余额').result()
        const id = randomUUID()
        c.withdrawals.unshift({ id, amount, status: 'requested', reject_reason: '', requested_at: new Date().toISOString(), completed_at: null })
        return { status: 200, body: { id } }
      })
    },

    // 修订 R7：与提现同一口径；成功即记进钱包余额与流水
    'POST /v1/me/commission/transfer': async (ctx) => {
      const body = await readStrict(ctx, ['amount'])
      if (!body) return
      if (body.amount !== undefined && !Number.isInteger(body.amount)) return ctx.fail(400, 'bad_request', '请求体字段类型不对')
      await ctx.idempotent('commission_transfer_to_balance', () => {
        const amount = (body.amount as number | undefined) ?? 0
        if (amount <= 0) return new BillingError(422, 'validation_failed', '参数不合法', { amount: '转入金额必须大于 0' }).result()
        const c = commissionState(ctx.user.userId)
        if (amount > available(c)) return new BillingError(409, 'conflict', '可提现佣金不足。冻结期内的佣金要等解冻后才能转出，提现处理中的金额也不能再转').result()
        const txn = randomUUID()
        const at = new Date().toISOString()
        c.transfers.unshift({ ledger_txn_id: txn, amount, created_at: at })
        const wallet = portalState(ctx.user.userId)
        wallet.balance += amount
        wallet.ledger.unshift({ kind: 'commission_to_balance', delta: amount, memo: '', at })
        return { status: 200, body: { ledger_txn_id: txn, amount } }
      })
    },
  },
}
