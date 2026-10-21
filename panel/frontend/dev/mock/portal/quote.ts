import type { Json, MockModule } from '../types.ts'
import { assertNoPendingReset, BillingError, couponCheck, couponFace, isUuid, readStrict } from './billing.ts'
import { findPlan, MIN_PAYMENT, plansNow, type CatalogPlan, type CatalogPrice } from './catalog.ts'
import { addInterval, gate, isDead, isLiveSub, isRevivable, portalState, subPermanent, usedBytes, type PortalState, type SubFixture } from './fixtures.ts'
import { applyBalance, waiveSmallDue, type Balance } from './purchase.ts'
// ---------------------------------------------------------------------------
// 统一报价：POST /v1/me/checkout/quote 与建单接口共用。
// total = max(0, subtotal − discount − credit)；剩余价值 credit：订阅套餐 = 本期实付 × 剩余付费时间比；
// 一次性 = 本期实付 × min(剩余时间比, 剩余流量比)，永久的只看流量比；向下取整；过期 30 天内的那份为 0。建单带 as_of + expect 按 as_of 重算比对，不符 409 quote_changed。
// ---------------------------------------------------------------------------
const DAY_MS = 86_400_000
/** as_of 只接受 [now-10min, now]（允许 5 秒时钟误差） */
const AS_OF_WINDOW_MS = 10 * 60_000
const MAX_ROWS = 20

export interface CreditDetail {
  paid: number
  days_left: number
  days_total: number
  traffic_left: number
  traffic_total: number
  ratio_ppm: number
}

export interface QuoteRow {
  subscription_id: string | null
  plan_id: string | null
  price_id: string | null
  interval: string
  interval_count: number
  validity_days: number | null
  subtotal: number
  discount: number
  coupon: { code: string } | null
  coupon_error: string | null
  credit: number
  credit_detail: CreditDetail | null
  total: number
  refund: number
  with_balance: Balance
  without_balance: Balance
  period_start: string | null
  period_end: string | null
  previous_end: string | null
}

export function creditOf(sub: SubFixture, at: number): { credit: number; detail: CreditDetail } {
  // 口径照 Go order/service/proration.go：订阅套餐只按剩余付费时间算，不看流量（明细的流量两项为 0）；
  // 一次性取剩余时间比与剩余流量比里小的，永久的时间比视为 1、天数为 0/0；流量上限不含送的；
  // 天数向下取整，ratio_ppm 与钱都向下截断。mock 不记送的天数，付费时长 = 周期起止。
  const plan = findPlan(sub.plan_id)
  const start = new Date(sub.current_period_start).getTime()
  const end = new Date(sub.current_period_end).getTime()
  const live = isLiveSub(sub)
  const once = plan?.kind === 'one_time'
  const permanent = once && subPermanent(sub)
  const span = Math.max(0, end - start)
  const remaining = live ? Math.min(span - (at - start), end - at, span) : 0
  const cap = sub.limitBytes
  const trafficTotal = once && cap !== null ? Math.max(cap, 0) : 0
  const trafficLeft = once && cap !== null ? Math.max(Math.min(cap - usedBytes(sub), cap), 0) : 0
  const detail: CreditDetail = {
    paid: sub.amount,
    days_left: permanent ? 0 : Math.floor(Math.max(0, remaining) / DAY_MS),
    days_total: permanent ? 0 : Math.floor(span / DAY_MS),
    traffic_left: trafficLeft,
    traffic_total: trafficTotal,
    ratio_ppm: 0,
  }
  if (!live || sub.amount <= 0) return { credit: 0, detail }
  let ratio: number
  if (permanent) ratio = 1
  else if (span <= 0 || remaining <= 0) return { credit: 0, detail }
  else ratio = remaining / span
  if (once && cap !== null) ratio = cap <= 0 ? 0 : Math.min(ratio, trafficLeft / cap)
  detail.ratio_ppm = Math.floor(ratio * 1_000_000)
  return { credit: Math.floor(sub.amount * ratio), detail }
}

