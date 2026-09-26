/**
 * [INPUT]: 依赖 ./api 的 Ticket / TicketDetail / Message / Assignee 类型与 QueueParams
 * [OUTPUT]: 对外提供 FILTERS 与 QueueFilter、queueParams、STATUS_VIEW / statusView、STATUS_OPTIONS、PRIORITY_VIEW、CATEGORY_LABELS、CLOSED_REASON_LABELS、isOpenStatus、waitLabel、slaLines、messageView、messageTime、assigneeOptions、systemText、MESSAGE_MAX
 * [POS]: admin/screens/tickets 的纯逻辑：契约后台-02 的状态 / 优先级 / 分类映射表、分段筛选到后端 query 的映射、等待时长与 SLA 文案、消息气泡的角色判定；不碰 React 与网络，model.test.ts 覆盖
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { Assignee, Message, QueueParams, Ticket, TicketCategory, TicketDetail, TicketPriority, TicketStatus } from './api'

/** 回复与备注的长度上限（后端 trim 后 1–5000 字） */
export const MESSAGE_MAX = 5000

// ===========================================================================
// 分段筛选 → 后端 query（契约后台-02 GET v1/tickets 的映射）
// ===========================================================================
export const FILTERS = [
  ['active', '未解决'],
  ['todo', '待处理'],
  ['mine', '我的'],
  // 左栏最窄 240，五段中文放不下「SLA 超时」四个字，缩成「超时」
  ['breached', '超时'],
  ['all', '全部'],
] as const
export type QueueFilter = (typeof FILTERS)[number][0]

const UNRESOLVED = 'open,pending_user,pending_agent,escalated'
// 「待处理」= 需要客服动作：设计稿只筛 open，后端「待客服处理」的语义包含这三个
const NEEDS_AGENT = 'open,pending_agent,escalated'

export function isFilter(value: string | null): value is QueueFilter {
  return FILTERS.some(([v]) => v === value)
}

export function queueParams(filter: QueueFilter, me: string | undefined, q: string): QueueParams {
  const text = q.trim()
  const base: QueueParams = text ? { q: text } : {}
  switch (filter) {
    case 'active':
      return { ...base, status: UNRESOLVED }
    case 'todo':
      return { ...base, status: NEEDS_AGENT }
    case 'mine':
      // me 还没回来时不能漏掉 assigned_to，否则「我的」会退化成全部未解决
      return { ...base, status: UNRESOLVED, assigned_to: me ?? '-' }
    case 'breached':
      return { ...base, breached: '1' }
    case 'all':
      return base
  }
}

// ===========================================================================
// 状态：后端 6 个值 → 设计稿 4 种色 + 「已关闭」（待补·前端）
// ===========================================================================
export type Tone = 'danger' | 'neutral' | 'info' | 'ok'

export const STATUS_VIEW: Record<TicketStatus, { label: string; tone: Tone }> = {
  open: { label: '待处理', tone: 'danger' },
  pending_user: { label: '等待用户', tone: 'neutral' },
  pending_agent: { label: '处理中', tone: 'info' },
  // 设计稿：escalated 显示「处理中」，另挂「已升级」徽标（徽标看 escalated_at）
  escalated: { label: '处理中', tone: 'info' },
  resolved: { label: '已解决', tone: 'ok' },
  closed: { label: '已关闭', tone: 'ok' },
}

export function statusView(status: TicketStatus) {
  return STATUS_VIEW[status]
}

/**
 * 「状态」下拉：客服能直接设的只有 pending_agent / resolved / closed（escalated 只经「升级」按钮）；
 * open、pending_user 置灰，escalated 只在当前就是它时出现（同样置灰），保证下拉能显示当前值。
 */
export function STATUS_OPTIONS(current: TicketStatus) {
  const options: Array<{ value: TicketStatus; label: string; disabled: boolean }> = [
    { value: 'open', label: '待处理', disabled: true },
    { value: 'pending_user', label: '等待用户', disabled: true },
    { value: 'pending_agent', label: '处理中', disabled: false },
    { value: 'resolved', label: '已解决', disabled: false },
    { value: 'closed', label: '已关闭', disabled: false },
  ]
  if (current === 'escalated') options.splice(2, 0, { value: 'escalated', label: '处理中 · 已升级', disabled: true })
  return options
}

export function isOpenStatus(status: TicketStatus): boolean {
  return status !== 'resolved' && status !== 'closed'
}

export const PRIORITY_VIEW: Record<TicketPriority, { label: string; dot: 'low' | 'mid' | 'high'; strong: boolean }> = {
  low: { label: '低', dot: 'low', strong: false },
  normal: { label: '中', dot: 'mid', strong: false },
  high: { label: '高', dot: 'high', strong: false },
  urgent: { label: '紧急', dot: 'high', strong: true },
}

export const CATEGORY_LABELS: Record<TicketCategory, string> = {
  general: '一般咨询',
  billing: '账单与支付',
  subscription: '订阅与套餐',
  technical: '连接与技术',
  account: '账号与安全',
  abuse: '举报与投诉',
}

export const CLOSED_REASON_LABELS = {
  user_closed: '用户关闭',
  withdrawn: '用户撤回',
  agent_closed: '客服关闭',
} as const

