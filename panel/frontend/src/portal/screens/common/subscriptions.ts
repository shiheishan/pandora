import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../../../shell/runtime'
import type { Subscription } from '../../queries'
import { pickTrafficQuota, resetAtOf, trafficSummary, type TrafficSummary } from './traffic'

// ---------------------------------------------------------------------------
// GET v1/me/subscriptions 的 schema 与查询在外框 queries.ts（外框徽标与页面同键共用），
// 这里转出，页面照旧从 common 取
// ---------------------------------------------------------------------------
export { isCurrent, isLive, isRenewableExpired, LIVE_STATUSES, liveSubscriptions, pickPrimary, renewableExpired, subscriptionSchema, useSubscriptions, type Subscription } from '../../queries'

/** 能否续费：取服务端的 renewable（生效状态且套餐允许续费，与续费下单同一口径）。 */
export const canRenew = (s: Subscription) => s.renewable

// ---------------------------------------------------------------------------
// GET v1/me/subscription-links：active 凭据，含过期 30 天内订阅的（expired 为真、只读），按 subscription_id 配对
// ---------------------------------------------------------------------------
export const subscriptionLinkSchema = z.object({
  subscription_id: z.string(),
  url: z.string(),
  expires_at: z.string().nullable(),
  fetch_count: z.number().int(),
  last_fetched_at: z.string().nullable(),
  distinct_sources_24h: z.number().int(),
  // 订阅已过期、链接暂停（只读展示；续费后原链接自动恢复，过期期间不能更换）
  expired: z.boolean(),
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
        const fresh: SubscriptionLink = { subscription_id: subscriptionId, url, expires_at: null, fetch_count: 0, last_fetched_at: null, distinct_sources_24h: 0, expired: false }
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

/**
 * 节点变更不再推到门户（realtime.channelsFor 只推管理端）：原先每个节点每次心跳都给全站每条门户连接
 * 推一条，订阅页因此每 2 秒重拉。节点的名称、协议、倍率很少变，改成 60 秒定时重拉（页面不可见时暂停）；
 * 订阅本身变了（续费、换套餐）仍随 subscriptions.changed 立刻刷新
 */
export const SUBSCRIPTION_NODES_REFRESH_MS = 60_000

export function useSubscriptionNodes(subscriptionId: string | undefined) {
  const api = useApi()
  return useQuery({
    queryKey: ['portal', 'subscriptions', subscriptionId, 'nodes'],
    queryFn: ({ signal }) => api.get(`v1/me/subscriptions/${encodeURIComponent(subscriptionId!)}/nodes`, nodesSchema, { signal }),
    enabled: subscriptionId !== undefined,
    meta: { topics: ['subscriptions.changed'] },
    refetchInterval: SUBSCRIPTION_NODES_REFRESH_MS,
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
// 一条订阅的流量摘要与下次重置：主卡、我的订阅、用量图共用；流量包余量取订阅上的
// pack_remaining_bytes（挂在用户上，几条订阅同一个数）。用量查询与用量图同键，缓存共享、不多发请求。
// ---------------------------------------------------------------------------
export interface PlanTraffic {
  summary: TrafficSummary | null
  resetAt: string | null
  /** 按日用量接口回的切日时区；日期显示统一按它，未拉到时为 undefined（退回本地时区） */
  timeZone: string | undefined
}

export function usePlanTraffic(sub: Subscription): PlanTraffic {
  const usage = useSubscriptionUsage(sub.id)
  const quota = pickTrafficQuota(sub.quotas)
  return {
    summary: trafficSummary(quota, sub.pack_remaining_bytes),
    resetAt: resetAtOf(sub, quota, usage.data),
    timeZone: usage.data?.timezone,
  }
}
