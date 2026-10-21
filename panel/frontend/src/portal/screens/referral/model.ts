import { formatMoney } from '../../../core/format'
import {
  isUSDTMethod,
  looksLikeTxHash,
  looksLikeUSDTAddress,
  payoutAccountHint,
  withdrawMethodLabel,
} from '../../../core/withdraw-methods'
import type { Commission } from '../../queries'
import { shortDate } from '../common/traffic'
import type { Invite, InviteCode } from './api'

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

/**
 * 邀请卡标题（样稿 .inv-h）：大号比例 + 一句话，按计佣范围写（R81 / R114），first_order 才说「首单」；
 * 费率为 0 时不提佣金，rate 为 null、只写「邀请好友注册」
 */
export function headlineParts(directRateBP: number, scope: Commission['summary']['scope']): { rate: string | null; text: string } {
  if (directRateBP <= 0) return { rate: null, text: '邀请好友注册' }
  const rate = ratePercentText(directRateBP)
  return { rate, text: scope === 'first_order' ? '好友首单付款，你拿这么多佣金' : '好友每次付款，你都拿这么多佣金' }
}

/** 金额去掉 ¥ 符号，给大号数字用（符号单独做小字）；只用于非负金额 */
export const yuanText = (minor: number) => formatMoney(minor).replace('¥', '')

/** 当前仍为有效的邀请码个数（可同时持有上限由 max_codes 约束） */
export function activeCodeCount(codes: readonly InviteCode[]): number {
  return codes.filter((c) => c.status === 'active').length
}

/** 是否还能再生成一个有效邀请码 */
export function canCreateCode(codes: readonly InviteCode[], maxCodes: number): boolean {
  return activeCodeCount(codes) < maxCodes
}

/** 列表里展示的状态文案 */
export function codeStatusLabel(status: InviteCode['status']): string {
  switch (status) {
    case 'active':
      return '有效'
    case 'exhausted':
      return '已用满'
    case 'disabled':
      return '已停用'
  }
}

/**
 * 邀请链接下面的一句说明：个人邀请码用一次就失效（single_use）时先说这个；
 * 否则有使用上限时说还剩几次；都没有返回 null。
 */
export function inviteUsage(data: Pick<Invite, 'invite' | 'single_use'>): { text: string; exhausted: boolean } | null {
  const { invite } = data
  if (data.single_use) {
    return { text: '每个邀请码只能邀请 1 人：一个链接只发给一位朋友，有人用它注册后，这里会换成新的邀请码。', exhausted: false }
  }
  if (invite.max_uses === null) return null
  const exhausted = invite.invited >= invite.max_uses
  return {
    text: exhausted
      ? `邀请码已用满 ${invite.max_uses} 次，新朋友暂时无法用它注册`
      : `这个邀请码已有 ${invite.invited} 人注册，还能再邀请 ${invite.max_uses - invite.invited} 人。`,
    exhausted,
  }
}