function periodEndMs(start: number, plan: CatalogPlan, price: CatalogPrice): number {
  if (plan.kind === 'one_time' && price.validity_days != null) return start + price.validity_days * DAY_MS
  return addInterval(start, price)
}

function stackEndMs(sub: SubFixture, price: CatalogPrice, at: number): number {
  if (subPermanent(sub)) return new Date(sub.current_period_end).getTime()
  if (price.validity_days === null) return addInterval(at, price)
  const base = Math.max(new Date(sub.current_period_end).getTime(), at)
  return base + price.validity_days * DAY_MS
}

interface RowSpec {
  sub: SubFixture | null
  plan: CatalogPlan | null
  price: CatalogPrice | null
  start: number | null
  endMs: number | null
  credit: { credit: number; detail: CreditDetail } | null
  /** 流量重置：不收券 */
  noDiscount?: boolean
}

/** strictCoupon：建单时优惠码不能用就整单拒绝；报价时写进 coupon_error、不让报价失败 */
function row(state: PortalState, spec: RowSpec, coupon: unknown, strictCoupon: boolean, subtotalOverride?: number): QuoteRow {
  const subtotal = subtotalOverride !== undefined ? subtotalOverride : spec.price!.unit_amount
  let discount = 0
  let couponError: string | null = null
  if (spec.noDiscount) {
    if (coupon !== undefined && coupon !== null && coupon !== '') {
      if (strictCoupon) throw new BillingError(422, 'validation_failed', '流量重置不能使用优惠码')
      couponError = '流量重置不能使用优惠码'
    }
  } else {
    try {
      discount = couponCheck(coupon, subtotal, spec.price?.id ?? null)
    } catch (e) {
      if (strictCoupon || !(e instanceof BillingError)) throw e
      couponError = e.message
    }
  }
  const credit = spec.credit?.credit ?? 0
  const total = Math.max(0, subtotal - discount - credit)
  const refund = Math.max(0, credit - (subtotal - discount))
  const validityDays = spec.plan?.kind === 'one_time' ? (spec.price?.validity_days ?? null) : null
  let periodEnd: string | null = null
  if (spec.endMs !== null) periodEnd = new Date(spec.endMs).toISOString()
  else if (spec.start !== null && spec.plan && spec.price) periodEnd = new Date(periodEndMs(spec.start, spec.plan, spec.price)).toISOString()
  return {
    subscription_id: spec.sub?.id ?? null,
    plan_id: spec.plan?.id ?? null,
    price_id: spec.price?.id ?? null,
    interval: spec.price?.billing_interval ?? '',
    interval_count: spec.price?.interval_count ?? 0,
    validity_days: validityDays,
    subtotal,
    discount,
    coupon: discount > 0 ? { code: couponFace(coupon)!.code } : null,
    coupon_error: couponError,
    credit,
    credit_detail: spec.credit?.detail ?? null,
    total,
    refund,
    with_balance: waiveSmallDue(applyBalance(total, state.balance, state.balance, MIN_PAYMENT), spec.credit !== null),
    without_balance: waiveSmallDue(applyBalance(total, state.balance, 0, MIN_PAYMENT), spec.credit !== null),
    period_start: spec.start === null ? null : new Date(spec.start).toISOString(),
    period_end: periodEnd,
    previous_end: spec.sub ? spec.sub.current_period_end : null,
  }
}

function ownedSub(state: PortalState, id: unknown): SubFixture {
  const sub = typeof id === 'string' ? state.subs.find((s) => s.id === id) : undefined
  if (!sub) throw new BillingError(404, 'not_found', '订阅不存在')
  return sub
}

function assertSubActive(sub: SubFixture, at: number) {
  if (!isLiveSub(sub) && !isRevivable(sub, at)) {
    throw new BillingError(409, 'conflict', '订阅已过期太久，不能再续费')
  }
}

