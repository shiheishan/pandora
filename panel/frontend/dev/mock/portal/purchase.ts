// ---------------------------------------------------------------------------
// panel/internal/domain/purchase 的原样移植：落点选项与默认值、备注名规范化、余额与支付最低额。
// 规则以 Go 为准（options.go / balance.go / label.go），改 Go 要同步这里。
// ---------------------------------------------------------------------------

export type State = 'live' | 'revivable' | 'dead'
export type OfferKind = 'plan' | 'days' | 'reset' | 'traffic' | 'mixed'
export type Kind = 'renew' | 'change' | 'new' | 'extend_days' | 'reset_traffic' | 'add_traffic'

export interface Candidate {
  subscriptionId: string
  planId: string
  state: State
  /** 毫秒；null = 永不过期 */
  periodEnd: number | null
  trafficUsed: number
  /** 0 = 不限 */
  trafficCap: number
  packRemaining: number
}

export interface Offer {
  kind: OfferKind
  planId?: string
  hasDays?: boolean
  hasReset?: boolean
  hasTraffic?: boolean
}

export interface Option {
  key: string
  kind: Kind
  subscription_id?: string
  expired?: boolean
  badge?: string
}

export interface Choice {
  kind: Kind
  subscription_id?: string
}

export const optionKey = (kind: Kind, sub?: string) => (kind === 'new' ? 'new' : `${kind}:${sub ?? ''}`)

const endOf = (c: Candidate) => c.periodEnd ?? Number.MAX_SAFE_INTEGER
const byEndAsc = (cs: Candidate[]) => [...cs].sort((a, b) => endOf(a) - endOf(b) || (a.subscriptionId < b.subscriptionId ? -1 : 1))
const byEndDesc = (cs: Candidate[]) => byEndAsc(cs).reverse()
const only = (cs: Candidate[], ...states: State[]) => cs.filter((c) => states.includes(c.state))
const option = (kind: Kind, c: Candidate): Option => ({ key: optionKey(kind, c.subscriptionId), kind, subscription_id: c.subscriptionId })
const usedRatio = (c: Candidate) => (c.trafficCap <= 0 ? 0 : c.trafficUsed / c.trafficCap)
const remaining = (c: Candidate) => (c.trafficCap <= 0 ? Number.MAX_SAFE_INTEGER : Math.max(0, c.trafficCap - c.trafficUsed) + c.packRemaining)

function planOptions(planId: string, cands: Candidate[]): [Option[], string] {
  const alive = cands.filter((c) => c.state !== 'dead')
  const same = alive.filter((c) => c.planId === planId)
  const other = alive.filter((c) => c.planId !== planId)
  const opts: Option[] = [...byEndAsc(only(same, 'live')), ...byEndDesc(only(same, 'revivable'))].map((c) => option('renew', c))
  const renews = opts.length
  if (alive.length === 1 && renews === 1) return [opts, opts[0]!.key]
  for (const c of byEndDesc(only(other, 'revivable'))) opts.push({ ...option('change', c), expired: true })
  opts.push({ key: 'new', kind: 'new' })
  for (const c of byEndAsc(only(other, 'live'))) opts.push(option('change', c))
  if (renews > 0) {
    opts[0]!.badge = 'same_plan'
    return [opts, opts[0]!.key]
  }
  if (opts.length === 1) return [opts, opts[0]!.key]
  // 不同款一律不预选（10-07）
  return [opts, '']
}

function daysOptions(cands: Candidate[]): [Option[], string] {
  const opts = [...byEndAsc(only(cands, 'live')), ...byEndAsc(only(cands, 'revivable'))].map((c) => option('extend_days', c))
  if (!opts.length) return [[], '']
  opts[0]!.badge = 'soonest_expiry'
  return [opts, opts[0]!.key]
}

function pickBest(live: Candidate[], better: (a: Candidate, b: Candidate) => boolean): number {
  let best = 0
  live.forEach((c, i) => {
    if (better(c, live[best]!)) best = i
  })
  return best
}

function resetOptions(cands: Candidate[]): [Option[], string] {
  const live = byEndAsc(only(cands, 'live'))
  if (!live.length) return [[], '']
  const best = pickBest(live, (a, b) => usedRatio(a) > usedRatio(b))
  const opts = live.map((c) => option('reset_traffic', c))
  if (opts.length > 1) opts[best]!.badge = 'most_used'
  return [opts, opts[best]!.key]
}

function trafficOptions(cands: Candidate[]): [Option[], string] {
  const live = byEndAsc(only(cands, 'live'))
  if (!live.length) return [[], '']
  const best = pickBest(live, (a, b) => remaining(a) < remaining(b))
  const opts = live.map((c) => option('add_traffic', c))
  if (opts.length > 1) opts[best]!.badge = 'least_remaining'
  return [opts, opts[best]!.key]
}

