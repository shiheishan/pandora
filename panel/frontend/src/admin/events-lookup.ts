// ---------------------------------------------------------------------------
// 实时推送只说哪一行变了；弹层打开时按表 + id 调现有单条读接口各取一次（react-query，
// 键按表 + id，staleTime 放长），读到就显示可读内容，404 / 查无此行写「已删除」，
// 读失败或没有单条接口退回通用文案。外框不 import screens/**，这里写最小 schema 与状态文案，
// 口径与 entity-search.ts、各模块 model 一致，那边改了这边跟着改。
// ---------------------------------------------------------------------------
import { useQueries } from '@tanstack/react-query'
import { z } from 'zod'
import { isApiError, type ApiClient } from '../core/api'
import { formatMoney } from '../core/format'
import { useApi } from '../shell/runtime'
import { ORDER_STATUS_TEXT } from './entity-search'
import { MAX_EVENTS, type EventItem, type EventLookup } from './events'

const LOOKUP_STALE_MS = 10 * 60_000
const LOOKUP_GC_MS = 30 * 60_000
/** 一次最多查几条（= events.ts 的 MAX_EVENTS） */
export const LOOKUP_MAX = MAX_EVENTS

export function eventLookupKey(l: EventLookup) {
  return ['admin', 'events', 'lookup', l.table, l.id] as const
}
export function lookupId(l: EventLookup): string {
  return `${l.table}:${l.id}`
}

export const orderLookupSchema = z.object({
  order: z.object({
    order_no: z.string(),
    user_email: z.string(),
    total_amount: z.number().int(),
    status: z.string(),
  }),
})

export const ticketLookupSchema = z.object({
  subject: z.string(),
  user_email: z.string(),
  status: z.string(),
})

export const nodeLookupSchema = z.object({
  nodes: z.array(
    z.object({
      name: z.string(),
      serving_status: z.string(),
      stale: z.boolean(),
    }),
  ),
})

export const serverLookupSchema = z.object({
  name: z.string(),
  status: z.string(),
  heartbeat_online: z.boolean(),
})

export const planLookupSchema = z.object({
  plan: z.object({
    name: z.string(),
    status: z.string(),
  }),
})

export type EventDetail =
  | { table: 'orders'; orderNo: string; userEmail: string; totalAmount: number; status: string }
  | { table: 'tickets'; subject: string; userEmail: string; status: string }
  | { table: 'nodes'; name: string; servingStatus: string; stale: boolean }
  | { table: 'servers'; name: string; status: string; heartbeatOnline: boolean }
  | { table: 'plans'; name: string; status: string }

/** ok = 读到了；gone = 404 或按 id 查不到（已删除）；none = 没查、还在查或读失败（退回通用文案） */
export type LookupState = { kind: 'ok'; detail: EventDetail } | { kind: 'gone' } | { kind: 'none' }

const TICKET_STATUS_TEXT: Record<string, string> = {
  open: '待处理',
  pending_user: '等待用户',
  pending_agent: '处理中',
  escalated: '已升级',
  resolved: '已解决',
  closed: '已关闭',
}

function nodeStatusText(servingStatus: string, stale: boolean): string {
  if (servingStatus === 'active') return stale ? '离线' : '在线'
  if (servingStatus === 'draft') return '草稿'
  if (servingStatus === 'disabled') return '已停用'
  if (servingStatus === 'retired') return '已退役'
  return servingStatus
}

function serverStatusText(status: string, heartbeatOnline: boolean): string {
  if (status === 'draft') return '草稿'
  if (status === 'ready') return heartbeatOnline ? '服务中' : '心跳中断'
  if (status === 'draining') return '维护中'
  if (status === 'maintenance') return '停机维护'
  if (status === 'retired') return '已退役'
  return status
}

const PLAN_STATUS_TEXT: Record<string, string> = {
  draft: '草稿',
  active: '在售',
  archived: '已归档',
}

