import type { Sub, User } from './users.ts'

// ---------------------------------------------------------------------------
// 后台开单的落点（假后端）：照 Go 的 purchase.Options（planOptions 一支）原样移植，
// 再按 purchase.Placement 的形状补上「这一份现在的样子」和「落地之后会怎样」。
// 假后端的钱是近似：剩余价值 = 本期付费 × min(剩余天数比, 剩余流量比)，与 prorationCredit 同一公式
// ---------------------------------------------------------------------------
const DAY = 86_400_000
/** 原地续费窗口：过期不满 30 天还能续（billing/expire.go 在满 30 天时关窗） */
const RENEWAL_WINDOW_DAYS = 30
const LIVE = new Set<Sub['status']>(['active', 'trialing', 'grace', 'past_due'])

export type State = 'live' | 'revivable' | 'dead'
export interface Candidate {
  sub: Sub
  state: State
}

export function stateOf(s: Sub, now: number): State {
  if (LIVE.has(s.status)) return 'live'
  if (s.status === 'expired' && s.current_period_end !== null) {
    const lapsed = now - Date.parse(s.current_period_end)
    if (lapsed >= 0 && lapsed < RENEWAL_WINDOW_DAYS * DAY) return 'revivable'
  }
  return 'dead'
}

export const candidatesOf = (u: User, now: number): Candidate[] => u.subs.map((sub) => ({ sub, state: stateOf(sub, now) }))

export type Kind = 'renew' | 'change' | 'new'
export interface Option {
  key: string
  kind: Kind
  subscription_id?: string
  expired?: boolean
  badge?: string
}
export const optionKey = (kind: Kind, subscriptionId?: string) => (kind === 'new' ? 'new' : `${kind}:${subscriptionId}`)

/** 永不过期的排最后 */
const endOf = (c: Candidate) => (c.sub.current_period_end === null ? Number.MAX_SAFE_INTEGER : Date.parse(c.sub.current_period_end))
const byEndAsc = (cs: Candidate[]) => [...cs].sort((a, b) => endOf(a) - endOf(b) || (a.sub.id < b.sub.id ? -1 : a.sub.id > b.sub.id ? 1 : 0))
const byEndDesc = (cs: Candidate[]) => byEndAsc(cs).reverse()
const only = (cs: Candidate[], state: State) => cs.filter((c) => c.state === state)
const opt = (kind: Kind, c: Candidate): Option => ({ key: optionKey(kind, c.sub.id), kind, subscription_id: c.sub.id })

/**
 * 套餐卡 / 后台开单的选项与默认（entrySub 是入口订阅，优先作为默认）。规则见 purchase.planOptions：
 * 同款可续（生效中按到期从早到晚，再过期 30 天内按到期从晚到早）→ 不同款过期 30 天内（恢复并改成 P）
 * → 新开一份 → 不同款生效中（换掉）。默认：入口那份；否则第一个同款；否则只剩新开就新开；否则不预选。
 * 只有一份、而且就是同款时只返回这一项
 */
export function planOptions(planId: string, cands: Candidate[], entrySub: string): { options: Option[]; defaultKey: string } {
  const alive = cands.filter((c) => c.state !== 'dead')
  const same = alive.filter((c) => c.sub.plan_id === planId)
  const other = alive.filter((c) => c.sub.plan_id !== planId)
  const options: Option[] = [...byEndAsc(only(same, 'live')), ...byEndDesc(only(same, 'revivable'))].map((c) => opt('renew', c))
  const renews = options.length
  let defaultKey = ''
  if (alive.length === 1 && renews === 1) {
    defaultKey = options[0]!.key
  } else {
    for (const c of byEndDesc(only(other, 'revivable'))) options.push({ ...opt('change', c), expired: true })
    options.push({ key: 'new', kind: 'new' })
    for (const c of byEndAsc(only(other, 'live'))) options.push(opt('change', c))
    if (renews > 0) {
      options[0]!.badge = 'same_plan'
      defaultKey = options[0]!.key
    } else if (options.length === 1) {
      defaultKey = options[0]!.key
    }
  }
  const entry = options.find((o) => entrySub !== '' && o.subscription_id === entrySub)
  if (entry) defaultKey = entry.key
  return { options, defaultKey }
}

