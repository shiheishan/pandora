import { z } from 'zod'
import type { SseEvent } from '../core/sse'

// ---------------------------------------------------------------------------
// 实时事件的纯函数模型。事件只说「哪张表哪一行变了」，没有业务内容（10-16 用户定）：
// 通用标题与 meta 仍按 topic + op 生成，打开弹层后再按 lookup 查单条接口补上可读内容（见 events-lookup.ts）。
// 点击跳到对应后台地址。已读状态只在内存里，不持久化，刷新即清。
// ---------------------------------------------------------------------------
export type EventTone = 'ok' | 'warn' | 'danger' | 'info'
export type EventTab = 'all' | 'orders' | 'subscriptions' | 'tickets' | 'nodes' | 'other'

export type LookupTable = 'orders' | 'tickets' | 'nodes' | 'servers' | 'plans'
export interface EventLookup {
  table: LookupTable
  id: string
}

export const EVENT_TABS: ReadonlyArray<readonly [EventTab, string]> = [
  ['all', '全部'],
  ['orders', '订单'],
  ['subscriptions', '订阅'],
  ['tickets', '工单'],
  ['nodes', '节点'],
  ['other', '其他'],
]

export const MAX_EVENTS = 30

export interface EventItem {
  key: number
  title: string
  /** 第二行灰字：区域名 · 短 id（或开关 code） */
  meta: string
  at: Date
  tone: EventTone
  tab: Exclude<EventTab, 'all'>
  /** 点击后去的后台地址（hash 路径，如 /billing/orders/<id>）；null = 不跳转 */
  path: string | null
  unread: boolean
  /** 要查的那一行；null = 没有单条接口、删除事件或没有 id，只显示通用文案 */
  lookup: EventLookup | null
}

// 载荷 schema 沿用现有契约：全部字段可选；data 不是 JSON 或解析失败时按空载荷处理。
const payloadSchema = z.object({
  table: z.string().optional(),
  op: z.string().optional(),
  id: z.string().optional(),
  ticket_id: z.string().optional(),
  code: z.string().optional(),
  enabled: z.boolean().optional(),
})

type Payload = z.infer<typeof payloadSchema>

function parsePayload(data: string): Payload {
  try {
    const parsed = payloadSchema.safeParse(JSON.parse(data))
    return parsed.success ? parsed.data : {}
  } catch {
    return {}
  }
}

const OPS = ['INSERT', 'UPDATE', 'DELETE'] as const
type Op = (typeof OPS)[number]

function readOp(payload: Payload): Op {
  return payload.op && (OPS as readonly string[]).includes(payload.op) ? (payload.op as Op) : 'UPDATE'
}

/** op → 三段标题的取法：常规表按 插入/更新/删除 三种，特殊表三段一样 */
const titles = (insert: string, update: string, del: string) => (_payload: Payload, op: Op) => (op === 'INSERT' ? insert : op === 'DELETE' ? del : update)
const fixedTitle = (title: string) => () => title

interface TopicSpec {
  tab: Exclude<EventTab, 'all'>
  tone: EventTone
  area: string
  title: (payload: Payload, op: Op) => string
  /** 返回详情路径；没有 id 时给列表路径 */
  path: (id: string | undefined, payload: Payload) => string | null
  /** meta 第二段：短 id，或开关 code；空串 = 不显示 */
  metaId: (payload: Payload) => string
  lookup?: (payload: Payload) => LookupTable | null
}

/** 短 id：前 8 位，没有就空串 */
const shortId = (id: string | undefined) => (id ?? '').slice(0, 8)

const detailPath = (list: string, detail: (id: string) => string) => (id: string | undefined) => (id ? detail(encodeURIComponent(id)) : list)

