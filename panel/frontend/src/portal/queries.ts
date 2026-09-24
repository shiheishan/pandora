/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useQuery，依赖 zod，依赖 ../shell/runtime 的 useApi
 * [OUTPUT]: 对外提供 siteConfigSchema、appearanceSchema 与 useSiteConfig、useAppearance、usePortalMe、useBalance、useActivePlanName、useCommissionAvailable、useUnreadCount、displayName
 * [POS]: portal 外框用到的读接口（契约「门户外壳与认证」与各页的外壳映射）：顶栏余额、头像菜单的用户名 / 套餐 / 佣金、铃铛未读数、登录页的站点开关与插槽；schema 只收外框用到的字段，页面在第 3 阶段按需扩展
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
const balanceSchema = z.object({ balance: z.number(), currency: z.string() })
const subscriptionsSchema = z.object({
  subscriptions: z
    .array(z.object({ plan_name: z.string(), status: z.string() }))
    .nullable()
    .transform((s) => s ?? []),
})
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

const LIVE_STATUSES = new Set(['active', 'trialing', 'grace', 'past_due'])

/** 头像菜单的套餐徽标：第一条仍有效的订阅（列表按创建时间倒序）。 */
export function useActivePlanName(): string | null {
  const api = useApi()
  const { data } = useQuery({
    queryKey: ['portal', 'subscriptions'],
    queryFn: ({ signal }) => api.get('v1/me/subscriptions', subscriptionsSchema, { signal }),
    meta: { topics: ['subscriptions.changed', 'orders.changed'] },
  })
  return data?.subscriptions.find((s) => LIVE_STATUSES.has(s.status))?.plan_name ?? null
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
