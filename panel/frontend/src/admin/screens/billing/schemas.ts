import { z } from 'zod'

// ---------------------------------------------------------------------------
// 封闭枚举（迁移里的 CHECK）：未知值判为不符约定
// ---------------------------------------------------------------------------
export const ORDER_STATUSES = ['draft', 'pending_payment', 'processing', 'paid', 'fulfilled', 'cancelled', 'expired', 'refunded', 'partially_refunded'] as const
export const ORDER_KINDS = ['new', 'renewal', 'upgrade', 'downgrade', 'addon', 'topup', 'manual'] as const
export const INTENT_STATUSES = ['created', 'requires_action', 'processing', 'succeeded', 'failed', 'cancelled', 'expired'] as const
export const PAYMENT_STATUSES = ['succeeded', 'refunded', 'partially_refunded', 'disputed', 'reversed'] as const
export const REFUND_STATUSES = ['pending', 'approved', 'processing', 'succeeded', 'failed', 'rejected'] as const
export const LATE_STATUSES = ['suspense', 'refund_pending', 'refunded', 'manual_review', 'applied'] as const
export const LATE_KINDS = ['released_order', 'excess_capture', 'ineligible_subscription'] as const
export const CURRENCIES = ['CNY', 'USD'] as const
export type OrderStatus = (typeof ORDER_STATUSES)[number]
export type OrderKind = (typeof ORDER_KINDS)[number]
export type IntentStatus = (typeof INTENT_STATUSES)[number]
export type PaymentStatus = (typeof PAYMENT_STATUSES)[number]
export type RefundStatus = (typeof REFUND_STATUSES)[number]
export type LateStatus = (typeof LATE_STATUSES)[number]
export type LateKind = (typeof LATE_KINDS)[number]
export type Currency = (typeof CURRENCIES)[number]

const int = z.number().int()
const count = int.nonnegative()
const time = z.string()
/** 金额按币种分开的合计（分和美分不能相加，R3 / R66）：币种 → 最小单位 */
const byCurrency = z.record(z.string(), int)

// ---------------------------------------------------------------------------
// GET v1/orders：列表行（R63 加 balance_applied 与渠道；plan_name / interval 是首项快照）
// ---------------------------------------------------------------------------
export const orderRowSchema = z.object({
  id: z.string(),
  order_no: z.string(),
  user_email: z.string(),
  kind: z.enum(ORDER_KINDS),
  status: z.enum(ORDER_STATUSES),
  currency: z.string(),
  total_amount: int,
  payable_amount: int,
  paid_amount: int,
  refunded_amount: int,
  balance_applied: int,
  created_at: time,
  paid_at: time.nullable(),
  provider_code: z.string().nullable(),
  provider_name: z.string().nullable(),
  plan_name: z.string(),
  interval: z.string(),
  interval_count: int,
  item_count: int,
  // R95 / R114：manual_reason 非空即人工单（开单人仍只在详情里）
  manual: z.boolean(),
})
export const ordersSchema = z.object({ orders: z.array(orderRowSchema), total: count })
export type OrderRow = z.output<typeof orderRowSchema>

// ---------------------------------------------------------------------------
// GET v1/orders/{id}：详情（列表同一份查询 + 快照与开单人，R63）
// ---------------------------------------------------------------------------
const orderItemSchema = z.object({
  id: z.string(),
  product_id: z.string().nullable(),
  price_id: z.string().nullable(),
  plan_id: z.string().nullable(),
  plan_version_id: z.string().nullable(),
  product_name: z.string(),
  plan_name: z.string().nullable(),
  plan_version: int.nullable(),
  interval: z.string().nullable(),
  interval_count: int.nullable(),
  snapshot_entitlements: z.unknown(),
  snapshot_quotas: z.unknown(),
  quantity: int,
  unit_amount: int,
  line_amount: int,
  currency: z.string(),
  created_at: time,
})
export const orderDetailSchema = orderRowSchema.extend({
  user_id: z.string(),
  organization_id: z.string().nullable(),
  state_version: int,
  subtotal_amount: int,
  discount_amount: int,
  tax_amount: int,
  coupon_id: z.string().nullable(),
  manual_reason: z.string().nullable(),
  created_by: z.string().nullable(),
  created_by_email: z.string().nullable(),
  subscription_id: z.string().nullable(),
  expires_at: time.nullable(),
  fulfilled_at: time.nullable(),
  cancelled_at: time.nullable(),
  expired_at: time.nullable(),
  cancel_reason: z.string().nullable(),
  updated_at: time,
  items: z.array(orderItemSchema),
})
export const orderResponseSchema = z.object({ order: orderDetailSchema })
export type OrderDetail = z.output<typeof orderDetailSchema>
export type OrderItem = z.output<typeof orderItemSchema>

