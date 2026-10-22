import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../shell/runtime'
import type { ModuleKey } from './modules'

// ---------------------------------------------------------------------------
// 条目按 kind 区分；某类条目可以缺席（如没有待处理时），缺席按 0 计。
// 写严：未知 kind 判为不符约定，而不是悄悄吞掉。
// ---------------------------------------------------------------------------
const int = z.number().int()
const count = int.nonnegative()

const taskItemSchema = z.discriminatedUnion('kind', [
  z.object({ kind: z.literal('tickets_open'), count, high_priority: count, oldest_wait_seconds: count.nullable() }),
  z.object({ kind: z.literal('withdrawals_pending'), count, amount: int }),
  z.object({
    kind: z.literal('nodes_offline'),
    count,
    sample: z.array(z.object({ id: z.string(), name: z.string() })).max(3),
    longest_offline_seconds: count.nullable(),
  }),
  // 证书要处理的节点：申请或续期失败、快到期或已过期（门槛：IP 证书 2 天、域名与手填证书 14 天）
  z.object({
    kind: z.literal('node_certs'),
    count,
    failing: count,
    expiring: count,
    sample: z.array(z.object({ id: z.string(), name: z.string() })).max(3),
  }),
  z.object({ kind: z.literal('orders_pending_stale'), count, threshold_seconds: count }),
  z.object({ kind: z.literal('notifications_backlog'), queued: count, failed_total: count, backlog_state: z.enum(['clear', 'backlogged']) }),
  // 数据巡检：每项检查最近一次的违例数（code 是后端的检查代号，中文名见 dash/model 的 INTEGRITY_CHECKS）
  z.object({
    kind: z.literal('integrity'),
    checks: z.array(z.object({ code: z.string().min(1), count, checked_at: z.string() })),
  }),
])
export const tasksSchema = z.object({ as_of: z.string(), items: z.array(taskItemSchema) })
export type TaskItem = z.output<typeof taskItemSchema>

const TASKS_QUERY_KEY = ['admin', 'dashboard', 'tasks'] as const

export function useDashboardTasks() {
  const api = useApi()
  return useQuery({
    queryKey: TASKS_QUERY_KEY,
    queryFn: ({ signal }) => api.get('v1/dashboard/tasks', tasksSchema, { signal }),
    meta: { topics: ['tickets.changed', 'orders.changed', 'nodes.changed'] },
    refetchInterval: 60_000,
  })
}

/** 某类条目的计数；条目不在时为 0（通知积压、数据巡检没有单一 count，不走这里） */
export function taskCount(
  items: readonly TaskItem[] | undefined,
  kind: Exclude<TaskItem['kind'], 'notifications_backlog' | 'integrity'>,
): number {
  const item = items?.find((i) => i.kind === kind)
  return item && 'count' in item ? item.count : 0
}

export interface NavBadge {
  count: number
  /** 数字数的是什么（悬停提示）：一个模块下有好几种「待处理」，只看数字容易对错口径 */
  label: string
  /** 红色（工单）；其余是灰色等宽数字 */
  hot?: boolean
}

/**
 * 侧栏徽标：工单 = tickets_open（红）、订单与收款 = orders_pending_stale、营销 = withdrawals_pending、节点与服务器 = nodes_offline；为 0 的不返回。
 * 证书异常只在仪表盘出卡，不进侧栏徽标。订单与收款的数字与仪表盘「超时未支付订单」同口径，不含「待处理到账」（钱到了没对上订单，另一回事）
 */
export function navBadges(items: readonly TaskItem[] | undefined): Partial<Record<ModuleKey, NavBadge>> {
  if (!items) return {}
  const out: Partial<Record<ModuleKey, NavBadge>> = {}
  const tickets = taskCount(items, 'tickets_open')
  if (tickets > 0) out.tickets = { count: tickets, label: `待处理工单 ${tickets} 个`, hot: true }
  const stale = items.find((i): i is Extract<TaskItem, { kind: 'orders_pending_stale' }> => i.kind === 'orders_pending_stale')
  if (stale && stale.count > 0) {
    const minutes = Math.round(stale.threshold_seconds / 60)
    out.billing = { count: stale.count, label: `超时未支付订单 ${stale.count} 个：下单超过 ${minutes} 分钟还没到账，与「待处理到账」无关` }
  }
  const withdrawals = taskCount(items, 'withdrawals_pending')
  if (withdrawals > 0) out.marketing = { count: withdrawals, label: `提现待审核 ${withdrawals} 笔` }
  const nodes = taskCount(items, 'nodes_offline')
  if (nodes > 0) out.nodes = { count: nodes, label: `离线节点 ${nodes} 个` }
  return out
}

/** 离线节点名（待办 nodes_offline 的 sample，最多 3 个）；条目缺席时为空，仪表盘系统状态用来点名 */
export function offlineNodeNames(items: readonly TaskItem[] | undefined): string[] {
  const item = items?.find((i): i is Extract<TaskItem, { kind: 'nodes_offline' }> => i.kind === 'nodes_offline')
  return item ? item.sample.map((n) => n.name) : []
}

/** 侧栏底部工单提醒卡：tickets_open 的数量与最久等待秒数；数量为 0（或条目缺席）时返回 null */
export function ticketAlert(items: readonly TaskItem[] | undefined): { count: number; oldestWaitSeconds: number | null } | null {
  const item = items?.find((i): i is Extract<TaskItem, { kind: 'tickets_open' }> => i.kind === 'tickets_open')
  if (!item || item.count === 0) return null
  return { count: item.count, oldestWaitSeconds: item.oldest_wait_seconds }
}

/** 等待时长的中文：< 60 秒「不到 1 分钟」，< 1 小时「N 分钟」，< 1 天「N 小时」，否则「N 天」（都向下取整） */
export function formatWait(seconds: number): string {
  if (seconds < 60) return '不到 1 分钟'
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} 小时`
  return `${Math.floor(seconds / 86400)} 天`
}
