/**
 * [INPUT]: 依赖 ../../../core/format 的 formatMoney，依赖 ../common/traffic 的 shortDate，依赖 ../../queries 的 Commission 类型，依赖 ./api 的 Invite 类型
 * [OUTPUT]: 对外提供 inviteLink、headline、inviteUsage、parseWithdrawAmount、withdrawBlock、CommissionRecord / RecordTone、commissionRecords
 * [POS]: portal/screens/referral 的纯映射（契约门户-06）：邀请链接 /?invite=、横幅文案（公开接口没有计佣范围，不写「首单」）、邀请码用量、提现金额元 → 分与上下限、提现表单何时锁住、三类记录（佣金 / 转入余额 / 提现）合并成「佣金记录」；有单元测试
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { formatMoney } from '../../../core/format'
import type { Commission } from '../../queries'
import { shortDate } from '../common/traffic'
import type { Invite } from './api'

/** 契约门户-06：/r/CODE 会撞 public 网关根下的订阅通配，邀请链接用查询串 */
export const inviteLink = (code: string, origin: string) => `${origin}/?invite=${encodeURIComponent(code)}`

/**
 * 横幅标题。「首单」只在计佣范围是 first_order 时成立（修订 R67），而门户的佣金概况不回 scope，
 * 所以两种范围都成立的说法；费率为 0 时不提佣金。
 */
export const headline = (ratePercent: number) => (ratePercent > 0 ? `邀请好友付费，您得 ${ratePercent}% 佣金` : '邀请好友注册')

/** 邀请码有使用上限时的一句说明；没有上限返回 null */
export function inviteUsage(invite: Invite['invite']): { text: string; exhausted: boolean } | null {
  if (invite.max_uses === null) return null
  const exhausted = invite.invited >= invite.max_uses
  return {
    text: exhausted ? `邀请码已用满 ${invite.max_uses} 次，新朋友暂时无法用它注册` : `邀请码最多可用 ${invite.max_uses} 次，已用 ${invite.invited} 次`,
    exhausted,
  }
}

/** 元 → 分；最多两位小数，不低于最低提现额、不超过可用佣金（后端同样校验，这里先挡一道） */
export function parseWithdrawAmount(input: string, limits: { min: number; available: number; currency: string }): { cents: number } | { error: string } {
  const text = input.trim()
  if (!text) return { error: '请输入提现金额' }
  if (!/^\d+(\.\d{1,2})?$/.test(text)) return { error: '金额最多两位小数' }
  const cents = Math.round(Number(text) * 100)
  if (cents < limits.min) return { error: `最低提现 ${formatMoney(limits.min, limits.currency)}` }
  if (cents > limits.available) return { error: `超过可用佣金 ${formatMoney(limits.available, limits.currency)}` }
  return { cents }
}

/** 提现表单锁住的理由：后端同一时间只允许一笔在途（含打款中）；可用额不到最低额也提不了 */
export function withdrawBlock(summary: Commission['summary']): string | null {
  if (summary.withdrawing > 0) return `有一笔 ${formatMoney(summary.withdrawing, summary.currency)} 的提现正在处理，完成后才能再申请`
  if (summary.available < summary.min_withdraw) return `可用佣金满 ${formatMoney(summary.min_withdraw, summary.currency)} 才能申请提现`
  return null
}

// ---------------------------------------------------------------------------
// 佣金记录：entries（+）、transfers（−，完成）、withdrawals（−）按时间倒序合并
// ---------------------------------------------------------------------------
/** plus 入账；minus 出账；void 没有生效（冲销、驳回、失败退回），金额划掉 */
export type RecordTone = 'plus' | 'minus' | 'void'

export interface CommissionRecord {
  key: string
  title: string
  /** 佣金对应的订单号；与标题分开，窄屏换行时整段不拆 */
  ref?: string
  /** 日期，驳回时附原因 */
  meta: string
  /** 带符号的金额（分） */
  amount: number
  currency: string
  status: string
  tone: RecordTone
  at: string
}

type Entry = Commission['entries'][number]
type Withdrawal = Commission['withdrawals'][number]

function entryStatus(e: Entry): { status: string; tone: RecordTone } {
  switch (e.status) {
    case 'pending':
    case 'frozen':
      return { status: e.frozen_until ? `冻结至 ${shortDate(e.frozen_until)}` : '冻结中', tone: 'plus' }
    case 'available':
    case 'settled':
      return { status: '已结算', tone: 'plus' }
    case 'reversed':
      return { status: '已冲销', tone: 'void' }
    case 'rejected':
      return { status: '已驳回', tone: 'void' }
  }
}

const WITHDRAWAL_STATUS: Readonly<Record<Withdrawal['status'], { status: string; tone: RecordTone }>> = {
  requested: { status: '审核中', tone: 'minus' },
  reviewing: { status: '审核中', tone: 'minus' },
  approved: { status: '打款中', tone: 'minus' },
  processing: { status: '打款中', tone: 'minus' },
  paid: { status: '已打款', tone: 'minus' },
  rejected: { status: '已驳回', tone: 'void' },
  failed: { status: '失败已退回', tone: 'void' },
  returned: { status: '失败已退回', tone: 'void' },
}

export function commissionRecords(c: Commission): CommissionRecord[] {
  const rows: CommissionRecord[] = []
  c.entries.forEach((e, i) => {
    rows.push({
      key: `e${i}`,
      title: `好友 ${e.from}`,
      ...(e.order_no ? { ref: e.order_no } : {}),
      meta: shortDate(e.created_at),
      amount: e.amount,
      currency: e.currency,
      at: e.created_at,
      ...entryStatus(e),
    })
  })
  for (const t of c.transfers ?? []) {
    rows.push({ key: `t${t.ledger_txn_id}`, title: '转入余额', meta: shortDate(t.created_at), amount: -t.amount, currency: t.currency, status: '完成', tone: 'minus', at: t.created_at })
  }
  for (const w of c.withdrawals) {
    const reason = w.status === 'rejected' && w.reject_reason ? ` · ${w.reject_reason}` : ''
    rows.push({ key: `w${w.id}`, title: '申请提现', meta: shortDate(w.requested_at) + reason, amount: -w.amount, currency: w.currency, at: w.requested_at, ...WITHDRAWAL_STATUS[w.status] })
  }
  return rows.sort((a, b) => new Date(b.at).getTime() - new Date(a.at).getTime())
}
