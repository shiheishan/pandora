import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../shell/runtime'

export const siteConfigSchema = z.object({
  registration_mode: z.enum(['closed', 'invite_only', 'open']),
  email_verification: z.boolean(),
  /** 找回密码开没开：没配邮件服务时为 false，门户隐藏「忘记密码」 */
  password_reset: z.boolean(),
})

export const appearanceSchema = z.object({
  theme: z
    .object({
      tokens: z
        .object({ light: z.record(z.string(), z.string()).optional(), dark: z.record(z.string(), z.string()).optional() })
        .partial()
        .catch({}),
      branding: z.object({ site_name: z.string().optional(), tagline: z.string().optional(), logo: z.string().optional() }).catch({}),
    })
    .nullable(),
  slots: z.record(z.string(), z.string()).catch({}),
})
export type Appearance = z.output<typeof appearanceSchema>

// GET v1/me 全字段：外框只用用户名与邮箱，账号安全页的「个人信息」读 user_id / created_at，同键一份
export const meSchema = z.object({
  user_id: z.string(),
  email: z.string(),
  display_name: z.string().nullable(),
  status: z.string(),
  created_at: z.string(),
  permissions: z.array(z.string()).nullable(),
})
export type PortalMe = z.output<typeof meSchema>
// 余额与流水（契约门户-05）：外框只读 balance，钱包页读 history，同键一份全字段
export const balanceSchema = z.object({
  balance: z.number().int(),
  currency: z.string(),
  history: z.array(z.object({ kind: z.string(), delta: z.number().int(), memo: z.string(), at: z.string() })),
})
export type Balance = z.output<typeof balanceSchema>
export const notificationsSchema = z.object({ unread: z.number() })

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
// GET v1/me/subscriptions：外框的套餐徽标与概览、我的套餐、选购、确认页共用一个查询键和一份
// 完整 schema（契约门户-02，含修订 R53–R62 的扩展字段与购买模型设计稿 2.9），各处经 select 取自己
// 要的部分，所以缓存里始终是全字段、整个门户只发一次请求。字段与 Go 的 mySubscriptionView / myQuotaView
// 一一对应：都没有 omitempty，指针字段（设备上限、周期末、下次重置、续费价、备注名、续到哪天）为 null 而不缺席。
// ---------------------------------------------------------------------------
export { SUBSCRIPTION_STATUSES, subscriptionSchema, subscriptionsSchema, type Subscription, type SubscriptionStatus } from './subscription-schema'
import { subscriptionsSchema, type Subscription, type SubscriptionStatus } from './subscription-schema'

export const LIVE_STATUSES: ReadonlySet<SubscriptionStatus> = new Set(['active', 'trialing', 'grace', 'past_due'])
export const isLive = (s: Pick<Subscription, 'status'>) => LIVE_STATUSES.has(s.status)

/** 仍然生效的订阅，按 current_period_end 最晚在前（没有周期末的视为最晚）。 */
export function liveSubscriptions(subs: readonly Subscription[]): Subscription[] {
  const end = (s: Subscription) => (s.current_period_end ? new Date(s.current_period_end).getTime() : Number.POSITIVE_INFINITY)
  return subs.filter(isLive).sort((a, b) => end(b) - end(a))
}

/** 已过期、但还能在原订阅上续费的（过期不满 30 天，后端 renewable 为真）：续费后原链接自动恢复（w5expiry）。 */
export const isRenewableExpired = (s: Pick<Subscription, 'status' | 'renewable'>) => s.status === 'expired' && s.renewable

/** 门户当成「当前订阅」的：生效中的，或能原地续费的已过期订阅。 */
export const isCurrent = (s: Pick<Subscription, 'status' | 'renewable'>) => isLive(s) || isRenewableExpired(s)

/** 能原地续费的已过期订阅，到期最晚的在前。 */
export function renewableExpired(subs: readonly Subscription[]): Subscription[] {
  const end = (s: Subscription) => (s.current_period_end ? new Date(s.current_period_end).getTime() : 0)
  return subs.filter(isRenewableExpired).sort((a, b) => end(b) - end(a))
}

