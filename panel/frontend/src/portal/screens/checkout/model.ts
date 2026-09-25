/**
 * [INPUT]: 依赖 ../common/catalog 的 Plan / Pack / Price / cnyPrices / periodOf / PERIODS，依赖 ../../queries 的 Subscription / pickPrimary / isLive
 * [OUTPUT]: 对外提供 CheckoutMode、resolveMode、periodOptions、defaultPriceId、isRepriced、Quote、buildQuote、orderRequest、couponPreviewBody、couponNote、normalizeCoupon
 * [POS]: portal/screens/checkout 的纯逻辑：由地址参数和目录决定结账模式（契约门户-03：没有订阅走新购，同套餐走续费，换套餐走变更），列周期选项、算订单预览、拼四种下单请求体；页面只负责摆放与请求，这里全部有单元测试
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { isLive, pickPrimary, type Subscription } from '../../queries'
import { cnyPrices, periodOf, PERIODS, type Pack, type Plan, type Price } from '../common/catalog'

// ---------------------------------------------------------------------------
// 模式：地址 #/checkout?plan=&price= | ?renew=<订阅>&price= | ?pack=
// ---------------------------------------------------------------------------
export type CheckoutMode =
  | { kind: 'new'; plan: Plan }
  | { kind: 'renew'; sub: Subscription; plan: Plan | null }
  | { kind: 'change'; sub: Subscription; plan: Plan }
  | { kind: 'pack'; pack: Pack }

export type ModeResult = { mode: CheckoutMode } | { problem: 'missing' | 'plan_gone' | 'pack_gone' | 'sub_gone' }

export function resolveMode(query: URLSearchParams, plans: readonly Plan[], packs: readonly Pack[], subs: readonly Subscription[]): ModeResult {
  const packId = query.get('pack')
  if (packId) {
    const pack = packs.find((p) => p.id === packId)
    return pack ? { mode: { kind: 'pack', pack } } : { problem: 'pack_gone' }
  }
  const renewId = query.get('renew')
  if (renewId) {
    const sub = subs.find((s) => s.id === renewId && isLive(s))
    if (!sub) return { problem: 'sub_gone' }
    return { mode: { kind: 'renew', sub, plan: plans.find((p) => p.id === sub.plan_id) ?? null } }
  }
  const planId = query.get('plan')
  if (planId) {
    const plan = plans.find((p) => p.id === planId)
    if (!plan) return { problem: 'plan_gone' }
    const primary = pickPrimary(subs)
    if (!primary) return { mode: { kind: 'new', plan } }
    if (primary.plan_id === plan.id) return { mode: { kind: 'renew', sub: primary, plan } }
    return { mode: { kind: 'change', sub: primary, plan } }
  }
  return { problem: 'missing' }
}

const periodRank = (p: Price) => {
  const i = PERIODS.indexOf(periodOf(p))
  return i < 0 ? PERIODS.length : i
}

/**
 * 周期选项（只列 CNY）。续费：套餐当前有效价格，外加订阅的原价格（仍有效但不在目录里时，
 * 例如组专属价）；原价格失效（renewal_price.available=false）时只列当前价格。
 */
export function periodOptions(mode: CheckoutMode): Price[] {
  if (mode.kind === 'pack') return []
  const listed = mode.plan ? cnyPrices(mode.plan) : []
  if (mode.kind === 'renew') {
    const rp = mode.sub.renewal_price
    if (rp && rp.available && rp.currency === 'CNY' && !listed.some((p) => p.id === rp.id)) {
      const interval = rp.billing_interval as Price['billing_interval']
      listed.push({ id: rp.id, currency: rp.currency, unit_amount: rp.unit_amount, billing_interval: interval, interval_count: rp.interval_count, trial_days: 0 })
    }
  }
  return [...listed].sort((a, b) => periodRank(a) - periodRank(b) || a.unit_amount - b.unit_amount)
}

/** 续费遇改价（契约门户-02 renew 待补·前端）：原价格已不可用 */
export const isRepriced = (mode: CheckoutMode) => mode.kind === 'renew' && mode.sub.renewal_price?.available === false

/** 默认选中：地址里点名的价格 → 续费沿用原价格 → 改价后选同 billing_interval → 第一项 */
export function defaultPriceId(mode: CheckoutMode, options: readonly Price[], requested: string | null): string | null {
  if (requested && options.some((p) => p.id === requested)) return requested
  if (mode.kind === 'renew') {
    const rp = mode.sub.renewal_price
    if (rp?.available && options.some((p) => p.id === rp.id)) return rp.id
    const same = rp && options.find((p) => p.billing_interval === rp.billing_interval && p.interval_count === rp.interval_count)
    if (same) return same.id
    const byInterval = rp && options.find((p) => p.billing_interval === rp.billing_interval)
    if (byInterval) return byInterval.id
  }
  return options[0]?.id ?? null
}

