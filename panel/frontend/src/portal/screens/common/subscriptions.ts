/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useQuery / useMutation / useQueryClient，依赖 zod，依赖 ../../../shell/runtime 的 useApi，依赖 ./traffic 的额度摘要与重置日计算
 * [OUTPUT]: 对外提供订阅相关 schema 与类型（Subscription、SubscriptionLink、UsageReport…）、LIVE_STATUSES、isLive、pickPrimary、useSubscriptions、useSubscriptionLinks、useSubscriptionNodes、useSubscriptionUsage、useTrafficPacks、useRotateLink、usePlanTraffic / PlanTraffic、liveSubscriptions、canRenew
 * [POS]: portal/screens/common 的订阅数据层（契约门户-01 / 门户-02，流量包余量属门户-03）：概览与我的订阅共用；schema 按契约写全写严，「待补·后端」字段一律可选，页面对缺席做降级
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../../../shell/runtime'
import { pickTrafficQuota, resetAtOf, trafficSummary, type TrafficSummary } from './traffic'

// ---------------------------------------------------------------------------
// GET v1/me/subscriptions
// 外框 queries.ts 用 ['portal','subscriptions'] 读同一接口，但它的 schema 只收
// plan_name / status（zod 会裁掉其余字段），同键共享缓存会把页面要的字段裁没，
// 所以页面用子键 detail；两者按同样的 topic 失效。
// ---------------------------------------------------------------------------
export const SUBSCRIPTION_STATUSES = ['pending', 'trialing', 'active', 'past_due', 'grace', 'paused', 'cancelled', 'expired'] as const
export type SubscriptionStatus = (typeof SUBSCRIPTION_STATUSES)[number]

const quotaSchema = z.object({
  metric: z.string(),
  limit: z.number().int().nullable(),
  consumed: z.number().int(),
  remaining: z.number().int().nullable(),
  // 待补·后端
  period: z.string().optional(),
  period_start: z.string().nullable().optional(),
  period_end: z.string().nullable().optional(),
  granted_addon: z.number().int().optional(),
  adjusted: z.number().int().optional(),
})

const renewalPriceSchema = z.object({
  id: z.string(),
  currency: z.string(),
  unit_amount: z.number().int(),
  billing_interval: z.string(),
  interval_count: z.number().int(),
  available: z.boolean(),
})

export const subscriptionSchema = z.object({
  id: z.string(),
  plan_id: z.string(),
  price_id: z.string(),
  plan_name: z.string(),
  plan_version: z.number().int(),
  status: z.enum(SUBSCRIPTION_STATUSES),
  current_period_start: z.string().nullable(),
  current_period_end: z.string().nullable(),
  currency: z.string(),
  amount: z.number().int(),
  quotas: z.array(quotaSchema),
  // 待补·后端（后端二第 ⑤ 步）
  device_limit: z.number().int().nullable().optional(),
  online_devices: z.number().int().optional(),
  quota_reset_strategy: z.enum(['never', 'natural_month', 'billing_cycle', 'fixed_day']).optional(),
  next_reset_at: z.string().nullable().optional(),
  renewable: z.boolean().optional(),
  renewal_price: renewalPriceSchema.nullable().optional(),
  pack_remaining_bytes: z.number().int().optional(),
})
export type Subscription = z.output<typeof subscriptionSchema>

const subscriptionsSchema = z.object({ subscriptions: z.array(subscriptionSchema) })

export const LIVE_STATUSES: ReadonlySet<SubscriptionStatus> = new Set(['active', 'trialing', 'grace', 'past_due'])
export const isLive = (s: Pick<Subscription, 'status'>) => LIVE_STATUSES.has(s.status)

/** 仍然生效的订阅，按 current_period_end 最晚在前（没有周期末的视为最晚）。 */
export function liveSubscriptions(subs: readonly Subscription[]): Subscription[] {
  const end = (s: Subscription) => (s.current_period_end ? new Date(s.current_period_end).getTime() : Number.POSITIVE_INFINITY)
  return subs.filter(isLive).sort((a, b) => end(b) - end(a))
}

/** 契约：多条订阅时概览展示 status∈{active,trialing,grace,past_due} 中 current_period_end 最晚的一条。 */
export const pickPrimary = (subs: readonly Subscription[]): Subscription | null => liveSubscriptions(subs)[0] ?? null

/** 能否续费：待补字段 renewable 上线前按状态判断（allow_renewal 由下单时的 409 兜底）。 */
export const canRenew = (s: Subscription) => s.renewable ?? isLive(s)

const SUBS_KEY = ['portal', 'subscriptions', 'detail'] as const

export function useSubscriptions() {
  const api = useApi()
  return useQuery({
    queryKey: SUBS_KEY,
    queryFn: ({ signal }) => api.get('v1/me/subscriptions', subscriptionsSchema, { signal }),
    select: (d) => d.subscriptions,
    meta: { topics: ['subscriptions.changed', 'orders.changed'] },
  })
}

// ---------------------------------------------------------------------------
// GET v1/me/subscription-links：只含 active 且未过期的凭据，按 subscription_id 配对
// ---------------------------------------------------------------------------
export const subscriptionLinkSchema = z.object({
  subscription_id: z.string(),
  url: z.string(),
  expires_at: z.string().nullable(),
  fetch_count: z.number().int(),
  last_fetched_at: z.string().nullable(),
  distinct_sources_24h: z.number().int(),
})
export type SubscriptionLink = z.output<typeof subscriptionLinkSchema>

const linksSchema = z.object({ links: z.array(subscriptionLinkSchema) })
const LINKS_KEY = ['portal', 'subscription-links'] as const