function changeCheck(sub: SubFixture, plan: CatalogPlan, at: number) {
  if (plan.id === sub.plan_id) throw new BillingError(409, 'conflict', '与当前套餐相同，请使用续费')
  if (!plan.allowUpgrade) throw new BillingError(409, 'conflict', '目标套餐不允许变更')
  if (!isLiveSub(sub) && !isRevivable(sub, at)) throw new BillingError(409, 'conflict', '当前订阅状态不能变更')
}

const changeSpec = (sub: SubFixture, plan: CatalogPlan, price: CatalogPrice, at: number): RowSpec => ({
  sub,
  plan,
  price,
  start: at,
  endMs: null,
  credit: creditOf(sub, at),
})

export interface QuoteInput {
  action: unknown
  subscription_id?: unknown
  plan_id?: unknown
  coupon_code?: unknown
  new_copy?: unknown
}

/** 按 action 展开报价行；strictCoupon 见 row */
export function quoteRows(state: PortalState, req: QuoteInput, at: number, strictCoupon = false): QuoteRow[] {
  const coupon = req.coupon_code
  switch (req.action) {
    case 'renew': {
      const sub = ownedSub(state, req.subscription_id)
      assertSubActive(sub, at)
      const plan = findPlan(sub.plan_id)
      if (!plan) throw new BillingError(404, 'not_found', '套餐不存在')
      if (plan.kind === 'one_time') throw new BillingError(409, 'conflict', '目标套餐不允许变更')
      if (!plan.allowRenewal) throw new BillingError(409, 'conflict', '该套餐当前不允许续费')
      const start = isLiveSub(sub) ? new Date(sub.current_period_end).getTime() : at
      return plan.prices.map((price) => row(state, { sub, plan, price, start, endMs: null, credit: null }, coupon, strictCoupon))
    }
    case 'stack': {
      const sub = ownedSub(state, req.subscription_id)
      assertSubActive(sub, at)
      const plan = findPlan(sub.plan_id)
      if (!plan || plan.kind !== 'one_time') throw new BillingError(409, 'conflict', '只有一次性套餐可以叠加')
      if (!plan.allowNew) throw new BillingError(409, 'conflict', '该套餐当前不接受新购')
      const start = new Date(sub.current_period_start).getTime()
      return plan.prices.map((price) =>
        row(
          state,
          {
            sub,
            plan,
            price,
            start,
            endMs: stackEndMs(sub, price, at),
            credit: null,
          },
          coupon,
          strictCoupon,
        ),
      )
    }
    case 'change': {
      const sub = req.subscription_id === undefined ? null : ownedSub(state, req.subscription_id)
      const plan = req.plan_id === undefined ? null : findPlan(req.plan_id)
      if (req.plan_id !== undefined && !plan) throw new BillingError(404, 'not_found', '套餐不存在')
      if (sub && plan) {
        changeCheck(sub, plan, at)
        return plan.prices.map((price) => row(state, changeSpec(sub, plan, price, at), coupon, strictCoupon))
      }
      if (sub) {
        if (!isLiveSub(sub) && !isRevivable(sub, at)) throw new BillingError(409, 'conflict', '当前订阅状态不能变更')
        return plansNow()
          .filter((p) => p.id !== sub.plan_id && p.allowUpgrade && p.prices.length > 0)
          .slice(0, MAX_ROWS)
          .flatMap((p) => p.prices.map((price) => row(state, changeSpec(sub, p, price, at), coupon, strictCoupon)))
      }
      if (plan) {
        if (!plan.allowUpgrade) throw new BillingError(409, 'conflict', '目标套餐不允许变更')
        return state.subs
          .filter((s) => s.plan_id !== plan.id && (isLiveSub(s) || isRevivable(s, at)))
          .slice(0, MAX_ROWS)
          .flatMap((s) => plan.prices.map((price) => row(state, changeSpec(s, plan, price, at), coupon, strictCoupon)))
      }
      throw new BillingError(422, 'validation_failed', '参数不合法', { plan_id: '订阅与套餐至少给一个' })
    }
    case 'new': {
      if (!isUuid(req.plan_id)) throw new BillingError(422, 'validation_failed', '参数不合法', { plan_id: '必填' })
      const plan = findPlan(req.plan_id)
      if (!plan) throw new BillingError(404, 'not_found', '套餐不存在')
      if (!plan.allowNew) throw new BillingError(409, 'conflict', '该套餐当前不接受新购')
      const newCopy = req.new_copy === true
      if (!newCopy) {
        const same = state.subs.filter((s) => s.plan_id === plan.id && (isLiveSub(s) || isRevivable(s, at)))
        if (same.length > 0) {
          const msg =
            plan.kind === 'one_time'
              ? '你已有这个一次性套餐，可以叠加到原来那一份，或选择另开一份'
              : '你已有这个套餐的订阅，请在原订阅上续费，订阅链接不变'
          throw new BillingError(409, 'conflict', msg)
        }
      }
      return plan.prices.map((price) => row(state, { sub: null, plan, price, start: at, endMs: null, credit: null }, coupon, strictCoupon))
    }
    case 'reset': {
      const sub = ownedSub(state, req.subscription_id)
      const plan = findPlan(sub.plan_id)
      if (!plan || plan.reset === null || sub.limitBytes === null || sub.limitBytes <= 0 || !isLiveSub(sub)) {
        return []
      }
      if (plan.reset.limit !== null && sub.resetsUsed >= plan.reset.limit) {
        throw new BillingError(409, 'conflict', '本期的流量重置次数已用完')
      }
      assertNoPendingReset(state, sub.id)
      const subtotal = plan.reset.price
      return [row(state, { sub, plan, price: null, start: null, endMs: null, credit: null, noDiscount: true }, coupon, strictCoupon, subtotal)]
    }
    default:
      throw new BillingError(422, 'validation_failed', '参数不合法', { action: '只能是 new、renew、stack、change 或 reset' })
  }
}

