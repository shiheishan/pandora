import { z } from 'zod'

// ---------------------------------------------------------------------------
// 落点（购买模型设计稿 2.5，Go purchase.Placement / giftcard.CardPlacement）：这张卡在你名下怎么用。
// 选项字段 Go 都带 omitempty，零值缺席；placement 本身无 omitempty，纯余额卡为 null。
// default_key 为空串表示不预选（不同款套餐卡一律不预选），按钮置灰「先选一种用法」。
// ---------------------------------------------------------------------------
export const PLACEMENT_KINDS = ['renew', 'change', 'new', 'extend_days', 'reset_traffic', 'add_traffic'] as const
export type PlacementKind = (typeof PLACEMENT_KINDS)[number]

export const placementOptionSchema = z.object({
  key: z.string(),
  kind: z.enum(PLACEMENT_KINDS),
  subscription_id: z.string().optional(),
  /** change 用在过期 30 天内的那份上：「恢复并改成 X」 */
  expired: z.boolean().optional(),
  badge: z.string().optional(),
  label: z.string().optional(),
  plan_id: z.string().optional(),
  plan_name: z.string().optional(),
  state: z.enum(['live', 'revivable', 'dead']).optional(),
  period_end: z.string().optional(),
  new_period_end: z.string().optional(),
  /** change：原套餐没用完的部分，套餐卡全额退到余额 */
  credit: z.number().int().optional(),
  currency: z.string().optional(),
  /** change：这一份到期前来自赠送的剩余整天数，换掉时不保留（用户 10-08） */
  gift_days_lost: z.number().int().optional(),
  traffic_used: z.number().int().optional(),
  traffic_cap: z.number().int().optional(),
  pack_remaining: z.number().int().optional(),
})
export type PlacementOption = z.output<typeof placementOptionSchema>

export const placementSchema = z.object({ question: z.string(), options: z.array(placementOptionSchema), default_key: z.string() })
export type Placement = z.output<typeof placementSchema>

// ---------------------------------------------------------------------------
// POST v1/gift-cards/preview（修订 R68：不回发行量，套餐卡补套餐名与周期；设计稿 2.5 加 placement）
// ---------------------------------------------------------------------------
export const giftCardSchema = z.object({
  id: z.string(),
  name: z.string(),
  description: z.string(),
  type: z.enum(['general', 'plan', 'mystery']),
  status: z.string(),
  rewards: z.object({
    balance: z.number().int().optional(),
    traffic_bytes: z.number().int().optional(),
    expire_days: z.number().int().optional(),
    reset_quota: z.boolean().optional(),
    plan_id: z.string().optional(),
    price_id: z.string().optional(),
    pool: z.array(z.object({ label: z.string(), weight: z.number() })).optional(),
  }),
  conditions: z.record(z.string(), z.unknown()),
  limits: z.record(z.string(), z.unknown()),
  theme_color: z.string(),
  created_at: z.string(),
  plan_name: z.string().optional(),
  interval: z.string().optional(),
  interval_count: z.number().int().optional(),
  placement: placementSchema.nullable(),
})
export type GiftCard = z.output<typeof giftCardSchema>
