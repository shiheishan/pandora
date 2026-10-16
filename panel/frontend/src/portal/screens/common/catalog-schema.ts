import { z } from 'zod'

// ---------------------------------------------------------------------------
// GET v1/plans（Go catalog/handler 的 portalPlan，都没有 omitempty）：
// 订阅套餐（kind=subscription）按时间算，月 / 季 / 半年 / 年四档价，traffic_bytes 是每期的流量；
// 一次性套餐（kind=one_time）只有一档 one_time 价，traffic_bytes 是总流量，validity_days 为 null 即永久。
// reset 为 null 表示没开流量重置；limit 为 null 表示不限次数。没有币种，只有人民币。
// ---------------------------------------------------------------------------
export const PLAN_KINDS = ['subscription', 'one_time'] as const
export type PlanKind = (typeof PLAN_KINDS)[number]

const priceSchema = z.object({
  id: z.string(),
  billing_interval: z.enum(['month', 'year', 'one_time']),
  interval_count: z.number().int(),
  unit_amount: z.number().int(),
  validity_days: z.number().int().nullable(),
})
export type Price = z.output<typeof priceSchema>

export const planSchema = z.object({
  id: z.string(),
  name: z.string(),
  description: z.string().nullable(),
  kind: z.enum(PLAN_KINDS),
  recommended: z.boolean(),
  /** 假 = 不接新购（另买一份、一次性叠加）；已持有的按 allow_renewal 续。选购页与「再买一份」不列 */
  allow_new_purchase: z.boolean(),
  allow_renewal: z.boolean(),
  /** 订阅套餐是每期流量，一次性套餐是总流量；null = 不限 */
  traffic_bytes: z.number().int().nullable(),
  max_devices: z.number().int().nullable(),
  /** 全程限速（kbps），null = 不限 */
  throttle_kbps: z.number().int().positive().nullable(),
  prices: z.array(priceSchema),
  reset: z.object({ price: z.number().int(), limit: z.number().int().nullable() }).nullable(),
})
export type Plan = z.output<typeof planSchema>
