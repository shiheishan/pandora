import { periodMonths, periodOf, type Pack, type Plan } from '../common/catalog'
import { createPlacedOrder } from '../common/intent'
import { isHeld, leftOf } from '../common/purchase'
import { balanceSplit, expectation, type BalanceSplit, type Quote, type QuoteRequest, type QuoteRow } from '../common/quote'
import { isLive, type Subscription } from '../common/subscriptions'
import { renewOrder } from '../plans/labels'

// ---------------------------------------------------------------------------
// 确认页的地址（.claude/rules/screens-portal.md 约定地址）：
//   ?renew=<一份>[&from=plans]              续费（过期 30 天内的是「恢复使用」）
//   ?change=<一份>&plan=<套餐>              把这一份换成 X
//   ?change-plan=<套餐>[&sub=<一份>]        先选换掉哪一份（不预选；能换的只有一份时直接定）
//   ?new=<套餐>                             另买一份
//   ?pack=<流量包>&sub=<一份>               加流量（没带 sub 且多份时先选加到哪一份，选过带 pick=1）
//   ?plan=<套餐>                            老地址：没有套餐 → 新买；有同款 → 续费；否则 → 选换掉哪一份
//   以上都可带 &price=<价格> 预选「买多久」
// ---------------------------------------------------------------------------
export type Target =
  | { kind: 'renew'; subId: string; fromPlans: boolean }
  | { kind: 'change'; subId: string | null; planId: string; chooser: boolean }
  | { kind: 'new'; planId: string }
  | { kind: 'pack'; packId: string; subId: string | null; chooser: boolean }

export type Problem = 'missing' | 'plan_gone' | 'pack_gone' | 'sub_gone'
export type Parsed = { target: Target } | { problem: Problem }

/** 能换成 plan 的份：手上的、能付费换套餐的、不是同款的 */
export const changeCandidates = (held: readonly Subscription[], planId: string) => held.filter((s) => s.changeable && s.plan_id !== planId)

function changeOrRenew(planId: string, held: readonly Subscription[], picked: string | null): Target | Problem {
  const holders = renewOrder(held.filter((s) => s.plan_id === planId && s.renew_until !== null))
  const cands = changeCandidates(held, planId)
  if (!cands.length) return holders[0] ? { kind: 'renew', subId: holders[0].id, fromPlans: true } : held.length ? 'sub_gone' : { kind: 'new', planId }
  // 只有一份能换时直接定，不问；多份时不预选（换掉生效中的那份永不作默认）
  if (cands.length === 1) return { kind: 'change', subId: cands[0]!.id, planId, chooser: false }
  return { kind: 'change', subId: cands.some((s) => s.id === picked) ? picked : null, planId, chooser: true }
}

export function parseTarget(query: URLSearchParams, held: readonly Subscription[], plans: readonly Plan[], packs: readonly Pack[]): Parsed {
  const wrap = (t: Target | Problem): Parsed => (typeof t === 'string' ? { problem: t } : { target: t })
  const packId = query.get('pack')
  if (packId) {
    if (!packs.some((p) => p.id === packId)) return { problem: 'pack_gone' }
    const live = held.filter(isLive)
    if (!live.length) return { problem: 'sub_gone' }
    // 没带是哪一份：预选剩得最少的那份（与选购页流量包标签同一规则）
    const least = [...live].sort((a, b) => (leftOf(a).left ?? Number.MAX_SAFE_INTEGER) - (leftOf(b).left ?? Number.MAX_SAFE_INTEGER))[0]!
    const sub = live.find((s) => s.id === query.get('sub'))?.id ?? least.id
    // 从卡片或选购页来的已经带了是哪一份，不再问；老地址没带、多份时才在确认页选（选过的带 pick=1 继续显示）
    return { target: { kind: 'pack', packId, subId: sub, chooser: live.length > 1 && (!query.get('sub') || query.get('pick') === '1') } }
  }
  const renew = query.get('renew')
  if (renew) {
    const sub = held.find((s) => s.id === renew)
    return sub && sub.renew_until !== null ? { target: { kind: 'renew', subId: sub.id, fromPlans: query.get('from') === 'plans' } } : { problem: 'sub_gone' }
  }
  const change = query.get('change')
  const changePlan = query.get('change-plan')
  if (change || changePlan) {
    const planId = changePlan ?? query.get('plan') ?? ''
    if (!plans.some((p) => p.id === planId)) return { problem: 'plan_gone' }
    if (change && !changePlan) {
      const sub = held.find((s) => s.id === change)
      return sub && sub.changeable && sub.plan_id !== planId ? { target: { kind: 'change', subId: sub.id, planId, chooser: false } } : { problem: 'sub_gone' }
    }
    return wrap(changeOrRenew(planId, held, query.get('sub')))
  }
  const newPlan = query.get('new')
  if (newPlan) return plans.some((p) => p.id === newPlan) ? { target: { kind: 'new', planId: newPlan } } : { problem: 'plan_gone' }
  const legacy = query.get('plan')
  if (legacy) {
    if (!plans.some((p) => p.id === legacy)) return { problem: 'plan_gone' }
    if (!held.length) return { target: { kind: 'new', planId: legacy } }
    const owner = renewOrder(held.filter((s) => s.plan_id === legacy && s.renew_until !== null))[0]
    if (owner) return { target: { kind: 'renew', subId: owner.id, fromPlans: true } }
    return wrap(changeOrRenew(legacy, held, null))
  }
  return { problem: 'missing' }
}

