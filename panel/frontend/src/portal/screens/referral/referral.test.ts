import { describe, expect, it } from 'vitest'
import { commissionSchema, type Commission } from '../../queries'
import { inviteSchema } from './api'
import {
  activeCodeCount,
  canCreateCode,
  codeStatusLabel,
  commissionRecords,
  headlineParts,
  inviteLink,
  inviteUsage,
  parseWithdrawAmount,
  payoutError,
  ratePercentText,
  withdrawBlock,
  withdrawClosedHere,
  withdrawClosedHint,
  yuanText,
} from './model'

const SUMMARY = {
  pending: 1180,
  available: 13560,
  withdrawing: 0,
  settled: 10000,
  invitees: 6,
  orders: 5,
  paid_invitees: 4,
  total_earned: 29740,
  rate_percent: 20,
  direct_rate_bp: 2000,
  min_withdraw: 10000,
  withdraw_enabled: true,
  withdraw_methods: ['alipay'],
  scope: 'every_order' as const,
}

const WITHDRAW_CLOSED_HINT =
  '提现已关闭，佣金解冻后会自动转入余额，可直接用余额购买与续费。关闭前已可提现的部分不会自动转，请点「转入余额」。'

function commission(over: Partial<Commission> = {}): Commission {
  return commissionSchema.parse({ summary: SUMMARY, entries: [], withdrawals: [], transfers: [], ...over })
}

describe('佣金概况 schema', () => {
  it('修订 R69 的字段 Go 无 omitempty，缺席不放行', () => {
    const missing: Record<string, unknown> = { ...SUMMARY }
    delete missing.paid_invitees
    expect(commissionSchema.safeParse({ summary: missing, entries: [], withdrawals: [], transfers: [] }).success).toBe(false)
    expect(commissionSchema.safeParse({ summary: SUMMARY, entries: [], withdrawals: [] }).success).toBe(false)
  })

  it('三个列表 Go 端以空切片初始化，null 不放行', () => {
    expect(commissionSchema.safeParse({ summary: SUMMARY, entries: null, withdrawals: [], transfers: [] }).success).toBe(false)
    expect(commissionSchema.safeParse({ summary: SUMMARY, entries: [], withdrawals: [], transfers: null }).success).toBe(false)
  })

  it('提现状态是封闭枚举', () => {
    const w = { id: 'w', amount: 1, method: 'alipay', status: 'queued', reject_reason: '', requested_at: '2026-09-01T00:00:00Z', completed_at: null }
    expect(commissionSchema.safeParse({ summary: SUMMARY, entries: [], withdrawals: [w], transfers: [] }).success).toBe(false)
  })

  it('邀请：max_uses 可为 null，invitees 不收 null；codes 与 max_codes 必填', () => {
    const invite = { code: 'ZW8KQ4TM', invited: 0, max_uses: null, created_at: '2026-09-01T00:00:00Z' }
    const code = { code: 'ZW8KQ4TM', status: 'active' as const, invited: 0, visits: 12, max_uses: null, created_at: '2026-09-01T00:00:00Z' }
    expect(inviteSchema.parse({ invite, invitees: [], codes: [code], max_codes: 5 }).invite.max_uses).toBeNull()
    expect(inviteSchema.safeParse({ invite, invitees: null }).success).toBe(false)
    expect(inviteSchema.safeParse({ invite, invitees: [], codes: [], max_codes: 5 }).success).toBe(true)
  })
})

describe('邀请码列表', () => {
  const codes = [
    { code: 'A', status: 'active' as const, invited: 1, visits: 2, max_uses: null, created_at: '' },
    { code: 'B', status: 'exhausted' as const, invited: 0, visits: 0, max_uses: 1, created_at: '' },
    { code: 'C', status: 'disabled' as const, invited: 0, visits: 0, max_uses: null, created_at: '' },
  ]

  it('有效码个数与是否可再生成', () => {
    expect(activeCodeCount(codes)).toBe(1)
    expect(canCreateCode(codes, 5)).toBe(true)
    expect(canCreateCode(codes, 1)).toBe(false)
    expect(canCreateCode(codes.filter((c) => c.status !== 'active'), 5)).toBe(true)
  })

  it('状态文案', () => {
    expect(codeStatusLabel('active')).toBe('有效')
    expect(codeStatusLabel('exhausted')).toBe('已用满')
    expect(codeStatusLabel('disabled')).toBe('已停用')
  })
})

