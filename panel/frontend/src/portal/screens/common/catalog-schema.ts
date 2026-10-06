import { z } from 'zod'

// ---------------------------------------------------------------------------
// GET v1/plans（字段与 Go 的 planView 一一对应，都没有 omitempty：R69 的重置策略与续费 / 变更开关、
// R99 / R100 的限速、卖点与推荐都必回；重置日与限速在不适用时为 null）
// ---------------------------------------------------------------------------
export const RESET_STRATEGIES = ['never', 'natural_month', 'billing_cycle', 'fixed_day'] as const

export const priceSchema = z.object({
  id: z.string(),
  currency: z.string(),
  unit_amount: z.number().int(),
  billing_interval: z.enum(['day', 'week', 'month', 'quarter', 'year', 'one_time']),
  interval_count: z.number().int(),
  trial_days: z.number().int(),
})
export type Price = z.output<typeof priceSchema>

export const planSchema = z.object({
  id: z.string(),
  code: z.string(),
  name: z.string(),
  description: z.string().nullable(),
  version: z.number().int().nullable(),
  max_devices: z.number().int().nullable(),
  quotas: z.array(z.object({ metric: z.string(), limit: z.number().int().nullable(), unit: z.string(), period: z.enum(['total', 'cycle', 'day', 'month']) })),
  prices: z.array(priceSchema),
  quota_reset_strategy: z.enum(RESET_STRATEGIES),
  quota_reset_day: z.number().int().nullable(),
  allow_renewal: z.boolean(),
  allow_upgrade: z.boolean(),
  // 修订 R99：当前发布版本的限速（kbps），null = 不限；R100：卖点（按顺序）与「推荐」
  throttle_kbps: z.number().int().positive().nullable(),
  highlights: z.array(z.string()),
  recommended: z.boolean(),
})
export type Plan = z.output<typeof planSchema>