export function options(o: Offer, cands: Candidate[], entrySub = ''): [Option[], string] {
  let res: [Option[], string] = [[], '']
  if (o.kind === 'plan') res = planOptions(o.planId ?? '', cands)
  else if (o.kind === 'days') res = daysOptions(cands)
  else if (o.kind === 'reset') res = resetOptions(cands)
  else if (o.kind === 'traffic') res = trafficOptions(cands)
  else if (o.hasDays && !o.hasReset && !o.hasTraffic) res = daysOptions(cands)
  else if (o.hasDays) res = daysOptions(only(cands, 'live'))
  else if (o.hasReset) res = resetOptions(cands)
  else if (o.hasTraffic) res = trafficOptions(cands)
  const entry = entrySub ? res[0].find((op) => op.subscription_id === entrySub) : undefined
  return entry ? [res[0], entry.key] : res
}

export class ChoiceError extends Error {}

/** purchase.Resolve：给了选择就 Match；没给且只有一项就用它；多项回「请先选一种用法」；没有选项回 null */
export function resolve(choice: Choice | null, opts: Option[]): Option | null {
  if (choice && choice.kind) {
    const hit = opts.find((o) => o.key === optionKey(choice.kind, choice.subscription_id))
    if (!hit) throw new ChoiceError('这个用法现在不能用了，请刷新后再选')
    return hit
  }
  if (opts.length === 0) return null
  if (opts.length === 1) return opts[0]!
  throw new ChoiceError('请先选一种用法')
}

// ---------------------------------------------------------------------------
// 余额：purchase.ApplyBalance
// ---------------------------------------------------------------------------
export interface Balance {
  applied: number
  payable: number
  kept: number
  forced: boolean
  /** 应付低于最低额、用尽余额也付不完：下不了单（只有换套餐 ≤ ¥0.99 的零头能免） */
  below_minimum: boolean
  small_due: boolean
  /** SmallDue 时免掉、记作折扣的钱（purchase.WaiveSmallDue） */
  waived: number
}

export function applyBalance(due: number, available: number, requested: number, minPay: number): Balance {
  due = Math.max(0, due)
  available = Math.max(0, available)
  const limit = Math.min(due, available)
  requested = Math.min(Math.max(0, requested), limit)
  const b: Balance = { applied: requested, payable: due - requested, kept: 0, forced: false, below_minimum: false, small_due: false, waived: 0 }
  if (b.payable <= 0) {
    // 余额付清整单：整单本身低于最低额时这也是唯一付法，同样标 forced（Go purchase.ApplyBalance，B6c）
    b.forced = due > 0 && minPay > 1 && due < minPay
    return b
  }
  if (minPay <= 1 || b.payable >= minPay) return b
  if (due >= minPay) {
    const applied = due - minPay
    b.kept = b.applied - applied
    b.applied = applied
    b.payable = minPay
  } else if (available >= due) {
    b.applied = due
    b.payable = 0
    b.forced = true
  } else {
    // 用尽余额，剩下的零头标 below_minimum（与开关无关）
    b.applied = available
    b.payable = due - available
    b.below_minimum = true
  }
  return b
}

/** purchase.MaxSmallDueWaive：一次最多免 ¥0.99 */
export const MAX_SMALL_DUE_WAIVE = 99

/** purchase.WaiveSmallDue：只有换套餐（allowed）且零头 ≤ ¥0.99 时免掉——payable 归零、记进 waived、清 below_minimum */
export function waiveSmallDue(b: Balance, allowed: boolean): Balance {
  if (!allowed || !b.below_minimum || b.payable <= 0 || b.payable > MAX_SMALL_DUE_WAIVE) return b
  return { ...b, waived: b.payable, payable: 0, below_minimum: false, small_due: true }
}

// ---------------------------------------------------------------------------
// 备注名：purchase.NormalizeLabel（错误文案逐字照 Go）
// ---------------------------------------------------------------------------
export const LABEL_TOO_LONG = '名字最多 16 个字'
export const LABEL_INVALID = '名字里不能有换行、制表符之类的特殊字符'

// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u001f\u007f-\u009f\u00ad\u0600-\u0605\u061c\u06dd\u070f\u180e\u200b\u200c\u200e\u200f\u2028-\u202e\u2060-\u2064\u2066-\u206f\ufeff\ufff9-\ufffb]/

export function normalizeLabel(raw: string): { ok: string } | { error: string } {
  const s = raw.trim()
  if (s === '') return { ok: '' }
  if ([...s].length > 16) return { error: LABEL_TOO_LONG }
  if (CONTROL.test(s)) return { error: LABEL_INVALID }
  return { ok: s }
}

/** subscription.ProfileName：「站点名 · 备注名」，没有备注名时用套餐名 */
export const profileName = (site: string, label: string | null, planName: string) => `${site} · ${label || planName}`
