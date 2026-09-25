/**
 * [INPUT]: 依赖 ../../../ui 的 TagTone 类型，依赖 ./api 的 TicketRow / TicketDetail / TicketMessage 类型
 * [OUTPUT]: 对外提供 TicketRoute / ticketRoute、ticketStatus、canWithdraw、canClose、canReply、authorLabel、messageTime、validateTicket / TicketErrors、SUBJECT_RANGE、BODY_RANGE、REPLY_MAX
 * [POS]: portal/screens/tickets 的纯逻辑（契约门户-07）：子路由（列表 / 新建 / 某张工单）、状态文案与色调（closed 按 closed_reason 分已撤回与已关闭）、三个按钮的可见条件（与后端 withdraw.go / 关闭 / 回复的判定一致）、作者名（待决 D-F-2 未决前客服统一显示「客服」）、消息时间、新建表单校验（与 support.create 同口径）；有单元测试
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { TagTone } from '../../../ui'
import type { TicketDetail, TicketMessage, TicketRow } from './api'

// ---------------------------------------------------------------------------
// 子路由：#/tickets（列表，宽屏右侧显示第一张）、#/tickets/new[?order=<订单 id>]、#/tickets/<id>
// ---------------------------------------------------------------------------
export type TicketRoute = { kind: 'list' } | { kind: 'new'; orderId: string | null } | { kind: 'ticket'; id: string }

export function ticketRoute(rest: readonly string[], query: URLSearchParams): TicketRoute {
  const [first] = rest
  if (!first) return { kind: 'list' }
  if (first === 'new') return { kind: 'new', orderId: query.get('order') || null }
  return { kind: 'ticket', id: first }
}

// ---------------------------------------------------------------------------
// 状态（契约门户-07 列表映射）
// ---------------------------------------------------------------------------
export function ticketStatus(t: Pick<TicketRow, 'status' | 'closed_reason'>): { label: string; tone: TagTone } {
  switch (t.status) {
    case 'open':
    case 'pending_agent':
    case 'escalated':
      return { label: '等待回复', tone: 'warn' }
    case 'pending_user':
      return { label: '客服已回复', tone: 'info' }
    case 'resolved':
      return { label: '已解决', tone: 'neutral' }
    case 'closed':
      return t.closed_reason === 'withdrawn' ? { label: '已撤回', tone: 'neutral' } : { label: '已关闭', tone: 'neutral' }
  }
}

/** 撤回：未关闭且客服还没回复过（withdraw.go 数的是 author_kind=agent 的消息） */
export const canWithdraw = (t: Pick<TicketDetail, 'status' | 'messages'>) => t.status !== 'closed' && !t.messages.some((m) => m.author_kind === 'agent')
/** 问题已解决（关闭）：未关闭即可；已关闭再关后端回 404 */
export const canClose = (t: Pick<TicketDetail, 'status'>) => t.status !== 'closed'
/** 回复：未关闭即可，resolved 被追问会重开 */
export const canReply = canClose

/** D-F-2 未决前：客服不显示姓名；系统消息居中灰字，不需要作者 */
export function authorLabel(m: Pick<TicketMessage, 'author_kind'>): string {
  return m.author_kind === 'user' ? '我' : m.author_kind === 'agent' ? '客服' : '系统'
}

/** 设计稿口径：今天只写时分，今年写月-日 时分，更早带年份（浏览器本地时区） */
export function messageTime(at: string, now: Date = new Date()): string {
  const d = new Date(at)
  if (Number.isNaN(d.getTime())) return at
  const p = (n: number) => String(n).padStart(2, '0')
  const hm = `${p(d.getHours())}:${p(d.getMinutes())}`
  if (d.toDateString() === now.toDateString()) return hm
  const md = `${p(d.getMonth() + 1)}-${p(d.getDate())}`
  return d.getFullYear() === now.getFullYear() ? `${md} ${hm}` : `${d.getFullYear()}-${md} ${hm}`
}

// ---------------------------------------------------------------------------
// 新建表单：与 support.create 同口径（去首尾空白、按字数计），设计稿只要求标题，后端要求正文 ≥ 10 字
// ---------------------------------------------------------------------------
export const SUBJECT_RANGE = [4, 120] as const
export const BODY_RANGE = [10, 5000] as const
export const REPLY_MAX = 5000

export interface TicketErrors {
  subject?: string
  body?: string
}

const runes = (s: string) => [...s.trim()].length

export function validateTicket(input: { subject: string; body: string }): TicketErrors {
  const errors: TicketErrors = {}
  const s = runes(input.subject)
  const b = runes(input.body)
  if (s === 0) errors.subject = '请用一句话描述问题'
  else if (s < SUBJECT_RANGE[0] || s > SUBJECT_RANGE[1]) errors.subject = `标题需在 ${SUBJECT_RANGE[0]}–${SUBJECT_RANGE[1]} 字之间`
  if (b === 0) errors.body = '请写下详细描述'
  else if (b < BODY_RANGE[0]) errors.body = `详细描述至少 ${BODY_RANGE[0]} 个字，方便客服定位问题`
  else if (b > BODY_RANGE[1]) errors.body = `详细描述最多 ${BODY_RANGE[1]} 字`
  return errors
}
