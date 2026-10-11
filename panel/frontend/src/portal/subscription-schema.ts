import { z } from 'zod'

// GET v1/me/subscriptions 的 schema（纯 zod，不依赖运行时）：外框 queries.ts 转出，tests/ 的假后端测试也直接用它
export const SUBSCRIPTION_STATUSES = ['pending', 'trialing', 'active', 'past_due', 'grace', 'paused', 'cancelled', 'expired'] as const
export type SubscriptionStatus = (typeof SUBSCRIPTION_STATUSES)[number]

const quotaSchema = z.object({
  metric: z.string(),
  limit: z.number().int().nullable(),
  consumed: z.number().int(),
  remaining: z.number().int().nullable(),
  period: z.string(),
  period_start: z.string(),
  period_end: z.string().nullable(),
  granted_addon: z.number().int(),
  adjusted: z.number().int(),
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
  device_limit: z.number().int().nullable(),
  online_devices: z.number().int(),
  quota_reset_strategy: z.enum(['never', 'natural_month', 'billing_cycle', 'fixed_day']),
  next_reset_at: z.string().nullable(),
  renewable: z.boolean(),
  renewal_price: renewalPriceSchema.nullable(),
  /** 设计稿 2.9：挂在这一份上的流量包余量（不再是用户的总数） */
  pack_remaining_bytes: z.number().int(),
  /** 用户起的备注名（subscriptions.label），没起为 null */
  label: z.string().nullable(),
  /** App 里的配置名：「站点名 · 备注名」，没有备注名用套餐名（subscription.ProfileName，与下载头同源） */
  client_name: z.string(),
  /** 能付费换套餐（billing.subscriptionAcceptsPaidChange）：与 renewable 同口径，但不看 allow_renewal */
  changeable: z.boolean(),
  /** 按续费价续一期会到哪天：生效中的从当前到期日起算，过期的从现在起算；不能续时为 null */
  renew_until: z.string().nullable(),
})
export type Subscription = z.output<typeof subscriptionSchema>

/** unattached_pack_bytes：还没加到任何一份的流量包余量（没有套餐时兑换的送流量） */
export const subscriptionsSchema = z.object({ subscriptions: z.array(subscriptionSchema), unattached_pack_bytes: z.number().int() })
