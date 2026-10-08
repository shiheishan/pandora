// ---------------------------------------------------------------------------
// 单测夹具（只给 *.test.ts 用，页面不引用）：原型的三档套餐与「一份」的工厂，字段与 Go 一一对应
// ---------------------------------------------------------------------------
import { subscriptionSchema, type Subscription } from '../../queries'
import type { Pack, Plan, Price } from './catalog'
import type { BalanceSplit, Quote, QuoteRow } from './quote'
import type { SubscriptionLink } from './subscriptions'

export const GIB = 1024 ** 3
export const price = (id: string, unit_amount: number, billing_interval: Price['billing_interval'], interval_count = 1, currency = 'CNY'): Price => ({ id, currency, unit_amount, billing_interval, interval_count, trial_days: 0 })

const plan = (id: string, name: string, gb: number, devices: number, p: [number, number, number], recommended = false): Plan => ({
  id,
  code: id,
  name,
  description: null,
  version: 1,
  max_devices: devices,
  quotas: [{ metric: 'traffic.bytes', limit: gb * GIB, unit: 'bytes', period: 'month' }],
  prices: [price(`${id}1`, p[0], 'month'), price(`${id}3`, p[1], 'month', 3), price(`${id}12`, p[2], 'year')],
  quota_reset_strategy: 'billing_cycle',
  quota_reset_day: null,
  allow_renewal: true,
  allow_upgrade: true,
  throttle_kbps: null,
  highlights: [],
  recommended,
})

export const BASIC = plan('basic', '基础版', 50, 2, [1500, 4200, 15000])
export const STD = plan('std', '标准版', 100, 3, [3000, 8400, 30000], true)
export const PRO = plan('pro', '进阶版', 300, 5, [4500, 12600, 45000])
export const PLANS = [BASIC, STD, PRO]
export const PACK100: Pack = { id: 'k100', name: '100 GB', traffic_bytes: 100 * GIB, currency: 'CNY', unit_amount: 1800, recommended: true }

export function sub(over: Partial<Subscription> & { usedGiB?: number; capGiB?: number } = {}): Subscription {
  const { usedGiB = 40, capGiB = 100, ...rest } = over
  const status = rest.status ?? 'active'
  const live = ['active', 'trialing', 'grace', 'past_due'].includes(status)
  return subscriptionSchema.parse({
    id: 'sub1',
    plan_id: 'std',
    price_id: 'std1',
    plan_name: '标准版',
    plan_version: 1,
    status,
    current_period_start: '2026-09-19T12:00:00Z',
    current_period_end: '2026-10-19T12:00:00Z',
    currency: 'CNY',
    amount: 3000,
    quotas: [{ metric: 'traffic.bytes', limit: capGiB * GIB, consumed: usedGiB * GIB, remaining: Math.max(0, capGiB - usedGiB) * GIB, period: 'month', period_start: '2026-09-19T12:00:00Z', period_end: null, granted_addon: 0, adjusted: 0 }],
    device_limit: 3,
    online_devices: 1,
    quota_reset_strategy: 'billing_cycle',
    next_reset_at: null,
    renewable: live,
    renewal_price: { id: 'std1', currency: 'CNY', unit_amount: 3000, billing_interval: 'month', interval_count: 1, available: true },
    pack_remaining_bytes: 0,
    label: null,
    client_name: `Pandora · ${rest.label ?? rest.plan_name ?? '标准版'}`,
    changeable: live,
    renew_until: live ? '2026-11-19T12:00:00Z' : null,
    legacy_movable_pack_bytes: 0,
    ...rest,
  })
}

export const link = (subscriptionId: string, tail: string, over: Partial<SubscriptionLink> = {}): SubscriptionLink => ({
  subscription_id: subscriptionId,
  url: `https://sub.example.com/s/xxxxxxxx${tail}`,
  expires_at: null,
  fetch_count: 3,
  last_fetched_at: null,
  distinct_sources_24h: 1,
  expired: false,
  ...over,
})

const split = (over: Partial<BalanceSplit> = {}): BalanceSplit => ({ applied: 0, payable: 0, kept: 0, forced: false, small_due: false, waived: 0, ...over })

export function row(over: Partial<QuoteRow> = {}): QuoteRow {
  return {
    subscription_id: 'sub1',
    plan_id: 'std',
    pack_id: null,
    price_id: 'std1',
    interval: 'month',
    interval_count: 1,
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

export const quote = (rows: QuoteRow[], over: Partial<Quote> = {}): Quote => ({ as_of: '2026-10-07T12:00:00Z', currency: 'CNY', balance: 0, min_payment: 100, quotes: rows, ...over })
export { split }