/** 报价请求：换套餐还没选是哪一份时按套餐展开（给「换掉哪一份」每个选项写今天付多少） */
export function quoteRequest(t: Target, held: readonly Subscription[], coupon: string | null): QuoteRequest {
  const c = coupon ? { coupon_code: coupon } : {}
  switch (t.kind) {
    case 'renew':
      return { action: 'renew', subscription_id: t.subId, ...c }
    case 'change':
      return t.subId ? { action: 'change', subscription_id: t.subId, plan_id: t.planId, ...c } : { action: 'change', plan_id: t.planId, ...c }
    case 'new':
      return { action: 'new', plan_id: t.planId, new_copy: held.some(isHeld), ...c }
    default:
      return { action: 'pack', pack_id: t.packId, ...(t.subId ? { subscription_id: t.subId } : {}), ...c }
  }
}

/** 「买多久」按时长排：1 个月、3 个月、1 年，其余排后 */
const rowMonths = (r: Pick<QuoteRow, 'interval' | 'interval_count'>) => periodMonths(periodOf({ billing_interval: r.interval as never, interval_count: r.interval_count })) ?? 999
export const sortTiers = (rows: readonly QuoteRow[]) => [...rows].sort((a, b) => rowMonths(a) - rowMonths(b) || a.subtotal - b.subtotal)

/** 默认的一档：地址点名的 → 续费沿用原价格 → 与这一份现在同样长的 → 第一档 */
export function defaultTier(rows: readonly QuoteRow[], sub: Subscription | undefined, requested: string | null): QuoteRow | null {
  const sorted = sortTiers(rows)
  const hit = (pred: (r: QuoteRow) => boolean) => sorted.find(pred) ?? null
  const rp = sub?.renewal_price
  return (
    (requested ? hit((r) => r.price_id === requested) : null) ??
    (rp?.available ? hit((r) => r.price_id === rp.id) : null) ??
    (rp ? hit((r) => r.interval === rp.billing_interval && r.interval_count === rp.interval_count) : null) ??
    sorted[0] ??
    null
  )
}

// ---------------------------------------------------------------------------
// 建单：四个接口各自的请求体，都带 as_of + expect（服务端重算比对，不符回 409 quote_changed）。
// 后端 DisallowUnknownFields：可选字段不用时不传。use_balance 传这一档报价里用掉的余额。
// ---------------------------------------------------------------------------
export interface OrderInput {
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
    case 'pack':
      return { path: 'v1/me/traffic-pack-orders', body: { pack_id: t.packId, subscription_id: t.subId, ...extra } }
    case 'renew':
      return { path: `v1/me/subscriptions/${encodeURIComponent(t.subId)}/renew`, body: { price_id: i.row.price_id, ...extra } }
    case 'change':
      return { path: `v1/me/subscriptions/${encodeURIComponent(t.subId!)}/change-plan`, body: { plan_id: t.planId, price_id: i.row.price_id, ...extra } }
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

export { balanceSplit }