export const QUOTE_CHANGED = () => new BillingError(409, 'quote_changed', '金额刚变了，请再确认一次')

export function quoteTime(body: Json, now = Date.now()): number {
  if (body.as_of === undefined) return now
  const at = typeof body.as_of === 'string' ? Date.parse(body.as_of) : Number.NaN
  if (Number.isNaN(at)) throw new BillingError(422, 'validation_failed', '参数不合法', { as_of: '须为 RFC3339 时间' })
  if (at < now - AS_OF_WINDOW_MS || at > now + 5000) throw QUOTE_CHANGED()
  return at
}

export interface Settled {
  row: QuoteRow
  balance: Balance
}

/** 选出与价格档对应的那一行、套上余额，并与 expect 比对 */
export function settle(state: PortalState, rows: QuoteRow[], priceId: string | null, body: Json): Settled {
  const hit = priceId === null ? rows[0] : rows.find((r) => r.price_id === priceId)
  if (!hit) throw new BillingError(409, 'conflict', '所选价格已下架，请重新选择')
  const want = body.use_balance
  if (want !== undefined && (typeof want !== 'number' || !Number.isInteger(want))) throw new BillingError(422, 'validation_failed', '参数不合法', { use_balance: '须为整数（分）' })
  const balance = waiveSmallDue(applyBalance(hit.total, state.balance, (want as number | undefined) ?? 0, MIN_PAYMENT), hit.credit_detail !== null)
  if (balance.below_minimum) throw new BillingError(422, 'validation_failed', '应付金额低于支付渠道的最低付款额，请先充值或使用余额支付')
  const expect = body.expect as Json | undefined
  if (expect !== undefined) {
    if (!expect || typeof expect !== 'object') throw new BillingError(422, 'validation_failed', '参数不合法', { expect: '须为对象' })
    if (expect.total !== hit.total || expect.balance_applied !== balance.applied || expect.payable !== balance.payable) throw QUOTE_CHANGED()
  }
  return { row: hit, balance }
}

