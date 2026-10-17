import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../shell/runtime'

export const siteConfigSchema = z.object({
  registration_mode: z.enum(['closed', 'invite_only', 'open']),
  email_verification: z.boolean(),
  /** 找回密码开没开：没配邮件服务时为 false，门户隐藏「忘记密码」 */
  password_reset: z.boolean(),
})

export const signupConfigSchema = z.object({
  captcha: z
    .object({
      provider: z.enum(['recaptcha_v2', 'recaptcha_v3', 'turnstile']),
      site_key: z.string().min(1),
    })
    .nullable(),
  email_suffixes: z.array(z.string()),
  mail_link_login: z.boolean(),
})
export type SignupConfig = z.output<typeof signupConfigSchema>

export const appearanceSchema = z.object({
  site_name: z.string().catch('Pandora'),
  theme: z
    .object({
      tokens: z
        .object({ light: z.record(z.string(), z.string()).optional(), dark: z.record(z.string(), z.string()).optional() })
        .partial()
        .catch({}),
      branding: z.object({ tagline: z.string().optional(), logo: z.string().optional() }).catch({}),
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
})
// 余额与流水（契约门户-05）：外框只读 balance，钱包页读 history，同键一份全字段
export const balanceSchema = z.object({
  balance: z.number().int(),
  history: z.array(z.object({ kind: z.string(), delta: z.number().int(), memo: z.string(), at: z.string() })),
})
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

export function useSignupConfig() {
  const api = useApi()
  return useQuery({
    queryKey: ['portal', 'signup-config'],
    queryFn: ({ signal }) => api.get('v1/signup-config', signupConfigSchema, { auth: false, signal }),
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
export { subscriptionSchema, subscriptionsSchema, type Subscription } from './subscription-schema'
import { subscriptionsSchema, type Subscription } from './subscription-schema'

export const isLive = (s: Pick<Subscription, 'status'>) => s.status === 'active'

/** 仍然生效的订阅，按 current_period_end 最晚在前（没有周期末的视为最晚）。 */
function liveSubscriptions(subs: readonly Subscription[]): Subscription[] {
  const end = (s: Subscription) => (s.current_period_end ? new Date(s.current_period_end).getTime() : Number.POSITIVE_INFINITY)
  return subs.filter(isLive).sort((a, b) => end(b) - end(a))
}

/** 已过期、但还能在原订阅上续费的（过期不满 30 天，后端 renewable 为真）：续费后原链接自动恢复。 */
const isRenewableExpired = (s: Pick<Subscription, 'status' | 'renewable'>) => s.status === 'expired' && s.renewable

/** 能原地续费的已过期订阅，到期最晚的在前。 */
function renewableExpired(subs: readonly Subscription[]): Subscription[] {
  const end = (s: Subscription) => (s.current_period_end ? new Date(s.current_period_end).getTime() : 0)
  return subs.filter(isRenewableExpired).sort((a, b) => end(b) - end(a))
}

/**
 * 契约门户-01：多条订阅时展示生效订阅中 current_period_end 最晚的一条；外框徽标、概览、选购页都按它。
 * 没有生效订阅时回落到最近一条能原地续费的已过期订阅：显示「已过期」与续费，买同套餐走续费、
 * 别的套餐走改套餐，链接不变。
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

const primaryPlanName = (subs: Subscription[]) => pickPrimary(subs)?.plan_name ?? null

/** 侧栏账户菜单与「我的」页的套餐 chip：生效中到期最晚的那份（pickPrimary 只留给外框徽标用）。 */
export function useActivePlanName(): string | null {
  return useSubscriptions(primaryPlanName).data ?? null
}

// ---------------------------------------------------------------------------
// GET v1/me/commission（契约门户-06，含修订 R69）：外框头像菜单读可用佣金，邀请返利页读全部，
// 同键一份全字段。可用额按 5.A D-F-1 已统一为「账本余额 − 在途提现」，转余额与提现同一口径。
// R69 的 paid_invitees / total_earned / transfers 已上线且必回（Go 无 omitempty）。
// 三个列表 Go 端都以空切片初始化，没有记录时是 []，不是 null。
// ---------------------------------------------------------------------------
// 佣金条目状态：pending / available / voided（复核作废，门户显示「已作废」）；其余状态留在枚举里防解析失败
const COMMISSION_SCOPES = ['every_order', 'first_order'] as const
const COMMISSION_ENTRY_STATUSES = ['pending', 'available', 'reversed', 'frozen', 'settled', 'rejected', 'voided'] as const
const WITHDRAWAL_STATUSES = ['requested', 'reviewing', 'approved', 'rejected', 'processing', 'paid', 'failed', 'returned'] as const

export const commissionSchema = z.object({
  summary: z.object({
    pending: z.number().int(),
    available: z.number().int(),
    withdrawing: z.number().int(),
    settled: z.number().int(),
    invitees: z.number().int(),
    orders: z.number().int(),
    paid_invitees: z.number().int(),
    total_earned: z.number().int(),
    rate_percent: z.number().int(),
    direct_rate_bp: z.number().int(),
    min_withdraw: z.number().int(),
    withdraw_enabled: z.boolean(),
    withdraw_methods: z.array(z.string()),
    // R81 / R114：计佣范围，与计提同一个兜底；first_order 时横幅写「首单」
    scope: z.enum(COMMISSION_SCOPES),
  }),
  entries: z.array(
    z.object({
      amount: z.number().int(),
      base: z.number().int(),
      rate_percent: z.number().int(),
      rate_bp: z.number().int(),
      level: z.number().int(),
      share_percent: z.number().int(),
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
      method: z.string(),
      status: z.enum(WITHDRAWAL_STATUSES),
      reject_reason: z.string(),
      requested_at: z.string(),
      completed_at: z.string().nullable(),
    }),
  ),
  transfers: z.array(z.object({ ledger_txn_id: z.string(), amount: z.number().int(), created_at: z.string() })),
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

/** 「我的」页邀请横幅的可用佣金。 */
export function useCommissionAvailable() {
  return useCommission(commissionSummary)
}

// ---------------------------------------------------------------------------
// GET v1/orders?status=pending_payment：侧栏「订单」计数与我的套餐页的待支付横条共用。
// 外框与页面共用的键，schema 全字段；screens/common/orders.ts 里还有一份同样的定义，
// 之后应改成从这里 import（同一键只留一份 schema）。
// ---------------------------------------------------------------------------
const ORDER_KINDS = ['new', 'renewal', 'topup', 'upgrade', 'stack', 'traffic_reset'] as const
export const ORDER_STATUSES = ['pending_payment', 'paid', 'fulfilled', 'cancelled', 'expired'] as const

export const orderRowSchema = z.object({
  id: z.string(),
  order_no: z.string(),
  kind: z.enum(ORDER_KINDS),
  status: z.enum(ORDER_STATUSES),
  total_amount: z.number().int(),
  discount_amount: z.number().int(),
  balance_applied: z.number().int(),
  payable_amount: z.number().int(),
  paid_amount: z.number().int(),
  plan_name: z.string().optional(),
  cancellable: z.boolean(),
  // 发起过支付（有过任何一条支付意图）；Go 无 omitempty，恒在。决定显示「我已支付，刷新状态」
  has_payment_intent: z.boolean(),
  created_at: z.string(),
  paid_at: z.string().optional(),
  cancelled_at: z.string().optional(),
  cancel_reason: z.string().optional(),
  expires_at: z.string().optional(),
  // 修订 R69：周期是首个订单项的快照，Go 带 omitempty，没有周期（充值）时缺席；
  // 商品名 Go 无 omitempty，恒在（没有订单项时为空串）
  interval: z.string().optional(),
  interval_count: z.number().int().optional(),
  item_name: z.string(),
})
export type OrderRow = z.output<typeof orderRowSchema>

export const ordersPageSchema = z.object({
  orders: z.array(orderRowSchema),
  total: z.number().int(),
  // 修订 R69：本人全部订单按筛选段的计数，不受当前筛选影响
  counts: z.object({ open: z.number().int(), paid: z.number().int(), closed: z.number().int() }),
})
export type OrdersPage = z.output<typeof ordersPageSchema>

export const PENDING_ORDERS_KEY = ['portal', 'orders', { status: 'pending_payment' }] as const

const pendingCount = (d: OrdersPage) => d.counts.open

/** 侧栏「订单」徽标与手机「我的」页：待支付几笔；没拿到时为 null（不显示） */
export function usePendingOrderCount(): number | null {
  const api = useApi()
  const { data } = useQuery({
    queryKey: PENDING_ORDERS_KEY,
    queryFn: ({ signal }) => api.get('v1/orders', ordersPageSchema, { query: { status: 'pending_payment' }, signal }),
    select: pendingCount,
    meta: { topics: ['orders.changed'] },
  })
  return data ?? null
}

/** 侧栏「消息」计数与「我的」页：新通知没有 SSE，按契约 60 秒与窗口聚焦时用 limit=1 刷新 unread。 */
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