describe('邀请横幅', () => {
  it('邀请链接用查询串，不用两段式路径', () => {
    expect(inviteLink('ZW8KQ4TM', 'https://my.example.test')).toBe('https://my.example.test/?invite=ZW8KQ4TM')
  })

  it('横幅比例与首单展示；费率为 0 不提佣金', () => {
    expect(headlineParts(2000, 'every_order')).toEqual({ rate: '20', text: '好友每次付款，你都拿这么多佣金' })
    expect(headlineParts(495, 'first_order')).toEqual({ rate: '4.95', text: '好友首单付款，你拿这么多佣金' })
    expect(headlineParts(0, 'first_order')).toEqual({ rate: null, text: '邀请好友注册' })
  })

  it('yuanText 去掉符号给大号数字用', () => {
    expect(yuanText(13560)).toBe('135.60')
    expect(yuanText(123456789)).toBe('1,234,567.89')
  })

  it('ratePercentText 整数与小数去尾 0', () => {
    expect(ratePercentText(1000)).toBe('10')
    expect(ratePercentText(500)).toBe('5')
    expect(ratePercentText(495)).toBe('4.95')
    expect(ratePercentText(1250)).toBe('12.5')
  })

  it('邀请码用量：无上限不说，用满警示', () => {
    const base = { code: 'X', created_at: '' }
    expect(inviteUsage({ ...base, invited: 3, max_uses: null })).toBeNull()
    expect(inviteUsage({ ...base, invited: 3, max_uses: 10 })).toEqual({ text: '这个邀请码已有 3 人注册，还能再邀请 7 人。', exhausted: false })
    expect(inviteUsage({ ...base, invited: 6, max_uses: 6 })?.exhausted).toBe(true)
  })
})

describe('申请提现', () => {
  const limits = { min: 10000, available: 13560 }

  it('元转分，上下限取概况', () => {
    expect(parseWithdrawAmount('120.5', limits)).toEqual({ cents: 12050 })
    expect(parseWithdrawAmount('135.60', limits)).toEqual({ cents: 13560 })
    expect(parseWithdrawAmount('', limits)).toEqual({ error: '请输入提现金额' })
    expect(parseWithdrawAmount('1.234', limits)).toEqual({ error: '金额最多两位小数' })
    expect(parseWithdrawAmount('99.99', limits)).toEqual({ error: '最低提现 ¥100.00' })
    expect(parseWithdrawAmount('135.61', limits)).toEqual({ error: '超过可用佣金 ¥135.60' })
  })

  it('提现关闭与无开放方式锁表单，在途提现和不足最低额也锁', () => {
    expect(withdrawBlock(SUMMARY)).toBeNull()
    expect(withdrawBlock({ ...SUMMARY, withdraw_enabled: false })).toBe('提现已关闭，佣金解冻后会自动转入余额')
    expect(withdrawBlock({ ...SUMMARY, withdraw_methods: [] })).toBe('暂未开放提现方式')
    expect(withdrawBlock({ ...SUMMARY, withdrawing: 3000 })).toBe('有一笔 ¥30.00 的提现正在处理，完成后才能再申请')
    expect(withdrawBlock({ ...SUMMARY, available: 9999 })).toBe('可用佣金满 ¥100.00 才能申请提现')
  })

  it('关提现：withdrawClosedHere 为 true，固定提示文案', () => {
    const closed = { ...SUMMARY, withdraw_enabled: false }
    expect(withdrawClosedHere(closed)).toBe(true)
    expect(withdrawClosedHint(closed)).toBe(WITHDRAW_CLOSED_HINT)
    expect(withdrawClosedHint(SUMMARY)).toBeNull()
  })

  it('payoutError 校验收款账号', () => {
    expect(payoutError('alipay', '')).toBe('请填写收款账号')
    expect(payoutError('alipay', '   ')).toBe('请填写收款账号')
    expect(payoutError('alipay', '13800000000 张三')).toBeUndefined()
    expect(payoutError('usdt_trc20', 'TBXSw8fM4jpQkGc6zZjsVABFpVN7UvXPdV')).toBeUndefined()
    expect(payoutError('usdt_trc20', '0x123')).toBe('地址格式不对：T 开头的 TRC20 地址（34 位）')
    expect(payoutError('usdt_erc20', '0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed')).toBeUndefined()
  })
})

