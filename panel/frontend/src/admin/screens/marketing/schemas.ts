/**
 * [INPUT]: 依赖 zod
 * [OUTPUT]: 对外提供营销页全部接口的 zod schema 与推导类型：优惠券、兑换记录、礼品卡模板 / 统计 / 批次 / 卡码 / 使用记录 / 生码结果、佣金总览、提现，以及营销页用到的套餐目录子集
 * [POS]: admin/screens/marketing 与后端对账的唯一防线：形状逐字取自 api-contract.md 后台-06（含 R4 R5 R6 R17）并与 Go 处理器的 json tag 核对过；待补·后端的字段标可选，Go 的 omitempty 字段标可选，nil 切片可能序列化成 null 的标 nullable
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { z } from 'zod'

const iso = z.string()
const uuid = z.string()
/** Go 的 nil 切片序列化成 null：统一归一成空数组 */
const ids = z
  .array(uuid)
  .nullable()
  .transform((v) => v ?? [])

// ---------------------------------------------------------------------------
// 套餐目录子集（GET v1/plans，catalog.read）：只收营销页要的名字与价格
// ---------------------------------------------------------------------------
export const planSchema = z.object({
  id: uuid,
  name: z.string(),
  status: z.enum(['draft', 'active', 'archived']),
  prices: z.array(
    z.object({
      id: uuid,
      currency: z.string(),
      unit_amount: z.number(),
      billing_interval: z.string(),
      interval_count: z.number(),
      status: z.enum(['active', 'archived']),
    }),
  ),
})
export const plansResponse = z.object({ plans: z.array(planSchema) })
export type Plan = z.output<typeof planSchema>

// ---------------------------------------------------------------------------
// 优惠券
// ---------------------------------------------------------------------------
export const couponSchema = z.object({
  id: uuid,
  code: z.string(),
  name: z.string(),
  discount_type: z.enum(['percent', 'fixed']),
  discount_value: z.number(),
  currency: z.string(),
  max_discount: z.number().nullable(),
  min_order_amount: z.number(),
  max_redemptions: z.number().nullable(),
  max_redemptions_per_user: z.number(),
  redeemed_count: z.number(),
  applicable_plan_ids: ids,
  valid_from: iso.nullable(),
  valid_until: iso.nullable(),
  status: z.string(),
  created_at: iso,
  discounted_total: z.number(),
})
export const couponsResponse = z.object({ coupons: z.array(couponSchema), total: z.number() })
export type Coupon = z.output<typeof couponSchema>

export const redemptionsResponse = z.object({
  redemptions: z
    .array(z.object({ email: z.string(), order_no: z.string(), discount: z.number(), currency: z.string(), at: iso, reverted: z.boolean() }))
    .nullable()
    .transform((v) => v ?? []),
})
export type Redemption = z.output<typeof redemptionsResponse>['redemptions'][number]

export const couponCreated = z.object({ id: uuid, code: z.string() })
export const couponBatchCreated = z.object({ count: z.number(), codes: z.array(z.string()), name: z.string() })
export const okResponse = z.object({ ok: z.literal(true) })

// ---------------------------------------------------------------------------
// 礼品卡
// ---------------------------------------------------------------------------
const prizeSchema = z.object({
  label: z.string(),
  weight: z.number(),
  balance: z.number().optional(),
  traffic_bytes: z.number().optional(),
  expire_days: z.number().optional(),
})
const rewardsSchema = z.object({
  balance: z.number().optional(),
  traffic_bytes: z.number().optional(),
  expire_days: z.number().optional(),
  reset_quota: z.boolean().optional(),
  plan_id: uuid.optional(),
  price_id: uuid.optional(),
  pool: z.array(prizeSchema).optional(),
})
export type Rewards = z.output<typeof rewardsSchema>

export const templateSchema = z.object({
  id: uuid,
  name: z.string(),
  description: z.string(),
  type: z.enum(['general', 'plan', 'mystery']),
  status: z.enum(['active', 'paused', 'archived']),
  rewards: rewardsSchema,
  conditions: z.object({
    new_user_only: z.boolean().optional(),
    paid_user_only: z.boolean().optional(),
    allowed_plan_ids: z.array(uuid).optional(),
    require_invite: z.boolean().optional(),
  }),
  limits: z.object({ max_use_per_user: z.number().optional(), cooldown_hours: z.number().optional() }),
  theme_color: z.string(),
  code_total: z.number(),
  code_used: z.number(),
  created_at: iso,
})
export const templatesResponse = z.object({
  templates: z
    .array(templateSchema)
    .nullable()
    .transform((v) => v ?? []),
})
export const templateSaved = z.object({ template: templateSchema })
export type GiftTemplate = z.output<typeof templateSchema>