/** 元 → 分；最多两位小数，不低于最低提现额、不超过可用佣金（后端同样校验，这里先挡一道） */
export function parseWithdrawAmount(input: string, limits: { min: number; available: number }): { cents: number } | { error: string } {
  const text = input.trim()
  if (!text) return { error: '请输入提现金额' }
  if (!/^\d+(\.\d{1,2})?$/.test(text)) return { error: '金额最多两位小数' }
  const cents = Math.round(Number(text) * 100)
  if (cents < limits.min) return { error: `最低提现 ${formatMoney(limits.min)}` }
  if (cents > limits.available) return { error: `超过可用佣金 ${formatMoney(limits.available)}` }
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

/** 关提现时整页不能提现 */
export function withdrawClosedHere(summary: Commission['summary']): boolean {
  return !summary.withdraw_enabled
}

/**
 * 关提现时的提示，提现开着时为 null。
 * 解冻的自动转余额，关闭前已可提现的部分要自己点「转入余额」（后端只自动转关闭后解冻的条目）。
 */
export function withdrawClosedHint(summary: Commission['summary']): string | null {
  if (summary.withdraw_enabled) return null
  return '提现已关闭，佣金解冻后会自动转入余额，可直接用余额购买与续费。关闭前已可提现的部分不会自动转，请点「转入余额」。'
}

/** 提现表单锁住的理由：后端同一时间只允许一笔在途（含打款中）；可用额不到最低额也提不了 */
export function withdrawBlock(summary: Commission['summary']): string | null {
  if (withdrawClosedHere(summary)) return '提现已关闭，佣金解冻后会自动转入余额'
  if (summary.withdraw_methods.length === 0) return '暂未开放提现方式'
  if (summary.withdrawing > 0) return `有一笔 ${formatMoney(summary.withdrawing)} 的提现正在处理，完成后才能再申请`
  if (summary.available < summary.min_withdraw) return `可用佣金满 ${formatMoney(summary.min_withdraw)} 才能申请提现`
  return null
}

// ---------------------------------------------------------------------------
// 佣金记录：entries（+）、transfers（−，完成）、withdrawals（−）按时间倒序合并
// ---------------------------------------------------------------------------
/** plus 入账；minus 出账；void 没有生效（作废、驳回），金额划掉 */
type RecordTone = 'plus' | 'minus' | 'void'
/** 行首图标（样稿 .rec .ico）：返佣 gift、转入余额 wallet、提现 out */
type RecordIcon = 'gift' | 'wallet' | 'out'

export interface CommissionRecord {
  key: string
  title: string
  /** 佣金对应的订单号；与标题分开，窄屏换行时整段不拆 */
  ref?: string
  /** 日期，驳回时附原因 */
  meta: string
  /** 带符号的金额（分） */
  amount: number
  status: string
  tone: RecordTone
  icon: RecordIcon
  at: string
  /** USDT 已打款的链上交易哈希（可复制） */
  txHash?: string
  /** 交易哈希的区块浏览器地址 */
  txUrl?: string
}

type Entry = Commission['entries'][number]
type Withdrawal = Commission['withdrawals'][number]

function entryStatus(e: Entry): { status: string; tone: RecordTone } {
  switch (e.status) {
    case 'pending':
      if (e.under_review) {
        return { status: '审核中', tone: 'plus' }
      }
      return { status: `冻结至 ${shortDate(e.frozen_until)}`, tone: 'plus' }
    case 'available':
      return { status: '可用', tone: 'plus' }
    case 'voided':
      return { status: '已作废', tone: 'void' }
  }
}

const WITHDRAWAL_STATUS: Readonly<Record<Withdrawal['status'], { status: string; tone: RecordTone }>> = {
  requested: { status: '审核中', tone: 'minus' },
  approved: { status: '打款中', tone: 'minus' },
  paid: { status: '已打款', tone: 'minus' },
  rejected: { status: '已驳回', tone: 'void' },
}

/** USDT 交易哈希的区块浏览器地址（只拼公开浏览器的地址，不请求它们）；方式不是 USDT 或哈希形状不对返回 null */
export function txExplorerUrl(method: string, hash: string): string | null {
  if (!isUSDTMethod(method) || !looksLikeTxHash(method, hash)) return null
  const h = encodeURIComponent(hash.trim())
  switch (method) {
    case 'usdt_trc20':
      return `https://tronscan.org/#/transaction/${h}`
    case 'usdt_erc20':
      return `https://etherscan.io/tx/${h}`
    case 'usdt_bep20':
      return `https://bscscan.com/tx/${h}`
    default:
      return null
  }
}

/** 长哈希缩写：前 8 位 + … + 后 6 位；不超过 20 个字符原样返回 */
export function shortHash(hash: string): string {
  const h = hash.trim()
  if (h.length <= 20) return h
  return `${h.slice(0, 8)}…${h.slice(-6)}`
}

export function commissionRecords(c: Commission): CommissionRecord[] {
  const rows: CommissionRecord[] = []
  c.entries.forEach((e, i) => {
    const title = e.level <= 1 ? '好友付款返佣' : `第 ${e.level} 级好友付款返佣`
    const metaBase = shortDate(e.created_at)
    const meta = e.level <= 1 && e.from ? `${metaBase} · ${e.from}` : metaBase
    rows.push({
      key: `e${i}`,
      title,
      ...(e.order_no ? { ref: e.order_no } : {}),
      meta,
      amount: e.amount,
      icon: 'gift',
      at: e.created_at,
      ...entryStatus(e),
    })
  })
  for (const t of c.transfers) {
    rows.push({ key: `t${t.ledger_txn_id}`, title: '转入余额', meta: shortDate(t.created_at), amount: -t.amount, status: '已完成', tone: 'minus', icon: 'wallet', at: t.created_at })
  }
  for (const w of c.withdrawals) {
    const reason = w.status === 'rejected' && w.reject_reason ? ` · ${w.reject_reason}` : ''
    const isPaidUSDT = w.status === 'paid' && isUSDTMethod(w.method) && Boolean(w.tx_hash)
    const txUrl = isPaidUSDT ? txExplorerUrl(w.method, w.tx_hash) : null
    rows.push({
      key: `w${w.id}`,
      title: '提现',
      ref: withdrawMethodLabel(w.method),
      meta: shortDate(w.requested_at) + reason,
      amount: -w.amount,
      icon: 'out',
      at: w.requested_at,
      ...WITHDRAWAL_STATUS[w.status],
      ...(isPaidUSDT ? { txHash: w.tx_hash } : {}),
      ...(txUrl ? { txUrl } : {}),
    })
  }
  return rows.sort((a, b) => new Date(b.at).getTime() - new Date(a.at).getTime())
}
