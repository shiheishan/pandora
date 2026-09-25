/**
 * [INPUT]: 依赖 zod
 * [OUTPUT]: 对外提供 RESET_STRATEGIES、priceSchema / Price、planSchema / Plan
 * [POS]: portal/screens/common 的套餐目录形状层（纯 zod，不碰 React）：契约门户-03 GET v1/plans 的一行（含修订 R69、R99、R100）；catalog.ts 转出并在其上建查询与文案，tests/mock-portal.test.ts 直接拿它核对假后端
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { z } from 'zod'

// ---------------------------------------------------------------------------
// GET v1/plans（修订 R69 的四个字段写成可选：旧后端缺席时文案降级；R99 / R100 的三个字段按契约必填）
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
  quota_reset_strategy: z.enum(RESET_STRATEGIES).optional(),
  quota_reset_day: z.number().int().nullable().optional(),
  allow_renewal: z.boolean().optional(),
  allow_upgrade: z.boolean().optional(),
  // 修订 R99：当前发布版本的限速（kbps），null = 不限；R100：卖点（按顺序）与「推荐」
  throttle_kbps: z.number().int().positive().nullable(),
  highlights: z.array(z.string()),
  recommended: z.boolean(),
})
export type Plan = z.output<typeof planSchema>
