/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useQuery，依赖 zod，依赖 ../../../shell/runtime 的 useApi
 * [OUTPUT]: 对外提供 ORDER_KINDS、ORDER_STATUSES、orderRowSchema、OrderRow、intervalLabel、orderTitle、expiryNote、usePendingOrders、orderDetailSchema / OrderDetail / orderKey / useOrder、orderCreatedSchema / OrderCreated、PAID_STATUSES / SETTLED_STATUSES
 * [POS]: portal/screens/common 的订单读模型（契约门户-04 GET v1/orders）：概览顶部待支付条、结账后的支付结果确认先用，第 ③ 步订单页在此基础上扩展；标题与期限文案是纯函数、有单元测试
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../../../shell/runtime'

export const ORDER_KINDS = ['new', 'renewal', 'topup', 'addon', 'upgrade'] as const
export const ORDER_STATUSES = ['draft', 'pending_payment', 'processing', 'paid', 'fulfilled', 'cancelled', 'expired', 'partially_refunded', 'refunded'] as const

export const orderRowSchema = z.object({
  id: z.string(),
  order_no: z.string(),
  kind: z.enum(ORDER_KINDS),
  status: z.enum(ORDER_STATUSES),
  currency: z.string(),
  total_amount: z.number().int(),
  discount_amount: z.number().int(),
  balance_applied: z.number().int(),
  payable_amount: z.number().int(),
  paid_amount: z.number().int(),
  refunded_amount: z.number().int(),
  plan_name: z.string().optional(),
  cancellable: z.boolean(),
  created_at: z.string(),
  paid_at: z.string().optional(),
  cancelled_at: z.string().optional(),
  cancel_reason: z.string().optional(),
  expires_at: z.string().optional(),
  // 修订 R69 已上线；写成可选，旧后端（假后端 legacy 场景）缺席时标题退回 plan_name
  interval: z.string().optional(),
  interval_count: z.number().int().optional(),
  item_name: z.string().optional(),
})
export type OrderRow = z.output<typeof orderRowSchema>

export const ordersPageSchema = z.object({
  orders: z.array(orderRowSchema),
  total: z.number().int(),
  // 修订 R69
  counts: z.object({ open: z.number().int(), paid: z.number().int(), closed: z.number().int(), refunded: z.number().int() }).optional(),
})

/** 价格周期 → 「月付 / 季付 / 年付」；billing_interval 取值见 00003 的 CHECK。 */
const NAMED_PERIODS: Readonly<Record<string, string>> = {
  'month:1': '月付',
  'month:3': '季付',
  'quarter:1': '季付',
  'month:6': '半年付',
  'month:12': '年付',
  'year:1': '年付',
}
const PERIOD_UNITS: Readonly<Record<string, string>> = { day: '天', week: '周', month: '个月', quarter: '个季度', year: '年' }

export function intervalLabel(interval: string | undefined, count = 1): string {
  if (!interval) return ''
  if (interval === 'one_time') return '一次性'
  const named = NAMED_PERIODS[`${interval}:${count}`]
  if (named) return named
  const unit = PERIOD_UNITS[interval]
  return unit ? `${count} ${unit}` : ''
}

/** 契约门户-04 的标题映射；待补字段缺席时退回 plan_name（修订 R32：流量包订单的 plan_name 即流量包名）。 */
export function orderTitle(o: Pick<OrderRow, 'kind' | 'plan_name' | 'item_name' | 'interval' | 'interval_count'>): string {
  const plan = o.plan_name ?? ''
  const period = intervalLabel(o.interval, o.interval_count)
  switch (o.kind) {
    case 'topup':
      return '余额充值'
    case 'addon':
      return `流量包 · ${o.item_name ?? plan}`
    case 'upgrade':
      return `${plan} · 变更套餐`
    case 'renewal':
      return `${plan} · ${period ? `${period}续费` : '续费'}`
    default:
      return period ? `${plan} · ${period}` : plan
  }
}

/** 待支付条的期限：后端 30 分钟过期，按 expires_at 倒数。 */
export function expiryNote(expiresAt: string | undefined, now: Date = new Date()): string {
  if (!expiresAt) return '请尽快完成支付'
  const minutes = Math.ceil((new Date(expiresAt).getTime() - now.getTime()) / 60_000)
  return minutes > 0 ? `${minutes} 分钟内未支付将自动取消` : '即将自动取消'
}

/** 概览顶部待支付条：契约写的是 status=pending_payment。 */
export function usePendingOrders() {
  const api = useApi()
  return useQuery({
    queryKey: ['portal', 'orders', { status: 'pending_payment' }],
    queryFn: ({ signal }) => api.get('v1/orders', ordersPageSchema, { query: { status: 'pending_payment' }, signal }),
    select: (d) => d.orders,
    meta: { topics: ['orders.changed'] },
  })
}

// ---------------------------------------------------------------------------
// GET v1/orders/{id}（修订 R69 的明细字段）：支付回跳后的结果确认与第 ③ 步订单明细共用
// ---------------------------------------------------------------------------
export const orderDetailSchema = z.object({
  order: orderRowSchema.extend({
    items: z.array(z.object({ name: z.string(), quantity: z.number().int(), unit_amount: z.number().int(), line_amount: z.number().int() })),
    payments: z.array(
      z.object({
        status: z.string(),
        amount: z.number().int(),
        currency: z.string(),
        created_at: z.string(),
        method: z.string().optional(),
        provider_name: z.string().optional(),
      }),
    ),
    coupon_code: z.string().optional(),
    subscription_period_end: z.string().optional(),
  }),
})
export type OrderDetail = z.output<typeof orderDetailSchema>['order']

export const orderKey = (id: string) => ['portal', 'orders', 'detail', id] as const

/** 不会再变成已支付的终态 */
export const SETTLED_STATUSES: ReadonlySet<string> = new Set(['paid', 'fulfilled', 'cancelled', 'expired', 'partially_refunded', 'refunded'])

/** poll 给毫秒数时一直轮询到订单进入终态（支付回跳 3 秒一次）；false 不轮询 */
export function useOrder(id: string | undefined, poll: number | false = false) {
  const api = useApi()
  return useQuery({
    queryKey: orderKey(id ?? ''),
    queryFn: ({ signal }) => api.get(`v1/orders/${encodeURIComponent(id!)}`, orderDetailSchema, { signal }),
    select: (d) => d.order,
    enabled: id !== undefined && id !== '',
    refetchInterval: poll === false ? false : (query) => (SETTLED_STATUSES.has(query.state.data?.order.status ?? '') ? false : poll),
    meta: { topics: ['orders.changed'] },
  })
}

// ---------------------------------------------------------------------------
// 下单四个接口（新购、续费、变更套餐、流量包）同形的 201 响应
// ---------------------------------------------------------------------------
export const orderCreatedSchema = z.object({
  order_id: z.string(),
  order_no: z.string(),
  currency: z.string(),
  total_amount: z.number().int(),
  discount_amount: z.number().int(),
  balance_applied: z.number().int(),
  payable_amount: z.number().int(),
  status: z.enum(['pending_payment', 'fulfilled']),
  // 变更套餐另带（修订 R36）
  proration_credit: z.number().int().optional(),
  balance_refund: z.number().int().optional(),
})
export type OrderCreated = z.output<typeof orderCreatedSchema>

export const PAID_STATUSES: ReadonlySet<string> = new Set(['paid', 'fulfilled'])
