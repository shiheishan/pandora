import { href } from '../../../core/router'
import type { Plan } from '../common/catalog'
import type { Landing } from '../common/landings'
import { createPlacedOrder } from '../common/intent'
import { isHeld } from '../common/purchase'
import { expectation, type BalanceSplit, type Quote, type QuoteRequest, type QuoteRow } from '../common/quote'
import { isLive, type Subscription } from '../common/subscriptions'

// ---------------------------------------------------------------------------
// 确认页的地址（.claude/rules/screens-portal.md 约定地址）：
//   ?renew=<一份>[&from=plans]              续费（过期 30 天内的是「恢复使用」）
//   ?change=<一份>&plan=<套餐>              把这一份换成 X
//   ?stack=<一份>                           一次性同款叠加到那一份
//   ?new=<套餐>                             另买一份
//   ?reset=<一份>  买流量重置（把这一份当期已用流量清零）
//   ?plan=<套餐>                            老地址：没有份 → 新买；有份 → 先选放到哪里（LandingChooser）
//   ?change-plan=<套餐>                     同 ?plan=，兼容老地址
//   以上都可带 &price=<价格> 预选「买多久」
// ---------------------------------------------------------------------------
export type Target =
  | { kind: 'renew'; subId: string; fromPlans: boolean }
  | { kind: 'change'; subId: string; planId: string }
  | { kind: 'new'; planId: string }
  | { kind: 'stack'; subId: string }
  | { kind: 'reset'; subId: string }
  | { kind: 'choose'; planId: string }

export type Problem = 'missing' | 'plan_gone' | 'sub_gone'
type Parsed = { target: Target } | { problem: Problem }

export function parseTarget(query: URLSearchParams, held: readonly Subscription[], plans: readonly Plan[]): Parsed {
  const reset = query.get('reset')
  if (reset) {
    const sub = held.find((s) => s.id === reset)
    return sub && isLive(sub) ? { target: { kind: 'reset', subId: sub.id } } : { problem: 'sub_gone' }
  }

  const renew = query.get('renew')
  if (renew) {
    const sub = held.find((s) => s.id === renew)
    return sub && sub.renew_until !== null ? { target: { kind: 'renew', subId: sub.id, fromPlans: query.get('from') === 'plans' } } : { problem: 'sub_gone' }
  }

  const stack = query.get('stack')
  if (stack) {
    const sub = held.find((s) => s.id === stack)
    return sub && sub.stackable ? { target: { kind: 'stack', subId: sub.id } } : { problem: 'sub_gone' }
  }

  const change = query.get('change')
  if (change) {
    const planId = query.get('plan') ?? ''
    if (!plans.some((p) => p.id === planId)) return { problem: 'plan_gone' }
    const sub = held.find((s) => s.id === change)
    return sub && sub.changeable && sub.plan_id !== planId ? { target: { kind: 'change', subId: sub.id, planId } } : { problem: 'sub_gone' }
  }

  const newPlan = query.get('new')
  if (newPlan) return plans.some((p) => p.id === newPlan) ? { target: { kind: 'new', planId: newPlan } } : { problem: 'plan_gone' }

  const legacyPlan = query.get('plan') ?? query.get('change-plan')
  if (legacyPlan) {
    if (!plans.some((p) => p.id === legacyPlan)) return { problem: 'plan_gone' }
    if (!held.length) return { target: { kind: 'new', planId: legacyPlan } }
    return { target: { kind: 'choose', planId: legacyPlan } }
  }

  return { problem: 'missing' }
}

/** 报价请求；choose 不报价，返回 null */
export function quoteRequest(t: Target, held: readonly Subscription[], coupon: string | null): QuoteRequest | null {
  if (t.kind === 'choose') return null
  const c = coupon ? { coupon_code: coupon } : {}
  switch (t.kind) {
    case 'renew':
      return { action: 'renew', subscription_id: t.subId, ...c }
    case 'change':
      return { action: 'change', subscription_id: t.subId, plan_id: t.planId, ...c }
    case 'new':
      return { action: 'new', plan_id: t.planId, new_copy: held.some(isHeld), ...c }
    case 'stack':
      return { action: 'stack', subscription_id: t.subId, ...c }
    case 'reset':
      return { action: 'reset', subscription_id: t.subId, ...c }
  }
}

export { defaultTier, sortTiers } from '../common/quote'

/** 从 landings 选项跳到确认页地址 */
export function landingHref(planId: string, l: Landing, price: string | null): string {
  const q: Record<string, string> = {}
  if (price) q.price = price
  switch (l.mode) {
    case 'new':
      return href('/checkout', { new: planId, ...q })
    case 'renew':
      return href('/checkout', { renew: l.subscription_id!, ...q })
    case 'stack':
      return href('/checkout', { stack: l.subscription_id!, ...q })
    case 'change':
      return href('/checkout', { change: l.subscription_id!, plan: planId, ...q })
  }
}

// ---------------------------------------------------------------------------
// 建单：四个接口各自的请求体，都带 as_of + expect（服务端重算比对，不符回 409 quote_changed）。
// 后端 DisallowUnknownFields：可选字段不用时不传。use_balance 传这一档报价里用掉的余额。
// ---------------------------------------------------------------------------
interface OrderInput {
  target: Target
  quote: Quote
  row: QuoteRow
  split: BalanceSplit
  coupon: string | null
  /** 另买一份时起的名字 */
  label?: string
  newCopy?: boolean
}

export function orderRequest(i: OrderInput): { path: string; body: Record<string, unknown> } {
  const extra: Record<string, unknown> = { ...expectation(i.quote, i.row, i.split) }
  if (i.split.applied > 0) extra.use_balance = i.split.applied
  if (i.coupon) extra.coupon_code = i.coupon
  const t = i.target
  switch (t.kind) {
    case 'choose':
      throw new Error('choose 目标不能建单')
    case 'stack':
      return { path: `v1/me/subscriptions/${encodeURIComponent(t.subId)}/stack`, body: { price_id: i.row.price_id, ...extra } }
    case 'reset':
      return { path: `v1/me/subscriptions/${encodeURIComponent(t.subId)}/traffic-reset`, body: extra }
    case 'renew':
      return { path: `v1/me/subscriptions/${encodeURIComponent(t.subId)}/renew`, body: { price_id: i.row.price_id, ...extra } }
    case 'change':
      return { path: `v1/me/subscriptions/${encodeURIComponent(t.subId)}/change-plan`, body: { plan_id: t.planId, price_id: i.row.price_id, ...extra } }
    default: {
      const label = i.label?.trim()
      return { path: 'v1/orders', body: { plan_id: t.planId, price_id: i.row.price_id, ...(i.newCopy ? { new_copy: true } : {}), ...(label ? { label } : {}), ...extra } }
    }
  }
}

/** 刚下的待支付单（跨页面记住）：付款页点「换个付款方式」退回来再点，重开这张单的付款，不下第二张 */
export const placedOrders = createPlacedOrder<{ orderId: string }>()

/** 同一个意图的指纹不含报价时刻与 expect：重新报价后再点仍是同一张单 */
export function intentOf(req: { path: string; body: Record<string, unknown> }) {
  return { path: req.path, body: Object.fromEntries(Object.entries(req.body).filter(([k]) => k !== 'as_of' && k !== 'expect')) }
}
