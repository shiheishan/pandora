import type { TagTone } from '../../../ui'
import type { TicketDetail, TicketMessage, TicketRow } from './api'

// ---------------------------------------------------------------------------
// 子路由：#/tickets（列表，宽屏右侧显示第一张）、#/tickets/new[?order=<订单 id>]、#/tickets/<id>
// ---------------------------------------------------------------------------
type TicketRoute = { kind: 'list' } | { kind: 'new'; orderId: string | null } | { kind: 'ticket'; id: string }

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
      return { label: '客服已回复', tone: 'brand' }
    case 'resolved':
      return { label: '已解决', tone: 'neutral' }
    case 'closed':
      if (t.closed_reason === 'withdrawn') return { label: '已撤回', tone: 'neutral' }
      if (t.closed_reason === 'auto_closed') return { label: '已自动关闭', tone: 'neutral' }
      return { label: '已关闭', tone: 'neutral' }
  }
}

/** 撤回：未关闭且客服还没回复过（withdraw.go 数的是 author_kind=agent 的消息） */
export const canWithdraw = (t: Pick<TicketDetail, 'status' | 'messages'>) => t.status !== 'closed' && !t.messages.some((m) => m.author_kind === 'agent')
/** 问题已解决（关闭）：照设计稿，客服回复过才给关闭；后端未关闭即可关，前端更严 */
export const canClose = (t: Pick<TicketDetail, 'status' | 'messages'>) =>
  t.status !== 'closed' && t.messages.some((m) => m.author_kind === 'agent')
/** 回复：未关闭即可，resolved 被追问会重开 */
export const canReply = (t: Pick<TicketDetail, 'status'>) => t.status !== 'closed'

/** D-F-2 已决（5.A.2）：客服不显示姓名；系统消息居中灰字，不需要作者 */
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

/** 列表行右上角的时间（样稿 .ti-m time）：今天只写时分，今年写月-日，更早写年-月-日（浏览器本地时区） */
export function listTime(at: string, now: Date = new Date()): string {
  const d = new Date(at)
  if (Number.isNaN(d.getTime())) return ''
  const p = (n: number) => String(n).padStart(2, '0')
  if (d.toDateString() === now.toDateString()) return `${p(d.getHours())}:${p(d.getMinutes())}`
  const md = `${p(d.getMonth() + 1)}-${p(d.getDate())}`
  return d.getFullYear() === now.getFullYear() ? md : `${d.getFullYear()}-${md}`
}

// ---------------------------------------------------------------------------
// 新建表单：与 support.create 同口径（去首尾空白、按字数计），设计稿只要求标题，后端要求正文 ≥ 10 字
// ---------------------------------------------------------------------------
const SUBJECT_RANGE = [4, 120] as const
const BODY_RANGE = [10, 5000] as const
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
  if (s === 0) errors.subject = '请填写标题'
  else if (s < SUBJECT_RANGE[0] || s > SUBJECT_RANGE[1]) errors.subject = `标题需在 ${SUBJECT_RANGE[0]}–${SUBJECT_RANGE[1]} 字之间`
  if (b < BODY_RANGE[0]) errors.body = `至少写 ${BODY_RANGE[0]} 个字，客服才好判断`
  else if (b > BODY_RANGE[1]) errors.body = `详细描述最多 ${BODY_RANGE[1]} 字`
  return errors
}
