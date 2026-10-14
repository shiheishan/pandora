import { formatMoney } from '../../../core/format'
import { isUSDTMethod, looksLikeUSDTAddress, payoutAccountHint, withdrawMethodLabel } from '../../../core/withdraw-methods'
import type { Commission } from '../../queries'
import { shortDate } from '../common/traffic'
import type { Invite } from './api'

/** 契约门户-06：/r/CODE 会撞 public 网关根下的订阅通配，邀请链接用查询串 */
export const inviteLink = (code: string, origin: string) => `${origin}/?invite=${encodeURIComponent(code)}`

/** 万分比转百分比文字，去掉多余的 0（如 1000 → '10'，500 → '5'，495 → '4.95'，1250 → '12.5'） */
export function ratePercentText(bp: number): string {
  const intPart = Math.floor(bp / 100)
  const decPart = bp % 100
  if (decPart === 0) return `${intPart}`
  if (decPart % 10 === 0) return `${intPart}.${decPart / 10}`
  return `${intPart}.${decPart.toString().padStart(2, '0')}`
}

/** 横幅标题：按计佣范围写（R81 / R114），first_order 才说「首单」；费率为 0 时不提佣金 */
export function headline(directRateBP: number, scope: Commission['summary']['scope']): string {
  if (directRateBP <= 0) return '邀请好友注册'
  const rateText = ratePercentText(directRateBP)
  return scope === 'first_order' ? `好友首单付费，您得 ${rateText}% 佣金` : `邀请好友付费，您得 ${rateText}% 佣金`
}

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

/** 校验收款账号：USDT 检查地址特征 */
export function payoutError(method: string, detail: string): string | undefined {
  if (!detail.trim()) return '请填写收款账号'
  if (isUSDTMethod(method) && !looksLikeUSDTAddress(method, detail)) {
    return `地址格式不对：${payoutAccountHint(method)}`
  }
  return undefined
}

/** 关提现只关站点币种：汇总是站点币种且提现关着时，这个币种不能提现 */
export function withdrawClosedHere(summary: Commission['summary']): boolean {
  return !summary.withdraw_enabled && summary.currency === summary.site_currency
}

/**
 * 明细里还有已解冻佣金的其他币种（不是站点币种），按字母序。汇总只显示一个币种；
 * 站点币种和别的币种都有可用额时汇总取站点币种，别的币种要等站点币种转完、汇总切过去才看得到提现表单。
 */
export function otherAvailableCurrencies(c: Commission): string[] {
  const site = c.summary.site_currency
  return [...new Set(c.entries.filter((e) => e.status === 'available' && e.currency !== site).map((e) => e.currency))].sort()
}

/**
 * 关提现时的提示，按币种分开写；提现开着时为 null。
 * 站点币种：以后解冻的自动转余额，关闭前已可提现的部分要自己点「全部转入余额」（后端只自动转关闭后解冻的条目）；
 * 同时还有别的币种的佣金时，写明它们仍可提现、入口在哪（先把站点币种转走，汇总就换成那个币种的提现表单）。
 * 别的币种：转不进余额（钱包只显示站点币种），照常可以提现。
 */
export function withdrawClosedHint(summary: Commission['summary'], others: readonly string[] = []): string | null {
  if (summary.withdraw_enabled) return null
  if (withdrawClosedHere(summary)) {
    const base = '提现已关闭，佣金解冻后会自动转入余额，可直接用余额购买与续费。关闭前已可提现的部分不会自动转，请点「全部转入余额」。'
    if (others.length === 0 || summary.available <= 0) return base
    const list = others.join('、')
    return `${base}你的 ${list} 佣金仍可申请提现：先点「全部转入余额」转走本站币种的佣金，这里会换成 ${list} 的提现表单。`
  }
  return `提现已关闭只针对 ${summary.site_currency}；${summary.currency} 佣金转不进余额，仍可申请提现。`
}

/** 提现表单锁住的理由：后端同一时间只允许一笔在途（含打款中）；可用额不到最低额也提不了 */
export function withdrawBlock(summary: Commission['summary']): string | null {
  if (withdrawClosedHere(summary)) return '提现已关闭，佣金解冻后会自动转入余额'
  if (summary.withdraw_methods.length === 0) return '暂未开放提现方式'
  if (summary.withdrawing > 0) return `有一笔 ${formatMoney(summary.withdrawing, summary.currency)} 的提现正在处理，完成后才能再申请`
  if (summary.available < summary.min_withdraw) return `可用佣金满 ${formatMoney(summary.min_withdraw, summary.currency)} 才能申请提现`
  return null
}

// ---------------------------------------------------------------------------
// 佣金记录：entries（+）、transfers（−，完成）、withdrawals（−）按时间倒序合并
// ---------------------------------------------------------------------------
/** plus 入账；minus 出账；void 没有生效（冲销、驳回、失败退回），金额划掉 */
type RecordTone = 'plus' | 'minus' | 'void'

interface CommissionRecord {
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
    case 'voided':
      return { status: '已作废', tone: 'void' }
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
      // 第 2、3 级不是自己邀请的人，后端不给邮箱，只写第几级
      title: e.level <= 1 ? `好友 ${e.from}` : `第 ${e.level} 级好友`,
      ...(e.order_no ? { ref: e.order_no } : {}),
      meta: shortDate(e.created_at),
      amount: e.amount,
      currency: e.currency,
      at: e.created_at,
      ...entryStatus(e),
    })
  })
  for (const t of c.transfers) {
    rows.push({ key: `t${t.ledger_txn_id}`, title: '转入余额', meta: shortDate(t.created_at), amount: -t.amount, currency: t.currency, status: '完成', tone: 'minus', at: t.created_at })
  }
  for (const w of c.withdrawals) {
    const reason = w.status === 'rejected' && w.reject_reason ? ` · ${w.reject_reason}` : ''
    rows.push({ key: `w${w.id}`, title: `申请提现 · ${withdrawMethodLabel(w.method)}`, meta: shortDate(w.requested_at) + reason, amount: -w.amount, currency: w.currency, at: w.requested_at, ...WITHDRAWAL_STATUS[w.status] })
  }
  return rows.sort((a, b) => new Date(b.at).getTime() - new Date(a.at).getTime())
}
