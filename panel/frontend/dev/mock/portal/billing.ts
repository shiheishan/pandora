import { randomUUID } from 'node:crypto'
import type { AnonContext, Json, MockResult } from '../types.ts'
import { COUPONS, findPlan, type CatalogPlan, type CatalogPrice } from './catalog.ts'
import { addInterval, isLiveSub, makeSub, scenario, type OrderEffect, type OrderFixture, type PortalState, type SubFixture } from './fixtures.ts'

const ORDER_TTL_MS = 30 * 60_000

/** 业务拒绝：由调用方写成错误信封（在幂等表里也原样重放） */
export class BillingError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields?: Record<string, string>,
  ) {
    super(message)
  }
  result(): MockResult {
    return { status: this.status, body: { error: { code: this.code, message: this.message, ...(this.fields ? { fields: this.fields } : {}), request_id: randomUUID().slice(0, 8) } } }
  }
}

export const isUuid = (v: unknown): v is string => typeof v === 'string' && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(v)

/** 读请求体并拒绝多余字段（httpx.DecodeJSON 的 DisallowUnknownFields）；失败时已写好 400，返回 null */
export async function readStrict(ctx: AnonContext, allowed: readonly string[]): Promise<Json | null> {
  const body = await ctx.body()
  if (!body) {
    ctx.fail(400, 'bad_request', '请求体不是合法的 JSON 对象')
    return null
  }
  const extra = Object.keys(body).find((k) => !allowed.includes(k))
  if (extra) {
    ctx.fail(400, 'bad_request', `未知字段 ${extra}`)
    return null
  }
  return body
}

// ---------------------------------------------------------------------------
// 优惠码
// ---------------------------------------------------------------------------
/** 返回折扣（分）；码为空时 0；不可用时抛 BillingError（契约 coupons/preview 的错误列表） */
export function couponCheck(code: unknown, subtotal: number, priceId: string | null): number {
  if (typeof code !== 'string' || code.trim() === '') return 0
  const c = COUPONS[code.trim().toUpperCase()]
  if (!c) throw new BillingError(422, 'validation_failed', '优惠码不存在或已失效')
  if (c.error) throw new BillingError(c.error.status, c.error.code, c.error.message)
  if (c.onlyPrices && (!priceId || !c.onlyPrices.includes(priceId))) throw new BillingError(422, 'validation_failed', '这个优惠码不适用于所选套餐')
  if (c.minAmount && subtotal < c.minAmount) throw new BillingError(422, 'validation_failed', '订单金额未达到优惠码的使用门槛')
  return Math.min(subtotal, c.type === 'percent' ? Math.floor((subtotal * c.value) / 10000) : c.value)
}

/** 券面对象（billing.CouponFace）：码为空或不认识时 null；优惠码试算与变更试算恒带 coupon 键（Go 无 omitempty） */
export function couponFace(code: unknown) {
  const key = typeof code === 'string' ? code.trim().toUpperCase() : ''
  const c = key ? COUPONS[key] : undefined
  return c ? { code: key, discount_type: c.type, discount_value: c.value } : null
}

// ---------------------------------------------------------------------------
// 下单：金额由 quote.ts 的 settle 算好（余额已过 ApplyBalance），这里只落单、先扣（冻结）余额，
// 0 元当场履约。差价不到支付最低额且余额不够（SmallDue）时按设计稿 2.6 推荐 A 免掉、记为折扣
// ---------------------------------------------------------------------------
let orderSeq = 3400

export function placeOrder(
  state: PortalState,
  init: {
    kind: OrderFixture['kind']
    subtotal: number
    discount: number
    credit?: number
    /** 小计 − 折扣 − 剩余价值 */
    total: number
    /** 用掉的余额 */
    applied: number
    /** SmallDue 免掉的差价 */
    waived?: number
    couponCode?: string
    planName?: string
    itemName?: string
    price?: CatalogPrice
    subscriptionId?: string
    effect: OrderEffect
  },
): OrderFixture {
  if (init.applied > state.balance) throw new BillingError(409, 'conflict', '余额不足')
  const waived = init.waived ?? 0
  const total = init.total - waived
  const now = Date.now()
  const order: OrderFixture = {
    id: randomUUID(),
    order_no: `PD-2609-${++orderSeq}`,
    kind: init.kind,
    status: 'pending_payment',
    plan_name: init.planName,
    item_name: init.itemName,
    interval: init.price?.billing_interval,
    interval_count: init.price?.interval_count,
    subtotal: init.subtotal,
    total_amount: total,
    discount_amount: init.discount + waived,
    balance_applied: init.applied,
    payable_amount: total - init.applied,
    paid_amount: 0,
    coupon_code: init.couponCode,
    subscription_id: init.subscriptionId,
    created_at: new Date(now).toISOString(),
    expires_at: new Date(now + ORDER_TTL_MS).toISOString(),
    effect: init.effect,
  }
  state.orders.unshift(order)
  if (init.applied > 0) moveBalance(state, 'balance_hold', -init.applied, `订单 ${order.order_no}`)
  if (order.payable_amount === 0) fulfill(state, order)
  return order
}