describe('佣金记录', () => {
  const entry = (status: Commission['entries'][number]['status'], created_at: string, frozen_until: string | null = null, level = 1) => ({
    amount: 1180,
    base: 5900,
    rate_percent: 20,
    rate_bp: 2000,
    level,
    share_percent: 100,
    status,
    frozen_until,
    created_at,
    order_no: 'PD-24101',
    from: 'me***@gmail.com',
  })
  const withdrawal = (status: Commission['withdrawals'][number]['status'], requested_at: string, reject_reason = '', method = 'alipay') => ({
    id: status,
    amount: 12000,
    method,
    status,
    reject_reason,
    requested_at,
    completed_at: null,
  })

  it('三类记录按时间倒序合并，出账为负，带提现方式名', () => {
    const rows = commissionRecords(
      commission({
        entries: [entry('available', '2026-09-20T02:00:00Z')],
        transfers: [{ ledger_txn_id: 't1', amount: 5000, created_at: '2026-09-22T02:00:00Z' }],
        withdrawals: [withdrawal('paid', '2026-09-10T02:00:00Z')],
      }),
    )
    expect(rows.map((r) => [r.title, r.ref, r.amount, r.status, r.tone, r.icon])).toEqual([
      ['转入余额', undefined, -5000, '已完成', 'minus', 'wallet'],
      ['好友付款返佣', 'PD-24101', 1180, '可用', 'plus', 'gift'],
      ['提现', '支付宝', -12000, '已打款', 'minus', 'out'],
    ])
  })

  it('佣金状态：冻结至某日、冲销划掉；多级分销标题与 meta', () => {
    const rows = commissionRecords(
      commission({
        entries: [
          entry('pending', '2026-09-22T02:00:00Z', '2026-09-29T10:00:00Z'),
          { ...entry('reversed', '2026-09-01T12:00:00Z', null, 2), order_no: '', from: '' },
        ],
      }),
    )
    expect(rows[0]!.status).toBe('冻结至 09-29')
    expect(rows[1]).toMatchObject({ title: '第 2 级好友付款返佣', status: '已冲销', tone: 'void' })
    expect(rows[1]!.meta).toBe('09-01')
    expect(rows[1]!.ref).toBeUndefined()
    expect(commissionRecords(commission({ entries: [entry('pending', '2026-09-22T02:00:00Z')] }))[0]!.status).toBe('冻结中')
    const level1 = commissionRecords(commission({ entries: [entry('available', '2026-09-20T02:00:00Z')] }))[0]!
    expect(level1.meta).toContain('me***@gmail.com')
  })

  it('提现状态映射，驳回带原因、失败退回划掉', () => {
    const rows = commissionRecords(
      commission({
        withdrawals: [
          withdrawal('reviewing', '2026-09-06T02:00:00Z'),
          withdrawal('processing', '2026-09-05T02:00:00Z'),
          withdrawal('rejected', '2026-09-04T02:00:00Z', '账号与实名不一致'),
          withdrawal('returned', '2026-09-03T02:00:00Z'),
        ],
      }),
    )
    expect(rows.map((r) => r.status)).toEqual(['审核中', '打款中', '已驳回', '失败已退回'])
    expect(rows[2]!.meta).toMatch(/ · 账号与实名不一致$/)
    expect(rows[3]!.tone).toBe('void')
  })

  it('没有转出记录（transfers 为空数组）时照常合并', () => {
    expect(commissionRecords(commission({ entries: [entry('available', '2026-09-20T02:00:00Z')] }))).toHaveLength(1)
  })
})
