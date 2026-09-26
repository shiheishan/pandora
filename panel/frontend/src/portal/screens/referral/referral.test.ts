/**
 * [INPUT]: 依赖 vitest，依赖 ../../queries 的 commissionSchema，依赖 ./api 的 inviteSchema，依赖 ./model 的纯映射
 * [OUTPUT]: 无（测试）
 * [POS]: 第 ④ 步邀请返利的单元测试：佣金概况 schema（R69 字段可缺席、列表不收 null、状态枚举封闭）、邀请链接与横幅文案、邀请码用量、提现金额与表单锁、三类记录的合并与状态映射
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it } from 'vitest'
import { commissionSchema, type Commission } from '../../queries'
import { inviteSchema } from './api'
import { commissionRecords, headline, inviteLink, inviteUsage, parseWithdrawAmount, withdrawBlock } from './model'

const SUMMARY = { currency: 'CNY', pending: 1180, available: 13560, withdrawing: 0, settled: 10000, invitees: 6, orders: 5, paid_invitees: 4, total_earned: 29740, rate_percent: 20, min_withdraw: 10000, scope: 'every_order' as const }

function commission(over: Partial<Commission> = {}): Commission {
  return commissionSchema.parse({ summary: SUMMARY, entries: [], withdrawals: [], transfers: [], ...over })
}

describe('佣金概况 schema', () => {
  it('修订 R69 的字段可以缺席（旧后端）', () => {
    const legacy: Record<string, unknown> = { ...SUMMARY }
    delete legacy.paid_invitees
    delete legacy.total_earned
    const parsed = commissionSchema.parse({ summary: legacy, entries: [], withdrawals: [] })
    expect(parsed.summary.paid_invitees).toBeUndefined()
    expect(parsed.transfers).toBeUndefined()
  })

  it('三个列表 Go 端以空切片初始化，null 不放行', () => {
    expect(commissionSchema.safeParse({ summary: SUMMARY, entries: null, withdrawals: [] }).success).toBe(false)
    expect(commissionSchema.safeParse({ summary: SUMMARY, entries: [], withdrawals: [], transfers: null }).success).toBe(false)
  })

  it('提现状态是封闭枚举', () => {
    const w = { id: 'w', amount: 1, currency: 'CNY', status: 'queued', reject_reason: '', requested_at: '2026-09-01T00:00:00Z', completed_at: null }
    expect(commissionSchema.safeParse({ summary: SUMMARY, entries: [], withdrawals: [w] }).success).toBe(false)
  })

  it('邀请：max_uses 可为 null，invitees 不收 null', () => {
    const invite = { code: 'ZW8KQ4TM', invited: 0, max_uses: null, created_at: '2026-09-01T00:00:00Z' }
    expect(inviteSchema.parse({ invite, invitees: [] }).invite.max_uses).toBeNull()
    expect(inviteSchema.safeParse({ invite, invitees: null }).success).toBe(false)
  })
})

describe('邀请横幅', () => {
  it('邀请链接用查询串，不用两段式路径', () => {
    expect(inviteLink('ZW8KQ4TM', 'https://my.pandora.run')).toBe('https://my.pandora.run/?invite=ZW8KQ4TM')
  })

  it('横幅不写「首单」（公开接口没有计佣范围）；费率为 0 不提佣金', () => {
    expect(headline(20, 'every_order')).toBe('邀请好友付费，您得 20% 佣金')
    expect(headline(20, 'first_order')).toBe('好友首单付费，您得 20% 佣金')
    expect(headline(0, 'first_order')).toBe('邀请好友注册')
  })

  it('邀请码用量：无上限不说，用满警示', () => {
    const base = { code: 'X', created_at: '' }
    expect(inviteUsage({ ...base, invited: 3, max_uses: null })).toBeNull()
    expect(inviteUsage({ ...base, invited: 3, max_uses: 10 })).toEqual({ text: '邀请码最多可用 10 次，已用 3 次', exhausted: false })
    expect(inviteUsage({ ...base, invited: 6, max_uses: 6 })?.exhausted).toBe(true)
  })
})

describe('申请提现', () => {
  const limits = { min: 10000, available: 13560, currency: 'CNY' }

  it('元转分，上下限取概况', () => {
    expect(parseWithdrawAmount('120.5', limits)).toEqual({ cents: 12050 })
    expect(parseWithdrawAmount('135.60', limits)).toEqual({ cents: 13560 })
    expect(parseWithdrawAmount('', limits)).toEqual({ error: '请输入提现金额' })
    expect(parseWithdrawAmount('1.234', limits)).toEqual({ error: '金额最多两位小数' })
    expect(parseWithdrawAmount('99.99', limits)).toEqual({ error: '最低提现 ¥100.00' })
    expect(parseWithdrawAmount('135.61', limits)).toEqual({ error: '超过可用佣金 ¥135.60' })
  })

  it('在途提现（含打款中）锁表单，可用不足最低额也锁', () => {
    expect(withdrawBlock(SUMMARY)).toBeNull()
    expect(withdrawBlock({ ...SUMMARY, withdrawing: 3000 })).toBe('有一笔 ¥30.00 的提现正在处理，完成后才能再申请')
    expect(withdrawBlock({ ...SUMMARY, available: 9999 })).toBe('可用佣金满 ¥100.00 才能申请提现')
  })
})

describe('佣金记录', () => {
  const entry = (status: Commission['entries'][number]['status'], created_at: string, frozen_until: string | null = null) => ({
    amount: 1180,
    base: 5900,
    rate_percent: 20,
    currency: 'CNY',
    status,
    frozen_until,
    created_at,
    order_no: 'PD-24101',
    from: 'me***@gmail.com',
  })
  const withdrawal = (status: Commission['withdrawals'][number]['status'], requested_at: string, reject_reason = '') => ({ id: status, amount: 12000, currency: 'CNY', status, reject_reason, requested_at, completed_at: null })

  it('三类记录按时间倒序合并，出账为负', () => {
    const rows = commissionRecords(
      commission({
        entries: [entry('available', '2026-09-20T02:00:00Z')],
        transfers: [{ ledger_txn_id: 't1', amount: 5000, currency: 'CNY', created_at: '2026-09-22T02:00:00Z' }],
        withdrawals: [withdrawal('paid', '2026-09-10T02:00:00Z')],
      }),
    )
    expect(rows.map((r) => [r.title, r.ref, r.amount, r.status, r.tone])).toEqual([
      ['转入余额', undefined, -5000, '完成', 'minus'],
      ['好友 me***@gmail.com', 'PD-24101', 1180, '已结算', 'plus'],
      ['申请提现', undefined, -12000, '已打款', 'minus'],
    ])
  })

  it('佣金状态：冻结至某日、冲销划掉；没有订单号不带 ref', () => {
    const rows = commissionRecords(commission({ entries: [entry('pending', '2026-09-22T02:00:00Z', '2026-09-29T10:00:00Z'), { ...entry('reversed', '2026-09-01T02:00:00Z'), order_no: '' }] }))
    expect(rows[0]!.status).toBe('冻结至 09-29')
    expect(rows[1]).toMatchObject({ title: '好友 me***@gmail.com', status: '已冲销', tone: 'void' })
    expect(rows[1]!.ref).toBeUndefined()
    expect(commissionRecords(commission({ entries: [entry('pending', '2026-09-22T02:00:00Z')] }))[0]!.status).toBe('冻结中')
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

  it('旧后端没有 transfers 时照常合并', () => {
    const legacy = commissionSchema.parse({ summary: SUMMARY, entries: [entry('available', '2026-09-20T02:00:00Z')], withdrawals: [] })
    expect(commissionRecords(legacy)).toHaveLength(1)
  })
})