/** 按表取一行；null = 已删除（404，或节点按 id 查回空数组）；其他错误原样抛出 */
export async function fetchEventDetail(api: ApiClient, l: EventLookup, signal?: AbortSignal): Promise<EventDetail | null> {
  const encId = encodeURIComponent(l.id)
  try {
    switch (l.table) {
      case 'orders': {
        const res = await api.get(`v1/orders/${encId}`, orderLookupSchema, { signal })
        return {
          table: 'orders',
          orderNo: res.order.order_no,
          userEmail: res.order.user_email,
          totalAmount: res.order.total_amount,
          status: res.order.status,
        }
      }
      case 'tickets': {
        const res = await api.get(`v1/tickets/${encId}`, ticketLookupSchema, { signal })
        return {
          table: 'tickets',
          subject: res.subject,
          userEmail: res.user_email,
          status: res.status,
        }
      }
      case 'nodes': {
        const res = await api.get('v1/nodes', nodeLookupSchema, {
          signal,
          query: { id: l.id, include_retired: '1' },
        })
        const row = res.nodes[0]
        if (!row) return null
        return {
          table: 'nodes',
          name: row.name,
          servingStatus: row.serving_status,
          stale: row.stale,
        }
      }
      case 'servers': {
        const res = await api.get(`v1/servers/${encId}`, serverLookupSchema, { signal })
        return {
          table: 'servers',
          name: res.name,
          status: res.status,
          heartbeatOnline: res.heartbeat_online,
        }
      }
      case 'plans': {
        const res = await api.get(`v1/plans/${encId}`, planLookupSchema, { signal })
        return {
          table: 'plans',
          name: res.plan.name,
          status: res.plan.status,
        }
      }
    }
  } catch (error) {
    if (isApiError(error) && error.status === 404) return null
    throw error
  }
}

/** 要查的行：按 items 顺序，跳过 lookup 为 null 的，按 table+id 去重，最多 LOOKUP_MAX 个 */
export function lookupTargets(items: readonly EventItem[]): EventLookup[] {
  const seen = new Set<string>()
  const out: EventLookup[] = []
  for (const item of items) {
    if (!item.lookup) continue
    const key = lookupId(item.lookup)
    if (seen.has(key)) continue
    seen.add(key)
    out.push(item.lookup)
    if (out.length >= LOOKUP_MAX) break
  }
  return out
}

/** 一条事件显示的两行字 */
export function eventText(item: EventItem, state: LookupState | undefined): { title: string; meta: string } {
  if (!state || state.kind === 'none') {
    return { title: item.title, meta: item.meta }
  }
  if (state.kind === 'gone') {
    return { title: item.title, meta: [item.meta, '已删除'].filter(Boolean).join(' · ') }
  }
  const detail = state.detail
  switch (detail.table) {
    case 'orders':
      return {
        title: `${item.title}：${detail.orderNo}`,
        meta: [detail.userEmail, formatMoney(detail.totalAmount), ORDER_STATUS_TEXT[detail.status] ?? detail.status].join(' · '),
      }
    case 'tickets':
      return {
        title: `${item.title}：${detail.subject}`,
        meta: [detail.userEmail, TICKET_STATUS_TEXT[detail.status] ?? detail.status].join(' · '),
      }
    case 'nodes':
      return {
        title: `${item.title}：${detail.name}`,
        meta: nodeStatusText(detail.servingStatus, detail.stale),
      }
    case 'servers':
      return {
        title: `${item.title}：${detail.name}`,
        meta: serverStatusText(detail.status, detail.heartbeatOnline),
      }
    case 'plans':
      return {
        title: `${item.title}：${detail.name}`,
        meta: PLAN_STATUS_TEXT[detail.status] ?? detail.status,
      }
  }
}

/** 弹层打开时（组件挂着才调）查可见条目对应的行；返回 lookupId → 状态 */
export function useEventLookups(items: readonly EventItem[]): Map<string, LookupState> {
  const api = useApi()
  const targets = lookupTargets(items)
  const results = useQueries({
    queries: targets.map((l) => ({
      queryKey: eventLookupKey(l),
      queryFn: ({ signal }: { signal: AbortSignal }) => fetchEventDetail(api, l, signal),
      staleTime: LOOKUP_STALE_MS,
      gcTime: LOOKUP_GC_MS,
      retry: false,
    })),
  })
  const map = new Map<string, LookupState>()
  targets.forEach((l, i) => {
    const r = results[i]
    if (!r) {
      map.set(lookupId(l), { kind: 'none' })
      return
    }
    if (r.data === null) {
      map.set(lookupId(l), { kind: 'gone' })
    } else if (r.data !== undefined) {
      map.set(lookupId(l), { kind: 'ok', detail: r.data })
    } else {
      map.set(lookupId(l), { kind: 'none' })
    }
  })
  return map
}