const TOPICS: Record<string, TopicSpec> = {
  'orders.changed': {
    tab: 'orders',
    tone: 'ok',
    area: '订单与收款',
    title: titles('新订单', '订单有变化', '订单已删除'),
    path: detailPath('/billing/orders', (id) => `/billing/orders/${id}`),
    metaId: (p) => shortId(p.id),
    lookup: (p) => (!p.table || p.table === 'orders' ? 'orders' : null),
  },
  'subscriptions.changed': {
    tab: 'subscriptions',
    tone: 'info',
    area: '用户',
    title: titles('新订阅', '订阅有变化', '订阅已删除'),
    // id 是订阅 id，不是用户 id，详情页定位不到，恒回用户列表
    path: () => '/users/list',
    metaId: (p) => shortId(p.id),
  },
  'tickets.changed': {
    tab: 'tickets',
    tone: 'danger',
    area: '工单',
    title: (payload, op) =>
      payload.table === 'ticket_messages'
        ? fixedTitle('工单有新消息')()
        : titles('新工单', '工单有更新', '工单已删除')(payload, op),
    // ticket_messages 的 id 是消息 id，定位不到工单，恒回工单列表
    path: (id, payload) => (payload.table === 'ticket_messages' || !id ? '/tickets' : `/tickets/${encodeURIComponent(id)}`),
    metaId: (p) => shortId(p.id),
    lookup: (p) => (!p.table || p.table === 'tickets' ? 'tickets' : null),
  },
  'ticket.updated': {
    tab: 'tickets',
    tone: 'danger',
    area: '工单',
    title: fixedTitle('工单有新回复'),
    // 短 id 取 ticket_id；没有 ticket_id 时退回工单列表
    path: (_id, payload) => (payload.ticket_id ? `/tickets/${encodeURIComponent(payload.ticket_id)}` : '/tickets'),
    metaId: (p) => shortId(p.ticket_id),
  },
  'nodes.changed': {
    tab: 'nodes',
    tone: 'warn',
    area: '节点与服务器',
    title: (payload, op) =>
      payload.table === 'servers'
        ? titles('新服务器', '服务器有变化', '服务器已删除')(payload, op)
        : titles('新节点', '节点有变化', '节点已删除')(payload, op),
    path: (id, payload) => (payload.table === 'servers' ? detailPath('/nodes/servers', (x) => `/nodes/servers/${x}`)(id) : detailPath('/nodes/nodes', (x) => `/nodes/nodes/${x}`)(id)),
    metaId: (p) => shortId(p.id),
    lookup: (p) => (p.table === 'servers' ? 'servers' : !p.table || p.table === 'nodes' ? 'nodes' : null),
  },
  'plans.changed': {
    tab: 'other',
    tone: 'info',
    area: '套餐',
    title: (payload, op) =>
      payload.table && payload.table !== 'plans'
        ? fixedTitle('套餐有变化')()
        : titles('新套餐', '套餐有变化', '套餐已删除')(payload, op),
    // plan_versions、prices 等子表的 id 不是套餐 id，恒回套餐列表
    path: (id, payload) => (payload.table === 'plans' || !payload.table ? detailPath('/plans', (x) => `/plans/${x}`)(id) : '/plans'),
    metaId: (p) => shortId(p.id),
    lookup: (p) => (!p.table || p.table === 'plans' ? 'plans' : null),
  },
  'announcements.changed': {
    tab: 'other',
    tone: 'info',
    area: '内容与外观',
    title: titles('新公告', '公告已更新', '公告已删除'),
    path: () => '/content/announce',
    metaId: (p) => shortId(p.id),
  },
  'switches.changed': {
    tab: 'other',
    tone: 'warn',
    area: '安全与运维',
    title: (payload) => (payload.enabled === undefined ? '降级开关有变化' : payload.enabled ? '降级开关已恢复' : '降级开关已暂停'),
    path: () => '/security/switches',
    metaId: (p) => p.code ?? '',
  },
}

const FALLBACK: TopicSpec = {
  tab: 'other',
  tone: 'info',
  area: '',
  title: fixedTitle('数据有变化'),
  path: () => null,
  metaId: () => '',
}

export function describeEvent(event: SseEvent, key: number, at: Date = new Date()): EventItem {
  const spec = TOPICS[event.event] ?? FALLBACK
  const payload = parsePayload(event.data)
  const op = readOp(payload)
  // 开关极性：enabled = 功能可用，false = 进入降级（与降级开关页一致）
  const tone: EventTone =
    event.event === 'switches.changed' && payload.enabled !== undefined ? (payload.enabled ? 'ok' : 'warn') : spec.tone
  const metaId = spec.metaId(payload)
  const lookupTable = op !== 'DELETE' && payload.id ? (spec.lookup?.(payload) ?? null) : null
  const lookup: EventLookup | null = lookupTable && payload.id ? { table: lookupTable, id: payload.id } : null
  return {
    key,
    title: spec.title(payload, op),
    meta: [spec.area, metaId].filter(Boolean).join(' · '),
    at,
    tone,
    tab: spec.tab,
    // 删掉的行打不开详情，退回列表
    path: spec.path(op === 'DELETE' ? undefined : payload.id, payload),
    unread: true,
    lookup,
  }
}

/** 新的放最前，截到 MAX_EVENTS */
export function pushEvent(list: readonly EventItem[], item: EventItem): EventItem[] {
  return [item, ...list].slice(0, MAX_EVENTS)
}

/** 按页签过滤；all = 全部 */
export function eventsInTab(list: readonly EventItem[], tab: EventTab): EventItem[] {
  return tab === 'all' ? [...list] : list.filter((item) => item.tab === tab)
}

/** 只把这一条 unread 置 false；本来就已读时返回原数组（引用不变，便于 React 跳过渲染） */
export function markRead(list: readonly EventItem[], key: number): EventItem[] {
  const index = list.findIndex((item) => item.key === key && item.unread)
  if (index < 0) return list as EventItem[]
  return list.map((item, i) => (i === index ? { ...item, unread: false } : item))
}

export function markAllRead(list: readonly EventItem[]): EventItem[] {
  if (!list.some((item) => item.unread)) return list as EventItem[]
  return list.map((item) => (item.unread ? { ...item, unread: false } : item))
}

export function unreadCount(list: readonly EventItem[]): number {
  return list.reduce((n, item) => n + (item.unread ? 1 : 0), 0)
}
