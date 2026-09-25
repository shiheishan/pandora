/**
 * [INPUT]: 依赖 node:crypto 的 randomUUID，依赖 ../types 的 AnonContext / Json / MockResult，依赖 ./catalog 的目录与优惠码，依赖 ./fixtures 的 PortalState / OrderFixture / makeSub / scenario
 * [OUTPUT]: 对外提供 BillingError、readStrict、isUuid、couponCheck、couponView、placeOrder、createdView、fulfill、moveBalance、cancelOrder、sweepExpired、assertNoOpenChange、changeQuote、orderRow、orderDetail
 * [POS]: dev/mock/portal 的计费逻辑（不是模块，不进登记表）：结账、订单、选购三个页面文件共用——请求体逐字段校验（后端 DisallowUnknownFields）、优惠码试算、下单时扣余额（记 balance_hold 流水）与 30 分钟过期或取消退回（balance_release）、履约（充值记 balance_topup）（新购开订阅、续费延期、变更原地换套餐并退余额、流量包加余量）、变更套餐折算（5.A D-E-2：剩余时间与剩余流量比取小）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomUUID } from 'node:crypto'
import type { AnonContext, Json, MockResult } from '../types.ts'
import { COUPONS, findPlan, intervalMonths, type CatalogPlan, type CatalogPrice } from './catalog.ts'
import { makeSub, scenario, type OrderEffect, type OrderFixture, type PortalState, type SubFixture } from './fixtures.ts'

const DAY_MS = 86_400_000
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

/** 修订 R69 的券面对象；legacy 场景不回 */
export function couponView(code: unknown) {
  if (scenario() === 'legacy' || typeof code !== 'string') return undefined
  const key = code.trim().toUpperCase()
  const c = COUPONS[key]
  return c ? { code: key, discount_type: c.type, discount_value: c.value } : null
}

// ---------------------------------------------------------------------------
// 下单：余额先扣（冻结），0 元当场履约
// ---------------------------------------------------------------------------
let orderSeq = 3400

export function placeOrder(
  state: PortalState,
  init: { kind: OrderFixture['kind']; subtotal: number; credit?: number; coupon: unknown; priceId: string | null; useBalance: unknown; planName?: string; itemName?: string; price?: CatalogPrice; subscriptionId?: string; effect: OrderEffect },
): OrderFixture {
  if (init.useBalance !== undefined && (typeof init.useBalance !== 'number' || !Number.isInteger(init.useBalance))) {
    throw new BillingError(422, 'validation_failed', '参数不合法', { use_balance: '须为整数（分）' })
  }
  const discount = couponCheck(init.coupon, init.subtotal, init.priceId)
  const total = Math.max(0, init.subtotal - discount - (init.credit ?? 0))
  const want = Math.max(0, (init.useBalance as number | undefined) ?? 0)
  const applied = Math.min(want, total)
  if (applied > state.balance) throw new BillingError(409, 'conflict', '余额不足')
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
    discount_amount: discount,
    balance_applied: applied,
    payable_amount: total - applied,
    paid_amount: 0,
    coupon_code: typeof init.coupon === 'string' && init.coupon.trim() ? init.coupon.trim().toUpperCase() : undefined,
    subscription_id: init.subscriptionId,
    created_at: new Date(now).toISOString(),
    expires_at: new Date(now + ORDER_TTL_MS).toISOString(),
    effect: init.effect,
  }
  state.orders.unshift(order)
  if (applied > 0) moveBalance(state, 'balance_hold', -applied, `订单 ${order.order_no}`)
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

function extend(iso: string, months: number): string {
  const d = new Date(iso)
  return new Date(d.getTime() + Math.round(months * 30) * DAY_MS).toISOString()
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
  if (e.type === 'addon') state.packBytes += e.bytes
  if (e.type === 'topup') moveBalance(state, 'balance_topup', e.amount, `充值 ${order.order_no}`)
  if (e.type === 'new') {
    const plan = findPlan(e.planId)!
    const price = plan.prices.find((p) => p.id === e.priceId)!
    const sub = makeSub({ planId: plan.id, priceId: price.id, status: 'active', usedGiB: 0, elapsedDays: 0, resetInDays: 30, expiresInDays: Math.round(intervalMonths(price) * 30), online: 0, sources: 0 })
    state.subs.unshift(sub)
    order.subscription_id = sub.id
  }
  if (e.type === 'renewal') {
    const sub = state.subs.find((s) => s.id === e.subId)
    const plan = sub && findPlan(sub.plan_id)
    const price = plan?.prices.find((p) => p.id === e.priceId)
    if (sub && price) {
      sub.current_period_end = extend(sub.current_period_end, intervalMonths(price))
      sub.price_id = price.id
      sub.amount = price.unit_amount
      if (sub.status === 'past_due' || sub.status === 'grace') sub.status = 'active'
    }
  }
  if (e.type === 'upgrade') {
    const sub = state.subs.find((s) => s.id === e.subId)
    const plan = findPlan(e.planId)
    const price = plan?.prices.find((p) => p.id === e.priceId)
    if (sub && plan && price) swapPlan(sub, plan, price)
    if (e.refund > 0) moveBalance(state, 'plan_change_refund', e.refund, '变更套餐差额退回')
  }
}