// ---------------------------------------------------------------------------
// GET v1/orders/{id}/payments：支付尝试、入账、退款（billing.payment.read，比读订单高一级）
// ---------------------------------------------------------------------------
const intentSchema = z.object({
  id: z.string(),
  provider_code: z.string(),
  provider_name: z.string(),
  currency: z.string(),
  amount: int,
  status: z.enum(INTENT_STATUSES),
  provider_ref: z.string().nullable(),
  failure_code: z.string().nullable(),
  failure_message: z.string().nullable(),
  expires_at: time.nullable(),
  created_at: time,
  updated_at: time,
})
const paymentSchema = z.object({
  id: z.string(),
  payment_intent_id: z.string().nullable(),
  provider_code: z.string(),
  provider_name: z.string(),
  provider_payment_id: z.string(),
  currency: z.string(),
  amount: int,
  fee_amount: int,
  refunded_amount: int,
  status: z.enum(PAYMENT_STATUSES),
  method: z.string().nullable(),
  paid_at: time,
})
const refundSchema = z.object({
  id: z.string(),
  payment_id: z.string().nullable(),
  provider_refund_id: z.string().nullable(),
  currency: z.string(),
  amount: int,
  reason: z.string(),
  status: z.enum(REFUND_STATUSES),
  entitlement_revoked: z.boolean(),
  commission_reversed: z.boolean(),
  failure_message: z.string().nullable(),
  succeeded_at: time.nullable(),
  created_at: time,
})
export const paymentHistorySchema = z.object({ payment_intents: z.array(intentSchema), payments: z.array(paymentSchema), refunds: z.array(refundSchema) })
export type PaymentHistory = z.output<typeof paymentHistorySchema>
export type PaymentIntent = z.output<typeof intentSchema>
export type Payment = z.output<typeof paymentSchema>
export type Refund = z.output<typeof refundSchema>

// ---------------------------------------------------------------------------
// 订单写接口的响应
// ---------------------------------------------------------------------------
/** POST v1/orders/{id}/cancel：billing.ReleaseOrderOutput，cancelled_at / cancel_reason 是 omitempty */
export const cancelledSchema = z.object({
  order: z.object({
    order_id: z.string(),
    status: z.enum(ORDER_STATUSES),
    state_version: int,
    cancelled_at: time.optional(),
    cancel_reason: z.string().optional(),
    already_terminal: z.boolean(),
  }),
  already_terminal: z.boolean(),
})
/** POST v1/orders/manual：201，业务层预写进幂等记录的那一份（billing.CreateOrderOutput） */
export const manualCreatedSchema = z.object({
  discount_amount: int,
  order_id: z.string(),
  order_no: z.string(),
  currency: z.string(),
  total_amount: int,
  balance_applied: int,
  payable_amount: int,
  status: z.enum(ORDER_STATUSES),
  // 用户已有别的套餐的订阅时落成原订阅上的变更单（2026-10-07），多这两项：原套餐的剩余价值与退进余额的部分
  proration_credit: int.optional(),
  balance_refund: int.optional(),
})
export type ManualCreated = z.output<typeof manualCreatedSchema>
/** POST v1/orders/{id}/mark-paid：修订 R2 起是 snake_case，不回 signature_failed */
export const markedPaidSchema = z.object({
  processed: z.boolean(),
  already_handled: z.boolean(),
  payment_id: z.string(),
  subscription_id: z.string(),
  ledger_txn_id: z.string(),
})

