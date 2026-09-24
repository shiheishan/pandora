/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useQuery / keepPreviousData，依赖 zod，依赖 ../../../shell/runtime 的 useApi
 * [OUTPUT]: 对外提供仪表盘八个读接口的 zod schema 与类型（Tasks、Backlog、Overview、Revenue、NodeTraffic、UserTraffic、SystemStatus、Activity 等）及对应的 useXxx 查询 hook
 * [POS]: admin/screens/dash 的数据层：只经 core/api.ts 取数、只经 react-query 缓存；形状逐字照 api-contract.md 后台-01（待补·后端字段一律可选），流量与积压三件照 DASH-01 冻结契约；model.ts 消费这里的类型，界面组件消费这里的 hook
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../../../shell/runtime'

// ---------------------------------------------------------------------------
// 共用片段。本机没有真网关，zod 是和后端对账的唯一防线：按契约写全、写严，
// 待补·后端的字段写 optional，后端补上即生效；多一个未知 kind 就判为不符约定。
// ---------------------------------------------------------------------------
const int = z.number().int()
const count = int.nonnegative()
/** DASH-01：numeric 聚合的字节一律是十进制字符串，禁止 JSON number */
const decimalBytes = z.string().regex(/^\d+$/)
const currency = z.string().min(1)

// ---------------------------------------------------------------------------
// GET v1/dashboard/tasks（待补·后端，ops.dashboard.read）：条目按各自读权限过滤
// ---------------------------------------------------------------------------
const taskItemSchema = z.discriminatedUnion('kind', [
  z.object({ kind: z.literal('tickets_open'), count, high_priority: count, oldest_wait_seconds: count.nullable() }),
  z.object({ kind: z.literal('withdrawals_pending'), count, amounts: z.array(z.object({ currency, amount: int })) }),
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

// ---------------------------------------------------------------------------
// GET v1/dashboard/backlog/notifications（DASH-01 冻结，ops.notification.read）
// ---------------------------------------------------------------------------
export const backlogSchema = z.object({
  as_of: z.string(),
  backlog_state: z.enum(['clear', 'backlogged']),
  processor_state: z.literal('unobservable'),
  scanner_interval_seconds: count,
  counts: z.object({
    ready: count,
    ready_retry: count,
    scheduled: count,
    scheduled_retry: count,
    sending_unobservable: count,
    failed_total: count,
    suppressed_total: count,
    bounced_total: count,
  }),
  oldest_ready_at: z.string().nullable(),
  max_ready_lag_seconds: count,
  last_sent_at: z.string().nullable(),
  assessment: z.object({ threshold_seconds: count, reason: z.enum(['no_due_backlog', 'within_threshold', 'lag_exceeded']) }),
})
export type Backlog = z.output<typeof backlogSchema>

// ---------------------------------------------------------------------------
// GET v1/overview（现有 + 待补·后端追加字段，billing.ledger.read）
// ---------------------------------------------------------------------------
const revenueRowSchema = z.object({
  currency,
  today: int,
  last_7_days: int,
  last_30_days: int,
  total: int,
  actual_today: int,
  actual_7_days: int,
  actual_30_days: int,
  actual_total: int,
  adjustment_today: int,
  adjustment_7_days: int,
  adjustment_30_days: int,
  adjustment_total: int,
  // 待补·后端
  yesterday: int.optional(),
  actual_yesterday: int.optional(),
})
export const overviewSchema = z.object({
  users: z.object({ total: count, active: count, today: count, last_7_days: count }),
  subscriptions: z.object({ active: count, trialing: count, expiring_7_days: count, expired: count, new_7_days: count.optional() }),
  revenue: z.array(revenueRowSchema),
  orders: z.object({ paid_today: count, pending: count, failed_today: count }),
  ledger_drift_accounts: count,
  // 待补·后端
  nodes: z.object({ total: count, online: count }).optional(),
})
export type RevenueRow = z.output<typeof revenueRowSchema>
export type Overview = z.output<typeof overviewSchema>

// ---------------------------------------------------------------------------
// GET v1/revenue/timeseries（现有 + 待补 previous_total，billing.ledger.read）
// ---------------------------------------------------------------------------
export type RevenueCurrency = 'CNY' | 'USD'
export type RevenueDays = 7 | 30 | 90
const revenuePointSchema = z.object({
  date: z.string().regex(/^\d{4}-\d{2}-\d{2}$/),
  actual_credit: int,
  actual_debit: int,
  adjustment: int,
  displayed_net: int,
})
export const revenueSchema = z.object({
  currency,
  days: count,
  points: z.array(revenuePointSchema),
  previous_total: int.optional(),
})
export type RevenuePoint = z.output<typeof revenuePointSchema>
export type Revenue = z.output<typeof revenueSchema>

// ---------------------------------------------------------------------------
// GET v1/dashboard/traffic/{nodes,users}（DASH-01 冻结）。用户排行只有脱敏邮箱
// （待决 D-A-2 未决前照此显示，也不得用别处已加载的对象补回完整邮箱）
// ---------------------------------------------------------------------------
const trafficBase = {
  range: z.enum(['24h', '7d', '30d']),
  snapshot_at: z.string(),
  from: z.string(),
  to: z.string(),
  basis: z.literal('strict_raw_report_entries'),
  totals: z.object({ reported_bytes: decimalBytes, attributed_bytes: decimalBytes, unattributed_bytes: decimalBytes }),
  quality: z.object({ duplicate_report_count: count, invalid_report_count: count, invalid_entry_count: count }),
}
const trafficItemBase = {
  upload_bytes: decimalBytes,
  download_bytes: decimalBytes,
  total_bytes: decimalBytes,
  contributing_entry_count: count,
  last_report_at: z.string(),
}
export const nodeTrafficSchema = z.object({
  ...trafficBase,
  items: z.array(z.object({ node_id: z.string(), name: z.string(), display_name: z.string().nullable(), report_count: count, ...trafficItemBase })),
  ranking: z.object({ returned_bytes: decimalBytes, other_node_bytes: decimalBytes }),
})
export const userTrafficSchema = z.object({
  ...trafficBase,
  items: z.array(z.object({ user_id: z.string(), email_masked: z.string(), subscription_count: count, ...trafficItemBase })),
  ranking: z.object({ returned_bytes: decimalBytes, other_user_bytes: decimalBytes }),
})
export type NodeTraffic = z.output<typeof nodeTrafficSchema>
export type UserTraffic = z.output<typeof userTrafficSchema>

// ---------------------------------------------------------------------------
// GET v1/system/status（现有 backup / database + 待补·后端 state / components，security.audit.read）
// ---------------------------------------------------------------------------
const backupFileSchema = z.object({ name: z.string(), size: count, created_at: z.string(), has_checksum: z.boolean() })
const backupSchema = z.object({
  dir: z.string(),
  readable: z.boolean(),
  message: z.string().optional(),
  count: count.optional(),
  total_bytes: count.optional(),
  latest: backupFileSchema.optional(),
  latest_age_hours: count.optional(),
  stale: z.boolean().optional(),
  missing_checksum: count.optional(),
  recent: z.array(backupFileSchema).optional(),
  identity_configured: z.boolean().optional(),
  identity_hint: z.string().optional(),
  offsite_configured: z.boolean().optional(),
})
const componentState = z.enum(['ok', 'warn', 'down', 'unknown'])
const componentBase = { state: componentState, latency_ms: z.number().nonnegative().optional(), message: z.string().optional() }
const queueMetrics = z.object({ queued: count, retrying: count, failed_total: count })
const componentSchema = z.discriminatedUnion('key', [
  z.object({ key: z.literal('postgres'), ...componentBase, metrics: z.object({ size_bytes: count, connections: count, max_connections: count }) }),
  z.object({ key: z.literal('valkey'), ...componentBase, metrics: z.object({}) }),
  z.object({ key: z.literal('node_fabric'), ...componentBase, metrics: z.object({ total: count, online: count, config_lagging: count }) }),
  z.object({ key: z.literal('payment_callbacks'), ...componentBase, metrics: z.object({ pending: count }) }),
  z.object({ key: z.literal('mail'), ...componentBase, metrics: queueMetrics }),
  z.object({ key: z.literal('telegram'), ...componentBase, metrics: queueMetrics }),
  z.object({ key: z.literal('sse'), ...componentBase, metrics: z.object({ connections: count }) }),
  z.object({
    key: z.literal('backup'),
    ...componentBase,
    metrics: z.object({ latest_age_hours: count.nullable(), stale: z.boolean(), identity_configured: z.boolean(), offsite_configured: z.boolean() }),
  }),
])
export const systemStatusSchema = z.object({
  backup: backupSchema,
  database: z.union([z.object({ size_bytes: count, connections: count, max_connections: count }), z.object({ error: z.string() })]),
  // 待补·后端
  state: z.enum(['ok', 'degraded']).optional(),
  components: z.array(componentSchema).optional(),
})
export type BackupFile = z.output<typeof backupFileSchema>
export type BackupStatus = z.output<typeof backupSchema>
export type ComponentState = z.output<typeof componentState>
export type SystemComponent = z.output<typeof componentSchema>
export type SystemStatus = z.output<typeof systemStatusSchema>

// ---------------------------------------------------------------------------
// GET v1/stats/timeseries（现有 + 待补 active_users，security.audit.read）
// ---------------------------------------------------------------------------
const activityPointSchema = z.object({
  day: z.string().regex(/^\d{2}-\d{2}$/),
  registered: count,
  logins: count,
  orders: count,
  unique_ips: count,
  active_users: count.optional(),
})
export const activitySchema = z.object({ points: z.array(activityPointSchema) })
export type ActivityPoint = z.output<typeof activityPointSchema>

// ---------------------------------------------------------------------------
// 查询 hook。enabled 由页面按权限决定：缺权限不发请求（DASH-01：不制造无意义的 404）。
// 查询键不与侧栏的 ['admin','dashboard','tasks'] 共用：侧栏的 schema 只取 count，
// 共用一条缓存会让两边互相覆盖出对方的形状。
// ---------------------------------------------------------------------------
const MINUTE = 60_000

export function useTasks(enabled: boolean) {
  const api = useApi()
  return useQuery({
    queryKey: ['admin', 'dash', 'tasks'],
    queryFn: ({ signal }) => api.get('v1/dashboard/tasks', tasksSchema, { signal }),
    enabled,
    meta: { topics: ['tickets.changed', 'orders.changed', 'nodes.changed'] },
    refetchInterval: MINUTE,
  })
}

export function useBacklog(enabled: boolean) {
  const api = useApi()
  return useQuery({
    queryKey: ['admin', 'dash', 'backlog'],
    queryFn: ({ signal }) => api.get('v1/dashboard/backlog/notifications', backlogSchema, { signal }),
    enabled,
    refetchInterval: MINUTE,
  })
}

export function useOverview(enabled: boolean) {
  const api = useApi()
  return useQuery({
    queryKey: ['admin', 'dash', 'overview'],
    queryFn: ({ signal }) => api.get('v1/overview', overviewSchema, { signal }),
    enabled,
    meta: { topics: ['orders.changed', 'subscriptions.changed', 'nodes.changed'] },
    refetchInterval: MINUTE,
  })
}

export function useRevenue(enabled: boolean, cur: RevenueCurrency, days: RevenueDays) {
  const api = useApi()
  return useQuery({
    queryKey: ['admin', 'dash', 'revenue', cur, days],
    queryFn: ({ signal }) => api.get('v1/revenue/timeseries', revenueSchema, { signal, query: { currency: cur, days } }),
    enabled,
    meta: { topics: ['orders.changed'] },
    // 切币种、切区间时保留上一张图，不闪骨架
    placeholderData: keepPreviousData,
  })
}

/** 仪表盘固定取近 24 小时前 5 名；「流量」KPI 读同一条查询的 totals，只发一次请求 */
const TRAFFIC_QUERY = { range: '24h', limit: 5 } as const

export function useNodeTraffic(enabled: boolean) {
  const api = useApi()
  return useQuery({
    queryKey: ['admin', 'dash', 'traffic', 'nodes'],
    queryFn: ({ signal }) => api.get('v1/dashboard/traffic/nodes', nodeTrafficSchema, { signal, query: TRAFFIC_QUERY }),
    enabled,
    refetchInterval: 5 * MINUTE,
  })
}

/** DASH-01：两张流量卡都可读时，以先成功那张的 snapshot_at 为锚点，两边口径一致 */
export function useUserTraffic(enabled: boolean, snapshotAt: string | undefined) {
  const api = useApi()
  return useQuery({
    queryKey: ['admin', 'dash', 'traffic', 'users', snapshotAt ?? null],
    queryFn: ({ signal }) =>
      api.get('v1/dashboard/traffic/users', userTrafficSchema, { signal, query: { ...TRAFFIC_QUERY, snapshot_at: snapshotAt } }),
    enabled,
    placeholderData: keepPreviousData,
  })
}

export function useSystemStatus(enabled: boolean) {
  const api = useApi()
  return useQuery({
    queryKey: ['admin', 'dash', 'system'],
    queryFn: ({ signal }) => api.get('v1/system/status', systemStatusSchema, { signal }),
    enabled,
    refetchInterval: MINUTE,
  })
}

export function useActivity(enabled: boolean) {
  const api = useApi()
  return useQuery({
    queryKey: ['admin', 'dash', 'activity'],
    queryFn: ({ signal }) => api.get('v1/stats/timeseries', activitySchema, { signal, query: { days: 14 } }),
    enabled,
    refetchInterval: 5 * MINUTE,
  })
}
