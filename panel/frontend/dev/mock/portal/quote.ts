import type { Json, MockModule } from '../types.ts'
import { BillingError, couponCheck, couponFace, isUuid, readStrict } from './billing.ts'
import { findPack, findPlan, MIN_PAYMENT, plansNow, type CatalogPack, type CatalogPlan, type CatalogPrice } from './catalog.ts'
import { addInterval, gate, isLiveSub, isRevivable, portalState, subPrice, usedBytes, type PortalState, type SubFixture } from './fixtures.ts'
import { applyBalance, type Balance } from './purchase.ts'

// ---------------------------------------------------------------------------
// 统一报价（设计稿 2.2）：POST /v1/me/checkout/quote 与四个建单接口共用这里的算法。
// 剩余价值 = 本期实付 × min(剩余时间比, 剩余流量比)，向下取整；过期 30 天内的那份为 0。
// 建单带 as_of + expect 时按 as_of 重算、比对，不符回 409 quote_changed。
// ---------------------------------------------------------------------------
const DAY_MS = 86_400_000
/** as_of 只接受 [now-10min, now] */
const AS_OF_WINDOW_MS = 10 * 60_000
const MAX_ROWS = 20

export type QuoteAction = 'renew' | 'change' | 'new' | 'pack'

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
  pack_id: string | null
  price_id: string | null
  interval: string
  interval_count: number
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
  const start = new Date(sub.current_period_start).getTime()
  const end = new Date(sub.current_period_end).getTime()
  const live = isLiveSub(sub)
  const timeRatio = live && end > start ? Math.min(1, Math.max(0, (end - at) / (end - start))) : 0
  const used = usedBytes(sub)
  const trafficLeft = Math.max(0, sub.limitBytes - used)
  const trafficRatio = !live ? 0 : sub.limitBytes > 0 ? trafficLeft / sub.limitBytes : 1
  const ratio = Math.min(timeRatio, trafficRatio)
  return {
    credit: Math.floor(sub.amount * ratio),
    detail: {
      paid: sub.amount,
      days_left: live ? Math.max(0, Math.ceil((end - at) / DAY_MS)) : 0,
      days_total: Math.max(1, Math.round((end - start) / DAY_MS)),
      traffic_left: trafficLeft,
      traffic_total: sub.limitBytes,
      ratio_ppm: Math.round(ratio * 1_000_000),
    },
  }
}

interface RowSpec {
  sub: SubFixture | null
  plan: CatalogPlan | null
  price: CatalogPrice | null
  pack: CatalogPack | null
  /** 新周期起点；null = 没有周期（流量包） */
  start: number | null
  credit: { credit: number; detail: CreditDetail } | null
}

/** strictCoupon：建单时优惠码不能用就整单拒绝；报价时写进 coupon_error、不让报价失败 */
function row(state: PortalState, spec: RowSpec, coupon: unknown, strictCoupon: boolean): QuoteRow {
  const subtotal = spec.pack ? spec.pack.unit_amount : spec.price!.unit_amount
  let discount = 0
  let couponError: string | null = null
  try {
    discount = couponCheck(coupon, subtotal, spec.price?.id ?? null)
  } catch (e) {
    if (strictCoupon || !(e instanceof BillingError)) throw e
    couponError = e.message
  }
  const credit = spec.credit?.credit ?? 0
  const total = Math.max(0, subtotal - discount - credit)
  const refund = Math.max(0, credit - (subtotal - discount))
  return {
    subscription_id: spec.sub?.id ?? null,
    plan_id: spec.plan?.id ?? null,
    pack_id: spec.pack?.id ?? null,
    price_id: spec.price?.id ?? null,
    interval: spec.price?.billing_interval ?? '',
    interval_count: spec.price?.interval_count ?? 0,
    subtotal,
    discount,
    coupon: discount > 0 ? { code: couponFace(coupon)!.code } : null,
    coupon_error: couponError,
    credit,
    credit_detail: spec.credit?.detail ?? null,
    total,
    refund,
    with_balance: applyBalance(total, state.balance, state.balance, MIN_PAYMENT),
    without_balance: applyBalance(total, state.balance, 0, MIN_PAYMENT),
    period_start: spec.start === null ? null : new Date(spec.start).toISOString(),
    period_end: spec.start === null || !spec.price ? null : new Date(addInterval(spec.start, spec.price)).toISOString(),
    previous_end: spec.sub ? spec.sub.current_period_end : null,
  }
}

const cnyPrices = (plan: CatalogPlan) => plan.prices.filter((p) => p.currency === 'CNY')
/** 换套餐按订阅现在的周期取同档，没有同档取第一档 */
const sameTier = (plan: CatalogPlan, sub: SubFixture) => {
  const cur = subPrice(sub)
  return cnyPrices(plan).find((p) => p.billing_interval === cur.billing_interval && p.interval_count === cur.interval_count) ?? cnyPrices(plan)[0]
}

function ownedSub(state: PortalState, id: unknown): SubFixture {
  const sub = typeof id === 'string' ? state.subs.find((s) => s.id === id) : undefined
  if (!sub) throw new BillingError(404, 'not_found', '订阅不存在')
  return sub
}

function changeCheck(sub: SubFixture, plan: CatalogPlan, now: number) {
  if (plan.id === sub.plan_id) throw new BillingError(409, 'conflict', '与当前套餐相同，请使用续费')
  if (!plan.allow_upgrade) throw new BillingError(409, 'conflict', '目标套餐不允许变更')
  if (!isLiveSub(sub) && !isRevivable(sub, now)) throw new BillingError(409, 'conflict', '当前订阅状态不能变更')
}

const changeSpec = (sub: SubFixture, plan: CatalogPlan, price: CatalogPrice, at: number): RowSpec => ({ sub, plan, price, pack: null, start: at, credit: creditOf(sub, at) })