// ---------------------------------------------------------------------------
// 订单预览：新购 / 续费 / 流量包按目录价与优惠码试算前端算；变更套餐用服务端试算
// ---------------------------------------------------------------------------
export interface ChangePreview {
  direction: 'upgrade' | 'downgrade'
  currency: string
  subtotal: number
  proration_credit: number
  discount: number
  total: number
  balance_refund: number
  current_period_end: string
  new_period_start: string
  new_period_end: string
  /** R76 / R114：所用优惠码的券面，没用码时为 null */
  coupon: { code: string; discount_type: 'percent' | 'fixed'; discount_value: number } | null
}

export interface Quote {
  currency: string
  subtotal: number
  discount: number
  /** 变更套餐的剩余价值折算（正数，展示为减项） */
  credit: number
  /** 余额抵扣前应付 */
  due: number
  balanceApplied: number
  payable: number
  /** 降级退回余额的差额 */
  refund: number
}

export function buildQuote(input: { subtotal: number; currency: string; discount: number; change?: ChangePreview | null; balance: number; useBalance: boolean }): Quote {
  const change = input.change ?? null
  const subtotal = change ? change.subtotal : input.subtotal
  const discount = change ? change.discount : Math.min(input.discount, input.subtotal)
  const credit = change ? change.proration_credit : 0
  const due = change ? change.total : Math.max(0, subtotal - discount)
  // 契约：开关打开时 use_balance = min(余额, 优惠后应付)
  const balanceApplied = input.useBalance ? Math.max(0, Math.min(input.balance, due)) : 0
  return { currency: change?.currency ?? input.currency, subtotal, discount, credit, due, balanceApplied, payable: due - balanceApplied, refund: change?.balance_refund ?? 0 }
}

// ---------------------------------------------------------------------------
// 请求体：后端 DisallowUnknownFields，可选字段不用时不传
// ---------------------------------------------------------------------------
export function orderRequest(mode: CheckoutMode, priceId: string | null, balanceApplied: number, coupon: string | null): { path: string; body: Record<string, unknown> } {
  const extra: Record<string, unknown> = {}
  if (balanceApplied > 0) extra.use_balance = balanceApplied
  if (coupon) extra.coupon_code = coupon
  switch (mode.kind) {
    case 'pack':
      return { path: 'v1/me/traffic-pack-orders', body: { pack_id: mode.pack.id, ...extra } }
    case 'renew':
      return { path: `v1/me/subscriptions/${encodeURIComponent(mode.sub.id)}/renew`, body: { ...(priceId ? { price_id: priceId } : {}), ...extra } }
    case 'change':
      return { path: `v1/me/subscriptions/${encodeURIComponent(mode.sub.id)}/change-plan`, body: { plan_id: mode.plan.id, price_id: priceId, ...extra } }
    default:
      return { path: 'v1/orders', body: { plan_id: mode.plan.id, price_id: priceId, ...extra } }
  }
}

/** POST v1/coupons/preview 的请求体：流量包用 pack_id 形态（修订 R69），其余用套餐 + 价格 */
export function couponPreviewBody(mode: CheckoutMode, priceId: string | null, code: string): Record<string, unknown> | null {
  if (mode.kind === 'pack') return { pack_id: mode.pack.id, coupon_code: code }
  const planId = mode.kind === 'renew' ? mode.sub.plan_id : mode.plan.id
  return priceId ? { plan_id: planId, price_id: priceId, coupon_code: code } : null
}

export const normalizeCoupon = (raw: string) => raw.trim().toUpperCase()

/** 成功行「已使用 CODE：20% 折扣 / 立减 ¥X」；percent 的 discount_value 是万分比 */
export function couponNote(code: string, coupon: { discount_type: 'percent' | 'fixed'; discount_value: number } | null | undefined, discount: number, fmt: (minor: number) => string): string {
  if (coupon?.discount_type === 'percent') return `已使用 ${code}：${Number((coupon.discount_value / 100).toFixed(2))}% 折扣`
  if (coupon?.discount_type === 'fixed') return `已使用 ${code}：立减 ${fmt(coupon.discount_value)}`
  return `已使用 ${code}：优惠 ${fmt(discount)}`
}