/** 同 POST v1/orders 的 201 响应 */
export function createdView(o: OrderFixture) {
  return {
    order_id: o.id,
    order_no: o.order_no,
    currency: 'CNY',
    total_amount: o.total_amount,
    discount_amount: o.discount_amount,
    balance_applied: o.balance_applied,
    payable_amount: o.payable_amount,
    status: o.status === 'fulfilled' ? 'fulfilled' : 'pending_payment',
    ...(o.effect.type === 'upgrade' ? { proration_credit: o.effect.credit, balance_refund: o.effect.refund } : {}),
  }
}

/** 履约时名字已被别的份占了（付款期间改了名）：加「 2」「 3」后缀，不让结算失败（设计稿 2.4 provision） */
function freeLabel(state: PortalState, label: string | null): string | null {
  if (!label) return null
  const taken = new Set(state.subs.map((s) => s.label?.toLowerCase()).filter(Boolean))
  if (!taken.has(label.toLowerCase())) return label
  for (let n = 2; ; n++) if (!taken.has(`${label} ${n}`.toLowerCase())) return `${label} ${n}`
}

/** 订阅按价格续一期：生效中的接在原到期日后，过期 30 天内的从现在起算（恢复使用、本期流量从 0 开始） */
export function renewSub(sub: SubFixture, price: CatalogPrice, now: number) {
  if (isLiveSub(sub)) {
    sub.current_period_end = new Date(addInterval(new Date(sub.current_period_end).getTime(), price)).toISOString()
    if (sub.status === 'past_due' || sub.status === 'grace') sub.status = 'active'
  } else {
    const end = addInterval(now, price)
    const fresh = makeSub({ planId: sub.plan_id, priceId: price.id, status: 'active', usedGiB: 0, elapsedDays: 0, resetInDays: 30, expiresInDays: 30, online: 0, sources: 0 })
    Object.assign(sub, { status: 'active', current_period_start: new Date(now).toISOString(), current_period_end: new Date(end).toISOString(), days: fresh.days, resetAt: fresh.resetAt })
  }
  sub.price_id = price.id
  sub.amount = price.unit_amount
}

export function fulfill(state: PortalState, order: OrderFixture, via?: { provider: string; method: string }) {
  const now = Date.now()
  order.status = 'fulfilled'
  order.paid_at = new Date(now).toISOString()
  order.paid_amount = order.payable_amount
  if (via) {
    order.payProvider = via.provider
    order.payMethod = via.method
  }
  const e = order.effect
  if (e.type === 'addon') {
    const sub = state.subs.find((s) => s.id === e.subId)
    if (sub) sub.packBytes += e.bytes
    else state.unattachedBytes += e.bytes
  }
  if (e.type === 'topup') moveBalance(state, 'balance_topup', e.amount, `充值 ${order.order_no}`)
  if (e.type === 'new') {
    const plan = findPlan(e.planId)!
    const price = plan.prices.find((p) => p.id === e.priceId)!
    const sub = makeSub({ planId: plan.id, priceId: price.id, status: 'active', usedGiB: 0, elapsedDays: 0, resetInDays: 30, expiresInDays: 30, online: 0, sources: 0, label: freeLabel(state, e.label) })
    sub.current_period_end = new Date(addInterval(now, price)).toISOString()
    // 这是唯一一份生效中的、名下又有未分配的流量包：自动挂上（设计稿 2.4 provision，actor=system）
    if (!state.subs.some(isLiveSub) && state.unattachedBytes > 0) {
      sub.packBytes += state.unattachedBytes
      state.transfers.push({ from: null, to: sub.id, bytes: state.unattachedBytes, at: new Date(now).toISOString() })
      state.unattachedBytes = 0
    }
    state.subs.unshift(sub)
    order.subscription_id = sub.id
  }
  if (e.type === 'renewal') {
    const sub = state.subs.find((s) => s.id === e.subId)
    const plan = sub && findPlan(sub.plan_id)
    const price = plan?.prices.find((p) => p.id === e.priceId)
    if (sub && price) renewSub(sub, price, now)
  }
  if (e.type === 'upgrade') {
    const sub = state.subs.find((s) => s.id === e.subId)
    const plan = findPlan(e.planId)
    const price = plan?.prices.find((p) => p.id === e.priceId)
    if (sub && plan && price) swapPlan(sub, plan, price)
    if (e.refund > 0) moveBalance(state, 'plan_change_refund', e.refund, '换套餐退回')
  }
}

