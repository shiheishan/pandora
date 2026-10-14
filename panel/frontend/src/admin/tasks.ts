import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../shell/runtime'

// ---------------------------------------------------------------------------
// 条目按 kind 区分；某类条目可以缺席（如没有待处理时），缺席按 0 计。
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

/** 某类条目的计数；条目不在或该类没有 count（通知积压）时为 0 */
export function taskCount(items: readonly TaskItem[] | undefined, kind: Exclude<TaskItem['kind'], 'notifications_backlog'>): number {
  const item = items?.find((i) => i.kind === kind)
  return item && 'count' in item ? item.count : 0
}