/** 同一套餐同时只能有一张未付款的新购单（防两个标签页各付一次）；报价与建单都拦（Go ensureNoPendingNewOrder）。过了付款期限还没关掉的那张也拦，另带 lapsed=true，门户只给「取消它」 */
export function assertNoPendingNew(state: PortalState, planId: string, planName: string) {
  const open = state.orders.find((o) => o.kind === 'new' && o.status === 'pending_payment' && o.effect.type === 'new' && o.effect.planId === planId)
  if (!open) return
  const lapsed = open.expires_at !== undefined && new Date(open.expires_at).getTime() <= Date.now()
  const msg = lapsed ? `你有一张已超过付款期限的「${planName}」订单，取消后再买` : `你有一张还没付款的「${planName}」订单，继续付款或取消后再买`
  throw new BillingError(409, 'order_pending', msg, lapsed ? { order_id: open.id, lapsed: 'true' } : { order_id: open.id })
}

function hasPendingChange(state: PortalState, subId: string): boolean {
  return state.orders.some(
    (o) =>
      o.subscription_id === subId &&
      o.status === 'pending_payment' &&
      (o.kind === 'renewal' || o.kind === 'stack' || o.kind === 'upgrade'),
  )
}

function landingCandidates(state: PortalState, at: number): SubFixture[] {
  const live = state.subs.filter((s) => !isDead(s, at))
  live.sort((a, b) => {
    const al = isLiveSub(a) ? 1 : 0
    const bl = isLiveSub(b) ? 1 : 0
    if (al !== bl) return bl - al
    return new Date(b.current_period_end).getTime() - new Date(a.current_period_end).getTime()
  })
  return live.filter((s) => !hasPendingChange(state, s.id)).slice(0, MAX_ROWS)
}

export const quote: MockModule = {
  routes: {
    'GET /v1/me/checkout/landings': async (ctx) => {
      if (!(await gate(ctx))) return
      const planId = ctx.query.get('plan_id')
      if (!planId || !isUuid(planId)) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { plan_id: '请选择套餐' })
      const plan = findPlan(planId)
      if (!plan) return ctx.fail(404, 'not_found', '套餐不存在')
      if (!plan.allowNew) return ctx.fail(409, 'conflict', '该套餐当前不接受新购')
      const state = portalState(ctx.user.userId)
      const at = Date.now()
      const subs = landingCandidates(state, at)
      const options: Array<{ mode: string; subscription_id?: string }> = [{ mode: 'new' }]
      for (const sub of subs) {
        if (sub.plan_id === plan.id) {
          if (plan.kind === 'one_time') options.push({ mode: 'stack', subscription_id: sub.id })
          else if (plan.allowRenewal) options.push({ mode: 'renew', subscription_id: sub.id })
        }
      }
      for (const sub of subs) {
        if (sub.plan_id !== plan.id && plan.allowUpgrade) options.push({ mode: 'change', subscription_id: sub.id })
      }
      ctx.send(200, { options })
    },

    'POST /v1/me/checkout/quote': async (ctx) => {
      if (!(await gate(ctx))) return
      const body = await readStrict(ctx, ['action', 'subscription_id', 'plan_id', 'coupon_code', 'new_copy'])
      if (!body) return
      const state = portalState(ctx.user.userId)
      const at = Date.now()
      try {
        const req = body as unknown as QuoteInput
        const plan = req.action === 'new' && isUuid(req.plan_id) ? findPlan(req.plan_id) : undefined
        if (plan) assertNoPendingNew(state, plan.id, plan.name)
        const quotes = quoteRows(state, req, at)
        ctx.send(200, { as_of: new Date(at).toISOString(), balance: state.balance, min_payment: MIN_PAYMENT, quotes })
      } catch (e) {
        if (!(e instanceof BillingError)) throw e
        ctx.fail(e.status, e.code, e.message, e.fields)
      }
    },
  },
}
