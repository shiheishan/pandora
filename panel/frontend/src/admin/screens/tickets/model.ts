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
type Tone = 'danger' | 'neutral' | 'info' | 'ok'

const STATUS_VIEW: Record<TicketStatus, { label: string; tone: Tone }> = {
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

function isOpenStatus(status: TicketStatus): boolean {
  return status !== 'resolved' && status !== 'closed'
}

/** 优先级文案；圆点颜色按优先级值本身取（样稿 .pd.low / normal / high / urgent） */
export const PRIORITY_LABELS: Record<TicketPriority, string> = {
  low: '低',
  normal: '中',
  high: '高',
  urgent: '紧急',
}

export const CATEGORY_LABELS: Record<TicketCategory, string> = {
  general: '一般咨询',
  billing: '账单与支付',
  subscription: '套餐与链接',
  technical: '连接与技术',
  account: '账号与安全',
  abuse: '举报与投诉',
}

export const CLOSED_REASON_LABELS = {
  user_closed: '用户关闭',
  withdrawn: '用户撤回',
  agent_closed: '客服关闭',
  auto_closed: '自动关闭',
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

/**
 * 队列行第一行右侧的等待时长（样稿「等 1 小时 30 分」）：= now − last_reply_at（契约：包含 system 与内部备注，近似值）；
 * 等待用户回复的给「已回复」，已解决 / 已关闭的给「已结束」。late = SLA 已超时且工单没结束（第三行右侧标红「SLA 超时」）
 */
export function waitLabel(t: Pick<Ticket, 'status' | 'last_reply_at' | 'sla_breached'>, now: Date): { text: string; late: boolean } {
  if (!isOpenStatus(t.status)) return { text: '已结束', late: false }
  const late = t.sla_breached === true
  if (t.status === 'pending_user') return { text: '已回复', late }
  const seconds = (now.getTime() - new Date(t.last_reply_at).getTime()) / 1000
  return { text: `等 ${span(Math.max(0, seconds))}`, late }
}

/** 列表行第三行的短编号：工单号后 6 位 */
export function shortNo(ticketNo: string): string {
  return ticketNo.slice(-6)
}

interface SlaLine {
  label: string
  text: string
  tone: 'ok' | 'warn' | 'danger' | 'neutral'
}

/** 属性栏「时限」的两行：首次响应与解决时限，已完成 / 剩余 / 已超时 */
export function slaLines(t: Pick<TicketDetail, 'status' | 'sla_first_response_due' | 'sla_resolution_due' | 'first_responded_at' | 'resolved_at'>, now: Date): SlaLine[] {
  const lines: SlaLine[] = []
  const due = (at: string) => (new Date(at).getTime() - now.getTime()) / 1000
  if (t.first_responded_at) {
    lines.push({ label: '首次响应', text: `已响应 · ${messageTime(t.first_responded_at, now)}`, tone: 'ok' })
  } else {
    const left = due(t.sla_first_response_due)
    lines.push(
      !isOpenStatus(t.status)
        ? { label: '首次响应', text: '未响应即已结束', tone: 'neutral' }
        : left < 0
          ? { label: '首次响应', text: `已超时 ${span(-left)}`, tone: 'danger' }
          : { label: '首次响应', text: `剩 ${span(left)}`, tone: left < 3600 ? 'warn' : 'neutral' },
    )
  }
  const left = due(t.sla_resolution_due)
  if (!isOpenStatus(t.status)) lines.push({ label: '解决时限', text: t.resolved_at ? `已解决 · ${messageTime(t.resolved_at, now)}` : '已结束', tone: 'ok' })
  else lines.push(left < 0 ? { label: '解决时限', text: `已超时 ${span(-left)}`, tone: 'danger' } : { label: '解决时限', text: `剩 ${span(left)}`, tone: left < 3600 ? 'warn' : 'neutral' })
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

/** 对话里的日期分隔（样稿 .day）：今天 / 昨天 / 09-22，跨年带年份 */
export function dayLabel(at: string, now: Date): string {
  const d = new Date(at)
  if (Number.isNaN(d.getTime())) return at
  if (d.toDateString() === now.toDateString()) return '今天'
  const yesterday = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1)
  if (d.toDateString() === yesterday.toDateString()) return '昨天'
  const p = (n: number) => String(n).padStart(2, '0')
  const md = `${p(d.getMonth() + 1)}-${p(d.getDate())}`
  return d.getFullYear() === now.getFullYear() ? md : `${d.getFullYear()}-${md}`
}

/**
 * 系统消息正文是后端拼的「客服将工单状态改为 resolved：原因」，状态码是英文；
 * 只把「改为 <状态码>」里的码换成界面上的叫法，其余原样
 */
export function systemText(body: string): string {
  return body.replace(/改为 (open|pending_user|pending_agent|escalated|resolved|closed)(?=：|$)/, (_, code: TicketStatus) => `改为「${code === 'escalated' ? '已升级' : STATUS_VIEW[code].label}」`)
}

type BubbleKind = 'user' | 'agent' | 'note' | 'system'

export function messageView(m: Message, userEmail: string, now: Date): { kind: BubbleKind; who: string; at: string } {
  const kind: BubbleKind = m.author_kind === 'system' ? 'system' : m.internal_note ? 'note' : m.author_kind
  // 契约映射：who = author_name ?? (user ? user_email : '客服')
  const who = m.author_name ?? (m.author_kind === 'user' ? userEmail || '用户' : m.author_kind === 'system' ? '系统' : '客服')
  return { kind, who, at: messageTime(m.created_at, now) }
}

// ===========================================================================
// 指派下拉：选项文字 display_name ?? email；当前指派人不在目录里（停用、不再是管理员）时也要能显示
// ===========================================================================
export function assigneeOptions(list: readonly Assignee[] | undefined, current: { id?: string; email?: string }) {
  const options = (list ?? []).map((a) => ({ value: a.id, label: a.display_name || a.email }))
  if (current.id && !options.some((o) => o.value === current.id)) options.push({ value: current.id, label: `${current.email ?? current.id.slice(0, 8)}（已不可指派）` })
  return [{ value: '', label: '未指派' }, ...options]
}