// POST v1/orders/{id}/query：向渠道查单（PAY-009），Go billing.OrderPaymentQuery；quarantine_kind 带 omitempty
export const orderQueriedSchema = z.object({
  order_id: z.string(),
  order_no: z.string(),
  provider_code: z.string(),
  channel_status: z.enum(['paid', 'unpaid', 'not_found']),
  reconciled: z.boolean(),
  already_recorded: z.boolean(),
  quarantine_kind: z.enum(LATE_KINDS).optional(),
  order_status: z.enum(ORDER_STATUSES),
})
export type OrderQueried = z.output<typeof orderQueriedSchema>

// ---------------------------------------------------------------------------
// GET v1/late-payments：挂账（保留规则 6，设计稿的「欠费单」）；R3 待处理合计按币种分开
// ---------------------------------------------------------------------------
const lateCaseSchema = z.object({
  id: z.string(),
  case_kind: z.enum(LATE_KINDS),
  status: z.enum(LATE_STATUSES),
  amount: int.positive(),
  currency: z.string(),
  order_no: z.string(),
  order_status: z.enum(ORDER_STATUSES),
  user_id: z.string(),
  user_email: z.string(),
  received_at: time,
  resolved_at: time.optional(),
  resolution_reason: z.string().optional(),
})
export const latePaymentsSchema = z.object({
  cases: z.array(lateCaseSchema),
  total: count,
  pending_amounts: byCurrency,
})
export type LateCase = z.output<typeof lateCaseSchema>
export type LatePayments = z.output<typeof latePaymentsSchema>
export const lateAppliedSchema = z.object({ ledger_txn_id: z.string() })

// ---------------------------------------------------------------------------
// GET v1/payment-providers：渠道卡（R66 加今日成交、24 小时成功率、最近回调）
// ---------------------------------------------------------------------------
const providerSchema = z.object({
  id: z.string(),
  code: z.string(),
  adapter: z.string(),
  display_name: z.string(),
  enabled: z.boolean(),
  accepting_new: z.boolean(),
  has_credentials: z.boolean(),
  base_url: z.string(),
  currencies: z.array(z.string()),
  // 编辑表单回填用的非机密配置（w2pay）；商户号与密钥只写不读，只有 has_credentials
  submit_path: z.string(),
  api_path: z.string(),
  methods: z.array(z.string()),
  default_method: z.string(),
  allow_private_host: z.boolean(),
  today: byCurrency,
  success_rate_24h: z.number().min(0).max(1).nullable(),
  last_callback_at: time.nullable(),
})
export const providersSchema = z.object({ providers: z.array(providerSchema) })
export type Provider = z.output<typeof providerSchema>
export const toggledSchema = z.object({ ok: z.literal(true) })
// POST v1/payment-providers（201）与 PUT v1/payment-providers/{code}（200）同形；只回凭据是否变更
export const providerWrittenSchema = z.object({ id: z.string(), code: z.string(), credentials_changed: z.boolean() })

// ---------------------------------------------------------------------------
// 收入调整（报表口径，只追加）：列表、登记与冲销同形；reversal_of 是 omitempty，R66 登记人
// ---------------------------------------------------------------------------
export const adjustmentSchema = z.object({
  id: z.string(),
  currency: z.enum(CURRENCIES),
  amount: int,
  reason: z.string(),
  effective_on: z.string(),
  reversal_of: z.string().optional(),
  created_by: z.string(),
  created_by_email: z.string().nullable(),
  created_at: time,
  reversed: z.boolean(),
})
export const adjustmentsSchema = z.object({ adjustments: z.array(adjustmentSchema) })
export type Adjustment = z.output<typeof adjustmentSchema>