/** 变更套餐履约：原地换套餐、凭据不变，新周期从今天起，已用流量清零（修订 R36） */
function swapPlan(sub: SubFixture, plan: CatalogPlan, price: CatalogPrice) {
  const now = Date.now()
  const fresh = makeSub({ planId: plan.id, priceId: price.id, status: 'active', usedGiB: 0, elapsedDays: 0, resetInDays: 30, expiresInDays: Math.round(intervalMonths(price) * 30), online: sub.online, sources: 0 })
  Object.assign(sub, {
    plan_id: plan.id,
    price_id: price.id,
    plan_name: plan.name,
    status: 'active',
    amount: price.unit_amount,
    limitBytes: fresh.limitBytes,
    deviceLimit: fresh.deviceLimit,
    current_period_start: new Date(now).toISOString(),
    current_period_end: fresh.current_period_end,
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
// 变更套餐试算（5.A D-E-2 + 修订 R39）：剩余价值 = 实付 × min(剩余时间比, 剩余流量比)，向下取整
// ---------------------------------------------------------------------------
export function changeQuote(sub: SubFixture, plan: CatalogPlan, price: CatalogPrice, coupon: unknown) {
  if (plan.id === sub.plan_id) throw new BillingError(409, 'conflict', '与当前套餐相同，请使用续费')
  if (!plan.allow_upgrade) throw new BillingError(409, 'conflict', '目标套餐不允许变更')
  if (!['active', 'trialing', 'grace', 'past_due'].includes(sub.status)) throw new BillingError(409, 'conflict', '当前订阅状态不能变更')
  if (price.currency !== 'CNY') throw new BillingError(409, 'conflict', '变更套餐不能更换币种')
  const now = Date.now()
  const start = new Date(sub.current_period_start).getTime()
  const end = new Date(sub.current_period_end).getTime()
  const timeRatio = end > start ? Math.min(1, Math.max(0, (end - now) / (end - start))) : 0
  const used = sub.days.reduce((s, d) => s + d.bytes, 0)
  const trafficRatio = sub.limitBytes > 0 ? Math.max(0, 1 - used / sub.limitBytes) : 1
  const credit = Math.floor(sub.amount * Math.min(timeRatio, trafficRatio))
  const discount = couponCheck(coupon, price.unit_amount, price.id)
  const total = Math.max(0, price.unit_amount - credit - discount)
  const refund = Math.max(0, credit + discount - price.unit_amount)
  return {
    direction: total > 0 ? 'upgrade' : 'downgrade',
    currency: 'CNY',
    subtotal: price.unit_amount,
    proration_credit: credit,
    discount,
    total,
    balance_refund: refund,
    current_period_end: sub.current_period_end,
    new_period_start: new Date(now).toISOString(),
    new_period_end: new Date(now + Math.round(intervalMonths(price) * 30) * DAY_MS).toISOString(),
  }
}

// ---------------------------------------------------------------------------
// 订单读形状（契约门户-04，含修订 R32、R69）；legacy 场景去掉修订 R69 的字段
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
    created_at: o.created_at,
    ...(o.paid_at ? { paid_at: o.paid_at } : {}),
    ...(o.cancelled_at ? { cancelled_at: o.cancelled_at } : {}),
    ...(o.cancel_reason ? { cancel_reason: o.cancel_reason } : {}),
    ...(o.expires_at ? { expires_at: o.expires_at } : {}),
    ...(legacy
      ? {}
      : {
          ...(o.interval === undefined ? {} : { interval: o.interval, interval_count: o.interval_count ?? 1 }),
          ...(o.item_name === undefined ? {} : { item_name: o.item_name }),
        }),
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
          ? [{ status: 'succeeded', amount: o.paid_amount, currency: 'CNY', created_at: o.paid_at, ...(legacy ? {} : { method: o.payMethod, provider_name: o.payProvider === 'epay' ? '易支付' : o.payProvider }) }]
          : [],
      ...(!legacy && o.coupon_code ? { coupon_code: o.coupon_code } : {}),
      ...(!legacy && o.status === 'fulfilled' && sub ? { subscription_period_end: sub.current_period_end } : {}),
    },
  }
}

