// ---------------------------------------------------------------------------
// 单测夹具（只给 *.test.ts 用，页面不引用）：原型的三档套餐与「一份」的工厂，字段与 Go 一一对应
// ---------------------------------------------------------------------------
import { subscriptionSchema, type Subscription } from '../../queries'
import type { Plan, Price } from './catalog'
import type { BalanceSplit, Quote, QuoteRow } from './quote'
import type { SubscriptionLink } from './subscriptions'

export const GIB = 1024 ** 3
const price = (id: string, unit_amount: number, billing_interval: Price['billing_interval'], interval_count = 1, validity_days: number | null = null): Price => ({ id, unit_amount, billing_interval, interval_count, validity_days })

const plan = (id: string, name: string, gb: number, devices: number, p: [number, number, number], recommended = false): Plan => ({
  id,
  name,
  description: null,
  kind: 'subscription',
  recommended,
  allow_new_purchase: true,
  allow_renewal: true,
  traffic_bytes: gb * GIB,
  max_devices: devices,
  throttle_kbps: null,
  prices: [price(`${id}1`, p[0], 'month'), price(`${id}3`, p[1], 'month', 3), price(`${id}12`, p[2], 'year')],
  reset: null,
})

export const BASIC = plan('basic', '基础版', 50, 2, [1500, 4200, 15000])
export const STD = plan('std', '标准版', 100, 3, [3000, 8400, 30000], true)
export const PRO = plan('pro', '进阶版', 300, 5, [4500, 12600, 45000])
/** 一次性套餐：¥20 共 100G、有效 30 天，可付费重置 ¥5 / 次、有效期内最多 2 次 */
export const ONCE = {
  id: 'once',
  name: '流量版',
  description: null,
  kind: 'one_time',
  recommended: false,
  allow_new_purchase: true,
  allow_renewal: false,
  traffic_bytes: 100 * GIB,
  max_devices: 2,
  throttle_kbps: null,
  prices: [price('once1', 2000, 'one_time', 1, 30)],
  reset: { price: 500, limit: 2 },
} satisfies Plan
export const PLANS = [BASIC, STD, PRO]

export function sub(over: Partial<Subscription> & { usedGiB?: number; capGiB?: number } = {}): Subscription {
  const { usedGiB = 40, capGiB = 100, ...rest } = over
  const status = rest.status ?? 'active'
  const live = status === 'active'
  return subscriptionSchema.parse({
    id: 'sub1',
    plan_id: 'std',
    price_id: 'std1',
    plan_name: '标准版',
    plan_version: 1,
    plan_kind: 'subscription',
    status,
    state: live ? 'live' : 'revivable',
    current_period_start: '2026-09-19T12:00:00Z',
    current_period_end: '2026-10-19T12:00:00Z',
    amount: 3000,
    validity_days: null,
    quotas: [{ metric: 'traffic.bytes', limit: capGiB * GIB, bonus_bytes: 0, consumed: usedGiB * GIB, remaining: Math.max(0, capGiB - usedGiB) * GIB, period: 'cycle', period_start: '2026-09-19T12:00:00Z', period_end: '2026-10-19T12:00:00Z' }],
    device_limit: 3,
    online_devices: 1,
    traffic_limit: capGiB * GIB,
    bonus_bytes: 0,
    traffic_used: usedGiB * GIB,
    permanent: false,
    resets_used: 0,
    reset_limit: null,
    reset_price: null,
    next_reset_at: '2026-10-19T12:00:00Z',
    renewable: live,
    stackable: false,
    renewal_price: { id: 'std1', unit_amount: 3000, billing_interval: 'month', interval_count: 1, available: true },
    label: null,
    client_name: `Pandora · ${rest.label ?? rest.plan_name ?? '标准版'}`,
    changeable: live,
    renew_until: live ? '2026-11-19T12:00:00Z' : null,
    usage: { traffic_bytes: capGiB * GIB, device_limit: 3, throttle_kbps: null },
    next_usage: null,
    is_trial: false,
    ...rest,
  })
}

export const link = (subscriptionId: string, tail: string, over: Partial<SubscriptionLink> = {}): SubscriptionLink => ({
  subscription_id: subscriptionId,
  url: `https://sub.example.com/s/xxxxxxxx${tail}`,
  fetch_count: 3,
  last_fetched_at: null,
  distinct_sources_24h: 1,
  expired: false,
  ...over,
})

const split = (over: Partial<BalanceSplit> = {}): BalanceSplit => ({ applied: 0, payable: 0, kept: 0, forced: false, below_minimum: false, small_due: false, waived: 0, ...over })

export function row(over: Partial<QuoteRow> = {}): QuoteRow {
  return {
    subscription_id: 'sub1',
    plan_id: 'std',
    price_id: 'std1',
    interval: 'month',
    interval_count: 1,
    validity_days: null,
    subtotal: 3000,
    discount: 0,
    coupon: null,
    coupon_error: null,
    credit: 0,
    credit_detail: null,
    total: 3000,
    refund: 0,
    with_balance: split({ payable: 3000 }),
    without_balance: split({ payable: 3000 }),
    period_start: '2026-10-19T12:00:00Z',
    period_end: '2026-11-19T12:00:00Z',
    previous_end: '2026-10-19T12:00:00Z',
    ...over,
  }
}

export const quote = (rows: QuoteRow[], over: Partial<Quote> = {}): Quote => ({ as_of: '2026-10-07T12:00:00Z', balance: 0, min_payment: 100, quotes: rows, ...over })
export { split }