/** balance_issued 是待补·后端（扩展），缺时统计格退回 balance_out 并改标签 */
export const giftStatsSchema = z.object({
  templates: z.number(),
  codes_total: z.number(),
  codes_used: z.number(),
  codes_unused: z.number(),
  balance_out: z.number(),
  traffic_out: z.number(),
  balance_issued: z.number().optional(),
})
export type GiftStats = z.output<typeof giftStatsSchema>

export const batchSchema = z.object({
  id: uuid,
  template_id: uuid,
  template_name: z.string(),
  prefix: z.string(),
  count: z.number(),
  used: z.number(),
  disabled: z.number(),
  expires_at: iso.nullable(),
  created_by_email: z.string().nullable(),
  created_at: iso,
  exported_at: iso.nullable(),
  exported_by_email: z.string().nullable(),
})
export const batchesResponse = z.object({
  items: z
    .array(batchSchema)
    .nullable()
    .transform((v) => v ?? []),
  total: z.number(),
})
export type Batch = z.output<typeof batchSchema>

/** R17：只有 code_masked，没有明文 */
export const codeSchema = z.object({
  id: uuid,
  code_masked: z.string(),
  status: z.enum(['unused', 'used', 'disabled', 'expired']),
  batch_id: uuid.optional(),
  expires_at: iso.optional(),
  used_email: z.string().optional(),
  used_at: iso.optional(),
  created_at: iso,
  template_id: uuid,
})
export const codesResponse = z.object({
  codes: z
    .array(codeSchema)
    .nullable()
    .transform((v) => v ?? []),
  total: z.number(),
})
export type GiftCode = z.output<typeof codeSchema>

export const usageSchema = z.object({
  template_name: z.string(),
  code_masked: z.string(),
  user_email: z.string(),
  granted: z.object({
    balance: z.number().optional(),
    traffic_bytes: z.number().optional(),
    expire_days: z.number().optional(),
    reset_quota: z.boolean().optional(),
  }),
  prize_label: z.string().optional(),
  redeemed_at: iso,
})
export const usagesResponse = z.object({
  usages: z
    .array(usageSchema)
    .nullable()
    .transform((v) => v ?? []),
})
export type Usage = z.output<typeof usageSchema>

/** R17：sample 只有前 4 张明文，完整明文只能一次性导出 */
export const codesGenerated = z.object({ batch_id: uuid, count: z.number(), sample: z.array(z.string()), batch: batchSchema })
export type CodesGenerated = z.output<typeof codesGenerated>
export const toggleResponse = z.object({ disabled: z.boolean() })

// ---------------------------------------------------------------------------
// 佣金与提现
// ---------------------------------------------------------------------------
/** total_earned / invited_users / scope 是待补·后端（扩展），按可选 */
export const overviewSchema = z.object({
  pending: z.number(),
  available: z.number(),
  paid_out: z.number(),
  this_month: z.number(),
  entries: z.number(),
  need_review: z.number(),
  waiting_withdrawals: z.number(),
  rate_percent: z.number(),
  freeze_days: z.number(),
  min_withdraw: z.number(),
  total_earned: z.number().optional(),
  invited_users: z.number().optional(),
  scope: z.enum(['first_order', 'every_order']).optional(),
})
export type CommissionOverview = z.output<typeof overviewSchema>

export const withdrawalSchema = z.object({
  id: uuid,
  email: z.string(),
  user_id: uuid,
  amount: z.number(),
  currency: z.string(),
  status: z.string(),
  payout_detail: z.string(),
  reject_reason: z.string(),
  requested_at: iso,
  completed_at: iso.nullable(),
  earned_total: z.number(),
})
export const withdrawalsResponse = z.object({
  withdrawals: z
    .array(withdrawalSchema)
    .nullable()
    .transform((v) => v ?? []),
})
export type Withdrawal = z.output<typeof withdrawalSchema>

export const reviewResponse = z.object({ status: z.enum(['approved', 'rejected']) })
export const paidResponse = z.object({ status: z.literal('paid') })
