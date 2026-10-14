import { z } from 'zod'

// POST v1/me/checkout/quote 的 schema（纯 zod）：报价钩子 quote.ts 转出，tests/ 的假后端测试也直接用它
export const balanceSplitSchema = z.object({
  /** 用掉的余额 */
  applied: z.number().int(),
  /** 还需在线支付 */
  payable: z.number().int(),
  /** 为了凑够支付最低额少用、留在余额里的钱 */
  kept: z.number().int(),
  /** 应付本身低于最低额且余额够付：只能全用余额，开关锁成打开 */
  forced: z.boolean(),
  /** 用尽余额后剩下的钱低于在线支付最低额：这一单下不了（按钮置灰，先充值或换更长的时长） */
  below_minimum: z.boolean(),
  /** 只出现在换套餐：抵扣后剩的零头（≤ ¥0.99）已免掉，payable 归零 */
  small_due: z.boolean(),
  /** 免掉的零头（分） */
  waived: z.number().int(),
})
export type BalanceSplit = z.output<typeof balanceSplitSchema>

export const creditDetailSchema = z.object({
  paid: z.number().int(),
  days_left: z.number().int(),
  days_total: z.number().int(),
  traffic_left: z.number().int(),
  traffic_total: z.number().int(),
  ratio_ppm: z.number().int(),
})
export type CreditDetail = z.output<typeof creditDetailSchema>

export const quoteRowSchema = z.object({
  subscription_id: z.string().nullable(),
  plan_id: z.string().nullable(),
  pack_id: z.string().nullable(),
  price_id: z.string().nullable(),
  interval: z.string(),
  interval_count: z.number().int(),
  subtotal: z.number().int(),
  discount: z.number().int(),
  coupon: z.object({ code: z.string() }).nullable(),
  coupon_error: z.string().nullable(),
  /** 原套餐没用完的部分（只有换套餐非零） */
  credit: z.number().int(),
  credit_detail: creditDetailSchema.nullable(),
  /** max(小计 − 优惠 − 原套餐没用完的部分, 0) */
  total: z.number().int(),
  /** 换便宜套餐时退进余额的差额 */
  refund: z.number().int(),
  with_balance: balanceSplitSchema,
  without_balance: balanceSplitSchema,
  period_start: z.string().nullable(),
  period_end: z.string().nullable(),
  previous_end: z.string().nullable(),
})
export type QuoteRow = z.output<typeof quoteRowSchema>

export const quoteSchema = z.object({
  /** 报价时刻：建单时原样带回，剩余价值的时间比例按它算（服务端只收 10 分钟内的） */
  as_of: z.string(),
  currency: z.string(),
  balance: z.number().int(),
  /** 支付最低额（分）：所有启用渠道里最大的那个 */
  min_payment: z.number().int(),
  quotes: z.array(quoteRowSchema),
})
export type Quote = z.output<typeof quoteSchema>