// ===========================================================================
// 时长
// ===========================================================================
function span(seconds: number): string {
  const minutes = Math.max(1, Math.floor(seconds / 60))
  if (minutes < 60) return `${minutes} 分钟`
  const hours = Math.floor(minutes / 60)
  if (hours < 48) return minutes % 60 ? `${hours} 小时 ${minutes % 60} 分` : `${hours} 小时`
  return `${Math.floor(hours / 24)} 天`
}

/** 队列行右侧：等待时长 = now − last_reply_at（契约：包含 system 与内部备注，近似值） */
export function waitLabel(t: Pick<Ticket, 'status' | 'last_reply_at' | 'sla_breached'>, now: Date): { text: string; late: boolean } {
  if (!isOpenStatus(t.status)) return { text: '已结束', late: false }
  const seconds = (now.getTime() - new Date(t.last_reply_at).getTime()) / 1000
  const text = `等待 ${span(Math.max(0, seconds))}`
  return t.sla_breached ? { text: `${text} · SLA 超时`, late: true } : { text, late: false }
}

export interface SlaLine {
  label: string
  text: string
  tone: 'ok' | 'warn' | 'danger' | 'neutral'
}

/** 详情头的 SLA：首次响应与解决两条，已完成 / 剩余 / 已超时（待补·前端） */
export function slaLines(t: Pick<TicketDetail, 'status' | 'sla_first_response_due' | 'sla_resolution_due' | 'first_responded_at' | 'resolved_at'>, now: Date): SlaLine[] {
  const lines: SlaLine[] = []
  const due = (at: string) => (new Date(at).getTime() - now.getTime()) / 1000
  if (t.first_responded_at) {
    lines.push({ label: '首次响应', text: `已响应 · ${messageTime(t.first_responded_at, now)}`, tone: 'ok' })
  } else if (t.sla_first_response_due) {
    const left = due(t.sla_first_response_due)
    lines.push(
      !isOpenStatus(t.status)
        ? { label: '首次响应', text: '未响应即已结束', tone: 'neutral' }
        : left < 0
          ? { label: '首次响应', text: `已超时 ${span(-left)}`, tone: 'danger' }
          : { label: '首次响应', text: `剩 ${span(left)}`, tone: left < 3600 ? 'warn' : 'neutral' },
    )
  }
  if (t.sla_resolution_due) {
    const left = due(t.sla_resolution_due)
    if (!isOpenStatus(t.status)) lines.push({ label: '解决时限', text: t.resolved_at ? `已解决 · ${messageTime(t.resolved_at, now)}` : '已结束', tone: 'ok' })
    else lines.push(left < 0 ? { label: '解决时限', text: `已超时 ${span(-left)}`, tone: 'danger' } : { label: '解决时限', text: `剩 ${span(left)}`, tone: left < 3600 ? 'warn' : 'neutral' })
  }
  return lines
}

// ===========================================================================
// 消息
// ===========================================================================
/** 今天的消息只给「21:04」，更早的给「09-22 11:20」，跨年再带年份 */
export function messageTime(at: string, now: Date): string {
  const d = new Date(at)
  if (Number.isNaN(d.getTime())) return at
  const p = (n: number) => String(n).padStart(2, '0')
  const hm = `${p(d.getHours())}:${p(d.getMinutes())}`
  if (d.toDateString() === now.toDateString()) return hm
  const md = `${p(d.getMonth() + 1)}-${p(d.getDate())} ${hm}`
  return d.getFullYear() === now.getFullYear() ? md : `${d.getFullYear()}-${md}`
}

/**
 * 系统消息正文是后端拼的「客服将工单状态改为 resolved：原因」，状态码是英文；
 * 只把「改为 <状态码>」里的码换成界面上的叫法，其余原样
 */
export function systemText(body: string): string {
  return body.replace(/改为 (open|pending_user|pending_agent|escalated|resolved|closed)(?=：|$)/, (_, code: TicketStatus) => `改为「${code === 'escalated' ? '已升级' : STATUS_VIEW[code].label}」`)
}

export type BubbleKind = 'user' | 'agent' | 'note' | 'system'

export function messageView(m: Message, userEmail: string | undefined, now: Date): { kind: BubbleKind; who: string; at: string } {
  const kind: BubbleKind = m.author_kind === 'system' ? 'system' : m.internal_note ? 'note' : m.author_kind
  // 契约映射：who = author_name ?? (user ? user_email : '客服')
  const who = m.author_name ?? (m.author_kind === 'user' ? (userEmail ?? '用户') : m.author_kind === 'system' ? '系统' : '客服')
  return { kind, who, at: messageTime(m.created_at, now) }
}

// ===========================================================================
// 指派下拉：选项文字 display_name ?? email；当前指派人不在目录里（停用、失去权限）时也要能显示
// ===========================================================================
export function assigneeOptions(list: readonly Assignee[] | undefined, current: { id?: string; email?: string }) {
  const options = (list ?? []).map((a) => ({ value: a.id, label: a.display_name || a.email }))
  if (current.id && !options.some((o) => o.value === current.id)) options.push({ value: current.id, label: `${current.email ?? current.id.slice(0, 8)}（已不可指派）` })
  return [{ value: '', label: '未指派' }, ...options]
}