export function useSubscriptionLinks() {
  const api = useApi()
  return useQuery({
    queryKey: LINKS_KEY,
    queryFn: ({ signal }) => api.get('v1/me/subscription-links', linksSchema, { signal }),
    select: (d) => d.links,
    // subscription_credentials 的变更走 subscriptions.changed
    meta: { topics: ['subscriptions.changed'] },
  })
}

// ---------------------------------------------------------------------------
// POST v1/me/subscriptions/{id}/rotate：无 body、不幂等（每次调用都再换一次），
// 按钮在请求期间禁用防连点；成功后先用返回的 url 覆盖显示，再重拉链接统计
// ---------------------------------------------------------------------------
const rotateSchema = z.object({ url: z.string() })

export function useRotateLink() {
  const api = useApi()
  const client = useQueryClient()
  return useMutation({
    mutationFn: (subscriptionId: string) => api.post(`v1/me/subscriptions/${encodeURIComponent(subscriptionId)}/rotate`, rotateSchema),
    onSuccess: ({ url }, subscriptionId) => {
      client.setQueryData<z.output<typeof linksSchema>>(LINKS_KEY, (old) => {
        const fresh: SubscriptionLink = { subscription_id: subscriptionId, url, expires_at: null, fetch_count: 0, last_fetched_at: null, distinct_sources_24h: 0 }
        const rest = (old?.links ?? []).filter((l) => l.subscription_id !== subscriptionId)
        return { links: [...rest, fresh] }
      })
      void client.invalidateQueries({ queryKey: LINKS_KEY })
    },
  })
}

// ---------------------------------------------------------------------------
// GET v1/me/subscriptions/{id}/nodes：保留规则 3，只有名称 / 协议 / 倍率；
// 状态不在 {active,trialing,grace} 或无有效凭据时回 404
// ---------------------------------------------------------------------------
export const nodePreviewSchema = z.object({ name: z.string(), protocol: z.string(), traffic_rate: z.number() })
export type NodePreview = z.output<typeof nodePreviewSchema>
const nodesSchema = z.object({ count: z.number().int(), nodes: z.array(nodePreviewSchema) })

export function useSubscriptionNodes(subscriptionId: string | undefined) {
  const api = useApi()
  return useQuery({
    queryKey: ['portal', 'subscriptions', subscriptionId, 'nodes'],
    queryFn: ({ signal }) => api.get(`v1/me/subscriptions/${encodeURIComponent(subscriptionId!)}/nodes`, nodesSchema, { signal }),
    enabled: subscriptionId !== undefined,
    meta: { topics: ['nodes.changed', 'subscriptions.changed'] },
  })
}

// ---------------------------------------------------------------------------
// GET v1/me/subscriptions/{id}/usage（修订 R47 / R48 / R50）：没有推送，靠进页与聚焦时重拉
// ---------------------------------------------------------------------------
export const usageReportSchema = z.object({
  timezone: z.string(),
  period_start: z.string(),
  period_end: z.string().nullable(),
  days: z.array(z.object({ date: z.string(), bytes: z.number().int() })),
  today_bytes: z.number().int(),
  avg_daily_bytes: z.number().int(),
})
export type UsageReport = z.output<typeof usageReportSchema>

export function useSubscriptionUsage(subscriptionId: string | undefined) {
  const api = useApi()
  return useQuery({
    queryKey: ['portal', 'subscriptions', subscriptionId, 'usage'],
    queryFn: ({ signal }) => api.get(`v1/me/subscriptions/${encodeURIComponent(subscriptionId!)}/usage`, usageReportSchema, { signal }),
    enabled: subscriptionId !== undefined,
  })
}

// ---------------------------------------------------------------------------
// GET v1/me/traffic-packs（修订 R30）：流量包挂用户，余量新增推 subscriptions.changed（R33）
// ---------------------------------------------------------------------------
const trafficPacksSchema = z.object({
  remaining_bytes_total: z.number().int(),
  packs: z.array(
    z.object({
      id: z.string(),
      source: z.enum(['order', 'gift_card', 'migration']),
      order_id: z.string().nullable(),
      granted_bytes: z.number().int(),
      consumed_bytes: z.number().int(),
      remaining_bytes: z.number().int(),
      created_at: z.string(),
    }),
  ),
})

export function useTrafficPacks() {
  const api = useApi()
  return useQuery({
    queryKey: ['portal', 'traffic-packs'],
    queryFn: ({ signal }) => api.get('v1/me/traffic-packs', trafficPacksSchema, { signal }),
    meta: { topics: ['subscriptions.changed', 'orders.changed'] },
  })
}

// ---------------------------------------------------------------------------
// 一条订阅的流量摘要与下次重置：主卡、我的订阅、用量图共用；流量包余量优先取
// 订阅上的待补字段 pack_remaining_bytes，缺席时读 GET v1/me/traffic-packs。
// 用量查询与用量图同键，缓存共享、不多发请求。
// ---------------------------------------------------------------------------
export interface PlanTraffic {
  summary: TrafficSummary | null
  resetAt: string | null
  /** 按日用量接口回的切日时区；日期显示统一按它，未拉到时为 undefined（退回本地时区） */
  timeZone: string | undefined
}

export function usePlanTraffic(sub: Subscription): PlanTraffic {
  const packs = useTrafficPacks()
  const usage = useSubscriptionUsage(sub.id)
  const quota = pickTrafficQuota(sub.quotas)
  return {
    summary: trafficSummary(quota, sub.pack_remaining_bytes ?? packs.data?.remaining_bytes_total ?? 0),
    resetAt: resetAtOf(sub, quota, usage.data),
    timeZone: usage.data?.timezone,
  }
}