/**
 * 契约门户-01：多条订阅时展示生效订阅中 current_period_end 最晚的一条；外框徽标、概览、选购页都按它。
 * 没有生效订阅时回落到最近一条能原地续费的已过期订阅：显示「已过期」与续费，买同套餐走续费、
 * 别的套餐走改套餐，链接不变（w5expiry）。
 */
export const pickPrimary = (subs: readonly Subscription[]): Subscription | null => liveSubscriptions(subs)[0] ?? renewableExpired(subs)[0] ?? null

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

/** 顶层的未分配流量包余量：与订阅列表同键同一次请求 */
export function useUnattachedPackBytes() {
  const api = useApi()
  return useQuery({
    queryKey: SUBSCRIPTIONS_KEY,
    queryFn: ({ signal }) => api.get('v1/me/subscriptions', subscriptionsSchema, { signal }),
    select: (d) => d.unattached_pack_bytes,
    meta: { topics: ['subscriptions.changed', 'orders.changed'] },
  })
}

const primaryPlanName = (subs: Subscription[]) => pickPrimary(subs)?.plan_name ?? null

/** 头像菜单的套餐徽标：生效中到期最晚的那份（pickPrimary 只留给外框徽标用）。 */
export function useActivePlanName(): string | null {
  return useSubscriptions(primaryPlanName).data ?? null
}

// ---------------------------------------------------------------------------
// GET v1/me/commission（契约门户-06，含修订 R69）：外框头像菜单读可用佣金，邀请返利页读全部，
// 同键一份全字段。可用额按 5.A D-F-1 已统一为「账本余额 − 在途提现」，转余额与提现同一口径。
// R69 的 paid_invitees / total_earned / transfers 已上线且必回（Go 无 omitempty）。
// 三个列表 Go 端都以空切片初始化，没有记录时是 []，不是 null。
// ---------------------------------------------------------------------------
// CHECK 允许六种；Go 只写 pending / available，冲销由 SQL 写 reversed
export const COMMISSION_SCOPES = ['every_order', 'first_order'] as const
export const COMMISSION_ENTRY_STATUSES = ['pending', 'available', 'reversed', 'frozen', 'settled', 'rejected'] as const
export const WITHDRAWAL_STATUSES = ['requested', 'reviewing', 'approved', 'rejected', 'processing', 'paid', 'failed', 'returned'] as const

export const commissionSchema = z.object({
  summary: z.object({
    currency: z.string(),
    pending: z.number().int(),
    available: z.number().int(),
    withdrawing: z.number().int(),
    settled: z.number().int(),
    invitees: z.number().int(),
    orders: z.number().int(),
    paid_invitees: z.number().int(),
    total_earned: z.number().int(),
    rate_percent: z.number().int(),
    min_withdraw: z.number().int(),
    // R81 / R114：计佣范围，与计提同一个兜底；first_order 时横幅写「首单」
    scope: z.enum(COMMISSION_SCOPES),
  }),
  entries: z.array(
    z.object({
      amount: z.number().int(),
      base: z.number().int(),
      rate_percent: z.number().int(),
      currency: z.string(),
      status: z.enum(COMMISSION_ENTRY_STATUSES),
      frozen_until: z.string().nullable(),
      created_at: z.string(),
      order_no: z.string(),
      from: z.string(),
    }),
  ),
  withdrawals: z.array(
    z.object({
      id: z.string(),
      amount: z.number().int(),
      currency: z.string(),
      status: z.enum(WITHDRAWAL_STATUSES),
      reject_reason: z.string(),
      requested_at: z.string(),
      completed_at: z.string().nullable(),
    }),
  ),
  transfers: z.array(z.object({ ledger_txn_id: z.string(), amount: z.number().int(), currency: z.string(), created_at: z.string() })),
})
export type Commission = z.output<typeof commissionSchema>

export const COMMISSION_KEY = ['portal', 'commission'] as const

export function useCommission<T = Commission>(select?: (d: Commission) => T) {
  const api = useApi()
  return useQuery({
    queryKey: COMMISSION_KEY,
    queryFn: ({ signal }) => api.get('v1/me/commission', commissionSchema, { signal }),
    select: (d) => (select ? select(d) : (d as T)),
  })
}

const commissionSummary = (d: Commission) => d.summary

/** 头像菜单的可用佣金提示。 */
export function useCommissionAvailable() {
  return useCommission(commissionSummary)
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