/** 变更套餐履约：原地换套餐、凭据不变，新周期从今天起，已用流量清零（修订 R36）；流量包仍挂在这一份上 */
export function swapPlan(sub: SubFixture, plan: CatalogPlan, price: CatalogPrice) {
  const now = Date.now()
  const fresh = makeSub({ planId: plan.id, priceId: price.id, status: 'active', usedGiB: 0, elapsedDays: 0, resetInDays: 30, expiresInDays: 30, online: sub.online, sources: 0 })
  Object.assign(sub, {
    plan_id: plan.id,
    price_id: price.id,
    plan_name: plan.name,
    status: 'active',
    amount: price.unit_amount,
    limitBytes: fresh.limitBytes,
    deviceLimit: fresh.deviceLimit,
    current_period_start: new Date(now).toISOString(),
    current_period_end: new Date(addInterval(now, price)).toISOString(),
    days: fresh.days,
    resetAt: fresh.resetAt,
  })
}

/** 余额变动一律记流水（契约门户-05 history 的 kind 取值） */
export function moveBalance(state: PortalState, kind: string, delta: number, memo: string) {
  state.balance += delta
  state.ledger.unshift({ kind, delta, memo, at: new Date().toISOString() })
}

/** 用户取消待支付单：退回冻结的余额（契约 POST v1/orders/{id}/cancel） */
export function cancelOrder(state: PortalState, order: OrderFixture) {
  order.status = 'cancelled'
  order.cancel_reason = 'user_cancelled'
  order.cancelled_at = new Date().toISOString()
  if (order.balance_applied > 0) moveBalance(state, 'balance_release', order.balance_applied, `订单 ${order.order_no} 取消`)
}

/** 读之前把超时的待支付单转为 expired，退回冻结的余额 */
export function sweepExpired(state: PortalState) {
  const now = Date.now()
  for (const o of state.orders) {
    if (o.status === 'pending_payment' && o.expires_at && new Date(o.expires_at).getTime() <= now) {
      o.status = 'expired'
      o.cancelled_at = o.expires_at
      if (o.balance_applied > 0) moveBalance(state, 'balance_release', o.balance_applied, `订单 ${o.order_no} 超时取消`)
    }
  }
}

/** 续费与变更互斥：同一订阅只能有一张在途单（修订 R37） */
export function assertNoOpenChange(state: PortalState, subId: string) {
  sweepExpired(state)
  const open = state.orders.some((o) => o.subscription_id === subId && (o.kind === 'renewal' || o.kind === 'upgrade') && o.status === 'pending_payment')
  if (open) throw new BillingError(409, 'conflict', '这条订阅还有未完成的续费或变更套餐订单，请先支付或取消')
}

// ---------------------------------------------------------------------------
// 订单读形状（契约门户-04，含修订 R32、R69）；legacy 场景去掉 Go 带 omitempty 的可缺席字段
// （周期、支付方式、优惠码、有效期至），无 omitempty 的 item_name / provider_name 恒在
// ---------------------------------------------------------------------------
const OPEN = new Set(['draft', 'pending_payment', 'processing'])

export function orderRow(o: OrderFixture) {
  const legacy = scenario() === 'legacy'
  return {
    id: o.id,
    order_no: o.order_no,
    kind: o.kind,
    status: o.status,
    currency: 'CNY',
    total_amount: o.total_amount,
    discount_amount: o.discount_amount,
    balance_applied: o.balance_applied,
    payable_amount: o.payable_amount,
    paid_amount: o.paid_amount,
    refunded_amount: o.refunded_amount ?? 0,
    ...(o.plan_name === undefined ? {} : { plan_name: o.plan_name }),
    cancellable: OPEN.has(o.status),
    // Go 无 omitempty，各场景恒在：发起过支付，或已经由渠道付清
    has_payment_intent: !!(o.hasIntent || o.payProvider),
    created_at: o.created_at,
    ...(o.paid_at ? { paid_at: o.paid_at } : {}),
    ...(o.cancelled_at ? { cancelled_at: o.cancelled_at } : {}),
    ...(o.cancel_reason ? { cancel_reason: o.cancel_reason } : {}),
    ...(o.expires_at ? { expires_at: o.expires_at } : {}),
    ...(legacy || o.interval === undefined ? {} : { interval: o.interval, interval_count: o.interval_count ?? 1 }),
    item_name: o.item_name ?? '',
  }
}

export function orderDetail(state: PortalState, o: OrderFixture) {
  const legacy = scenario() === 'legacy'
  const sub = o.subscription_id ? state.subs.find((s) => s.id === o.subscription_id) : undefined
  return {
    order: {
      ...orderRow(o),
      items: [{ name: o.item_name ?? o.plan_name ?? '余额充值', quantity: 1, unit_amount: o.subtotal, line_amount: o.subtotal }],
      payments:
        o.paid_at && o.payable_amount > 0
          ? [{ status: 'succeeded', amount: o.paid_amount, currency: 'CNY', created_at: o.paid_at, provider_name: o.payProvider === 'epay' ? '易支付' : (o.payProvider ?? ''), ...(legacy ? {} : { method: o.payMethod }) }]
          : [],
      ...(!legacy && o.coupon_code ? { coupon_code: o.coupon_code } : {}),
      ...(!legacy && o.status === 'fulfilled' && sub ? { subscription_period_end: sub.current_period_end } : {}),
    },
  }
}