/** period.AddInterval：月、季、年按日历加，日、周按天加；一次性按一个月算 */
export function addInterval(from: number, interval: string, count: number): Date {
  const d = new Date(from)
  const n = Math.max(1, count)
  switch (interval) {
    case 'day':
      d.setUTCDate(d.getUTCDate() + n)
      break
    case 'week':
      d.setUTCDate(d.getUTCDate() + 7 * n)
      break
    case 'quarter':
      d.setUTCMonth(d.getUTCMonth() + 3 * n)
      break
    case 'year':
      d.setUTCFullYear(d.getUTCFullYear() + n)
      break
    default:
      d.setUTCMonth(d.getUTCMonth() + n)
  }
  return d
}

/** 换套餐时原套餐没用完的部分：本期付费 × min(剩余天数比, 剩余流量比)；已过期的没有 */
export function prorationCredit(s: Sub, now: number): number {
  if (!LIVE.has(s.status)) return 0
  const start = s.current_period_start ? Date.parse(s.current_period_start) : NaN
  const end = s.current_period_end ? Date.parse(s.current_period_end) : NaN
  const timeRatio = Number.isFinite(start) && Number.isFinite(end) && end > start ? Math.min(1, Math.max(0, (end - now) / (end - start))) : 1
  const trafficRatio = s.traffic_limit && s.traffic_limit > 0 ? Math.min(1, Math.max(0, (s.traffic_limit - s.traffic_used) / s.traffic_limit)) : 1
  return Math.round(s.amount * Math.min(timeRatio, trafficRatio))
}

/**
 * 后台待支付单用户要付多少（分）：续一期与另开一份是价格，换套餐先抵原套餐没用完的部分（billing.orderTotal）。
 * preview 的 due 与建单共用
 */
export function manualDue(kind: Option['kind'], amount: number, credit: number): number {
  return Math.max(amount - (kind === 'change' ? credit : 0), 0)
}

/** 用户付不了：应付为正且低于站点最低额，最低额 ≤ 1 分等于不限（billing.manualDueBelowMinimum） */
export const manualDueBelowMinimum = (due: number, minPay: number) => due > 0 && minPay > 1 && due < minPay

export interface PriceInfo {
  interval: string
  count: number
  currency: string
}

/** purchase.Placement 的 json 形状：选项 + 这一份现在的样子 + 落地之后的到期日与剩余价值（omitempty 的键不出现） */
export function placementView(o: Option, c: Candidate | undefined, price: PriceInfo, now: number) {
  const view: Record<string, unknown> = { ...o }
  if (c) {
    const s = c.sub
    const base = o.kind === 'renew' && c.state === 'live' && s.current_period_end ? Date.parse(s.current_period_end) : now
    Object.assign(view, {
      ...(s.label ? { label: s.label } : {}),
      plan_id: s.plan_id,
      plan_name: s.plan_name,
      state: c.state,
      ...(s.current_period_end ? { period_end: s.current_period_end } : {}),
      new_period_end: addInterval(base, price.interval, price.count).toISOString(),
      currency: s.currency,
      ...(s.traffic_used > 0 ? { traffic_used: s.traffic_used } : {}),
      ...(s.traffic_limit ? { traffic_cap: s.traffic_limit } : {}),
      ...(s.pack_bytes > 0 ? { pack_remaining: s.pack_bytes } : {}),
    })
    if (o.kind === 'change') {
      const credit = prorationCredit(s, now)
      if (credit > 0) view.credit = credit
    }
  } else {
    view.new_period_end = addInterval(now, price.interval, price.count).toISOString()
  }
  return view
}
