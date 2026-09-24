/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useQuery，依赖 zod，依赖 ../shell/runtime 的 useApi
 * [OUTPUT]: 对外提供 tasksSchema、TaskItem、Tasks、TASKS_QUERY_KEY、useDashboardTasks、taskCount
 * [POS]: admin 的「需要处理」计数：GET v1/dashboard/tasks（待补·后端，ops.dashboard.read，契约后台-01 含修订 R51）。侧栏徽标与仪表盘「需要处理」卡片共用这一个查询键、这一份严格 schema，同一页只发一次请求、缓存里只有一种形状
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../shell/runtime'

// ---------------------------------------------------------------------------
// 条目按 kind 区分，处理器已按各条目的原读权限过滤（没有权限的条目不出现）：
// tickets_open←ops.ticket.read、withdrawals_pending←marketing.commission.read（R51）、
// nodes_offline←node.read、orders_pending_stale←billing.order.read、
// notifications_backlog←ops.notification.read、ledger_drift←billing.ledger.read。
// 写严：未知 kind 判为不符约定，而不是悄悄吞掉。
// ---------------------------------------------------------------------------
const int = z.number().int()
const count = int.nonnegative()

const taskItemSchema = z.discriminatedUnion('kind', [
  z.object({ kind: z.literal('tickets_open'), count, high_priority: count, oldest_wait_seconds: count.nullable() }),
  z.object({ kind: z.literal('withdrawals_pending'), count, amounts: z.array(z.object({ currency: z.string().min(1), amount: int })) }),
  z.object({
    kind: z.literal('nodes_offline'),
    count,
    sample: z.array(z.object({ id: z.string(), name: z.string() })).max(3),
    longest_offline_seconds: count.nullable(),
  }),
  z.object({ kind: z.literal('orders_pending_stale'), count, threshold_seconds: count }),
  z.object({ kind: z.literal('notifications_backlog'), queued: count, failed_total: count, backlog_state: z.enum(['clear', 'backlogged']) }),
  z.object({ kind: z.literal('ledger_drift'), count }),
])
export const tasksSchema = z.object({ as_of: z.string(), items: z.array(taskItemSchema) })
export type TaskItem = z.output<typeof taskItemSchema>
export type Tasks = z.output<typeof tasksSchema>

export const TASKS_QUERY_KEY = ['admin', 'dashboard', 'tasks'] as const

/** enabled 由调用方按 ops.dashboard.read 决定：缺权限不发请求（接口会回 404） */
export function useDashboardTasks(enabled: boolean) {
  const api = useApi()
  return useQuery({
    queryKey: TASKS_QUERY_KEY,
    queryFn: ({ signal }) => api.get('v1/dashboard/tasks', tasksSchema, { signal }),
    enabled,
    meta: { topics: ['tickets.changed', 'orders.changed', 'nodes.changed'] },
    refetchInterval: 60_000,
  })
}

/** 某类条目的计数；条目不在（无权限）或该类没有 count（通知积压）时为 0 */
export function taskCount(items: readonly TaskItem[] | undefined, kind: Exclude<TaskItem['kind'], 'notifications_backlog'>): number {
  const item = items?.find((i) => i.kind === kind)
  return item && 'count' in item ? item.count : 0
}
