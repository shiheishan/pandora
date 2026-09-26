/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useQuery / useMutation / useQueryClient，依赖 zod，依赖 ../../../shell/runtime 的 useApi，依赖 ../../queries 的订阅 schema 与共用查询，依赖 ./traffic 的额度摘要与重置日计算
 * [OUTPUT]: 对外提供（转出外框的）Subscription、subscriptionSchema、LIVE_STATUSES、isLive、pickPrimary、useSubscriptions，自有的 SubscriptionLink、UsageReport 等 schema 与 useSubscriptionLinks、useSubscriptionNodes、useSubscriptionUsage、useTrafficPacks、useRotateLink、usePlanTraffic / PlanTraffic、liveSubscriptions、canRenew；linksSchema / nodesSchema / trafficPacksSchema 为 tests/smoke 形状冒烟导出
 * [POS]: portal/screens/common 的订阅数据层（契约门户-01 / 门户-02，流量包余量属门户-03）：概览与我的订阅共用；订阅列表的 schema 与查询在外框 queries.ts（同键共用），这里转出；其余 schema 按契约写全写严，扩展字段（修订 R53–R62 已上线）写成可选，旧后端缺席时页面降级
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../../../shell/runtime'
import { isLive, type Subscription } from '../../queries'
import { pickTrafficQuota, resetAtOf, trafficSummary, type TrafficSummary } from './traffic'

// ---------------------------------------------------------------------------
// GET v1/me/subscriptions 的 schema 与查询在外框 queries.ts（外框徽标与页面同键共用），
// 这里转出，页面照旧从 common 取
// ---------------------------------------------------------------------------
export { isLive, LIVE_STATUSES, liveSubscriptions, pickPrimary, subscriptionSchema, useSubscriptions, type Subscription } from '../../queries'

/** 能否续费：renewable 优先；旧后端缺这个字段时按状态判断（allow_renewal 由下单时的 409 兜底）。 */
export const canRenew = (s: Subscription) => s.renewable ?? isLive(s)

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

export const linksSchema = z.object({ links: z.array(subscriptionLinkSchema) })
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
export const nodesSchema = z.object({ count: z.number().int(), nodes: z.array(nodePreviewSchema) })

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
export const trafficPacksSchema = z.object({
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
