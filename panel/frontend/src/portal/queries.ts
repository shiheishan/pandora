/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useQuery，依赖 zod，依赖 ../shell/runtime 的 useApi
 * [OUTPUT]: 对外提供 siteConfigSchema、appearanceSchema 与 useSiteConfig、useAppearance、usePortalMe、balanceSchema / Balance / useBalance、订阅 schema 与类型（subscriptionSchema、Subscription、SUBSCRIPTION_STATUSES）、LIVE_STATUSES / isLive / liveSubscriptions / pickPrimary、SUBSCRIPTIONS_KEY、useSubscriptions、useActivePlanName、useCommissionAvailable、useUnreadCount、displayName
 * [POS]: portal 外框用到的读接口（契约「门户外壳与认证」与各页的外壳映射）：顶栏余额、头像菜单的用户名 / 套餐 / 佣金、铃铛未读数、登录页的站点开关与插槽；订阅列表是页面与外框共用的全字段查询（外框经 select 取套餐名），其余 schema 只收外框用到的字段，页面要全字段时在这里扩展、不另起查询键
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../shell/runtime'

export const siteConfigSchema = z.object({
  registration_mode: z.enum(['closed', 'invite_only', 'open']),
  email_verification: z.boolean(),
})

export const appearanceSchema = z.object({
  theme: z
    .object({
      tokens: z
        .object({ light: z.record(z.string(), z.string()).optional(), dark: z.record(z.string(), z.string()).optional() })
        .partial()
        .catch({}),
      branding: z.object({ site_name: z.string().optional() }).catch({}),
    })
    .nullable(),
  slots: z.record(z.string(), z.string()).catch({}),
})
export type Appearance = z.output<typeof appearanceSchema>

const meSchema = z.object({ user_id: z.string(), email: z.string(), display_name: z.string().nullable() })
// 余额与流水（契约门户-05）：外框只读 balance，钱包页读 history，同键一份全字段
export const balanceSchema = z.object({
  balance: z.number().int(),
  currency: z.string(),
  history: z.array(z.object({ kind: z.string(), delta: z.number().int(), memo: z.string(), at: z.string() })),
})
export type Balance = z.output<typeof balanceSchema>
const commissionSchema = z.object({ summary: z.object({ available: z.number(), currency: z.string() }) })
const notificationsSchema = z.object({ unread: z.number() })

// 匿名接口：登录页也要用
export function useSiteConfig() {
  const api = useApi()
  return useQuery({
    queryKey: ['portal', 'site-config'],
    queryFn: ({ signal }) => api.get('v1/site-config', siteConfigSchema, { auth: false, signal }),
    staleTime: 5 * 60_000,
  })
}

export function useAppearance() {
  const api = useApi()
  return useQuery({
    queryKey: ['portal', 'appearance'],
    queryFn: ({ signal }) => api.get('v1/appearance', appearanceSchema, { auth: false, signal }),
    staleTime: 5 * 60_000,
  })
}

export function usePortalMe() {
  const api = useApi()
  return useQuery({ queryKey: ['portal', 'me'], queryFn: ({ signal }) => api.get('v1/me', meSchema, { signal }), staleTime: 60_000 })
}

/** 契约映射：userName = display_name ?? email 本地部分。 */
export function displayName(me: { email: string; display_name: string | null } | undefined): string {
  return me ? me.display_name || me.email.split('@')[0] || me.email : ''
}

export function useBalance() {
  const api = useApi()
  return useQuery({
    queryKey: ['portal', 'balance'],
    queryFn: ({ signal }) => api.get('v1/me/balance', balanceSchema, { signal }),
    // 充值、下单抵扣都走订单；礼品卡、佣金转余额没有推送，由各自操作完成后主动失效
    meta: { topics: ['orders.changed'] },
  })
}

// ---------------------------------------------------------------------------
// GET v1/me/subscriptions：外框的套餐徽标与概览、我的订阅、结账页共用一个查询键和一份
// 完整 schema（契约门户-02，含修订 R53–R62 的扩展字段），各处经 select 取自己要的部分，
// 所以缓存里始终是全字段、整个门户只发一次请求。扩展字段写成可选：旧后端（假后端 legacy
// 场景）缺席时页面降级，不因 schema 失败而整页报错。
// ---------------------------------------------------------------------------
export const SUBSCRIPTION_STATUSES = ['pending', 'trialing', 'active', 'past_due', 'grace', 'paused', 'cancelled', 'expired'] as const
export type SubscriptionStatus = (typeof SUBSCRIPTION_STATUSES)[number]

const quotaSchema = z.object({
  metric: z.string(),
  limit: z.number().int().nullable(),
  consumed: z.number().int(),
  remaining: z.number().int().nullable(),
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

/** 契约门户-01：多条订阅时展示生效订阅中 current_period_end 最晚的一条；外框徽标、概览、选购页都按它。 */
export const pickPrimary = (subs: readonly Subscription[]): Subscription | null => liveSubscriptions(subs)[0] ?? null

export const SUBSCRIPTIONS_KEY = ['portal', 'subscriptions'] as const

export function useSubscriptions<T = Subscription[]>(select?: (subs: Subscription[]) => T) {
  const api = useApi()
  return useQuery({
    queryKey: SUBSCRIPTIONS_KEY,
    queryFn: ({ signal }) => api.get('v1/me/subscriptions', subscriptionsSchema, { signal }),
    select: (d) => (select ? select(d.subscriptions) : (d.subscriptions as T)),
    meta: { topics: ['subscriptions.changed', 'orders.changed'] },
  })
}

const primaryPlanName = (subs: Subscription[]) => pickPrimary(subs)?.plan_name ?? null

/** 头像菜单的套餐徽标：与概览主卡同一条订阅。 */
export function useActivePlanName(): string | null {
  return useSubscriptions(primaryPlanName).data ?? null
}

export function useCommissionAvailable() {
  const api = useApi()
  return useQuery({
    queryKey: ['portal', 'commission'],
    queryFn: ({ signal }) => api.get('v1/me/commission', commissionSchema, { signal }),
    select: (d) => d.summary,
  })
}

/** 铃铛角标：新通知没有 SSE，按契约 60 秒与窗口聚焦时用 limit=1 刷新 unread。 */
export function useUnreadCount(): number {
  const api = useApi()
  const { data } = useQuery({
    queryKey: ['portal', 'notifications', 'unread'],
    queryFn: ({ signal }) => api.get('v1/me/notifications', notificationsSchema, { query: { limit: 1 }, signal }),
    refetchInterval: 60_000,
    select: (d) => d.unread,
  })
  return data ?? 0
}