export interface QuoteInput {
  action: unknown
  subscription_id?: unknown
  plan_id?: unknown
  pack_id?: unknown
  coupon_code?: unknown
  new_copy?: unknown
}

/** 按 action 展开报价行；strictCoupon 见 row */
export function quoteRows(state: PortalState, req: QuoteInput, at: number, strictCoupon = false): QuoteRow[] {
  const coupon = req.coupon_code
  switch (req.action) {
    case 'renew': {
      const sub = ownedSub(state, req.subscription_id)
      const plan = findPlan(sub.plan_id)
      if (!isLiveSub(sub) && !isRevivable(sub, at)) throw new BillingError(409, 'conflict', '这条订阅当前不能续费')
      if (!plan?.allow_renewal) throw new BillingError(409, 'conflict', '该套餐当前不允许续费')
      const start = isLiveSub(sub) ? new Date(sub.current_period_end).getTime() : at
      return cnyPrices(plan).map((price) => row(state, { sub, plan, price, pack: null, start, credit: null }, coupon, strictCoupon))
    }
    case 'change': {
      const sub = req.subscription_id === undefined ? null : ownedSub(state, req.subscription_id)
      const plan = req.plan_id === undefined ? null : findPlan(req.plan_id)
      if (req.plan_id !== undefined && !plan) throw new BillingError(404, 'not_found', '套餐不存在')
      if (sub && plan) {
        changeCheck(sub, plan, at)
        return cnyPrices(plan).map((price) => row(state, changeSpec(sub, plan, price, at), coupon, strictCoupon))
      }
      if (sub) {
        if (!isLiveSub(sub) && !isRevivable(sub, at)) throw new BillingError(409, 'conflict', '当前订阅状态不能变更')
        return plansNow()
          .filter((p) => p.id !== sub.plan_id && p.allow_upgrade && cnyPrices(p).length > 0)
          .slice(0, MAX_ROWS)
          .map((p) => row(state, changeSpec(sub, p, sameTier(p, sub)!, at), coupon, strictCoupon))
      }
      if (plan) {
        if (!plan.allow_upgrade) throw new BillingError(409, 'conflict', '目标套餐不允许变更')
        return state.subs
          .filter((s) => s.plan_id !== plan.id && (isLiveSub(s) || isRevivable(s, at)))
          .slice(0, MAX_ROWS)
          .map((s) => row(state, changeSpec(s, plan, sameTier(plan, s)!, at), coupon, strictCoupon))
      }
      throw new BillingError(422, 'validation_failed', '参数不合法', { plan_id: '订阅与套餐至少给一个' })
    }
    case 'new': {
      if (!isUuid(req.plan_id)) throw new BillingError(422, 'validation_failed', '参数不合法', { plan_id: '必填' })
      const plan = findPlan(req.plan_id)
      if (!plan) throw new BillingError(404, 'not_found', '套餐不存在')
      return cnyPrices(plan).map((price) => row(state, { sub: null, plan, price, pack: null, start: at, credit: null }, coupon, strictCoupon))
    }
    case 'pack': {
      const pack = findPack(req.pack_id)
      if (!pack) throw new BillingError(404, 'not_found', '流量包不存在或已下架')
      const sub = req.subscription_id === undefined ? null : ownedSub(state, req.subscription_id)
      return [row(state, { sub, plan: null, price: null, pack, start: null, credit: null }, coupon, strictCoupon)]
    }
    default:
      throw new BillingError(422, 'validation_failed', '参数不合法', { action: '只能是 renew / change / new / pack' })
  }
}

// ---------------------------------------------------------------------------
// 建单时的比对：as_of 窗口、按 as_of 重算、余额经 ApplyBalance、与 expect 逐项比
// ---------------------------------------------------------------------------
export const QUOTE_CHANGED = () => new BillingError(409, 'quote_changed', '金额刚变了，请再确认一次')

export function quoteTime(body: Json, now = Date.now()): number {
  if (body.as_of === undefined) return now
  const at = typeof body.as_of === 'string' ? Date.parse(body.as_of) : Number.NaN
  if (Number.isNaN(at)) throw new BillingError(422, 'validation_failed', '参数不合法', { as_of: '须为 RFC3339 时间' })
  // 允许 5 秒时钟误差
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
  const balance = applyBalance(hit.total, state.balance, (want as number | undefined) ?? 0, MIN_PAYMENT)
  const expect = body.expect as Json | undefined
  if (expect !== undefined) {
    if (!expect || typeof expect !== 'object') throw new BillingError(422, 'validation_failed', '参数不合法', { expect: '须为对象' })
    if (expect.total !== hit.total || expect.balance_applied !== balance.applied || expect.payable !== balance.payable) throw QUOTE_CHANGED()
  }
  return { row: hit, balance }
}


export const quote: MockModule = {
  routes: {
    // 只读、不幂等；挂 checkout 开关（假后端恒开）
    'POST /v1/me/checkout/quote': async (ctx) => {
      if (!(await gate(ctx))) return
      const body = await readStrict(ctx, ['action', 'subscription_id', 'plan_id', 'pack_id', 'coupon_code', 'new_copy'])
      if (!body) return
      const state = portalState(ctx.user.userId)
      const at = Date.now()
      try {
        const quotes = quoteRows(state, body as unknown as QuoteInput, at)
        ctx.send(200, { as_of: new Date(at).toISOString(), currency: 'CNY', balance: state.balance, min_payment: MIN_PAYMENT, quotes })
      } catch (e) {
        if (!(e instanceof BillingError)) throw e
        ctx.fail(e.status, e.code, e.message, e.fields)
      }
    },
  },
}
