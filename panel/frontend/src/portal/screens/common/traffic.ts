/**
 * [INPUT]: 无外部依赖（纯函数）
 * [OUTPUT]: 对外提供 GIB、formatGB、daysUntil、formatDate、shortDate、expiryInfo、usageLevel、TRAFFIC_METRIC、pickTrafficQuota、trafficSummary、resetAtOf、projectUsage、buildUsageBars 与相关类型
 * [POS]: portal/screens/common 的流量与期限计算：概览主卡、我的订阅头部、本期用量图共用；只做数字到文案的映射，不碰请求与组件，全部有单元测试
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// ---------------------------------------------------------------------------
// 单位：后端按 1<<30 计 GB（giftcard/redeem.go 的 gb 常量、流量包容量），界面写「GB」。
// ---------------------------------------------------------------------------
export const GIB = 1024 ** 3
const DAY_MS = 86_400_000

/** 字节 → GB 数字串：≥ 10 取整，< 10 保留一位（去掉 .0）；digits 显式给出时按它。 */
export function formatGB(bytes: number, digits?: number): string {
  const value = Math.max(0, bytes) / GIB
  const d = digits ?? (value >= 10 ? 0 : 1)
  const text = value.toFixed(d)
  return digits === undefined ? text.replace(/\.0$/, '') : text
}

/** 距离某时刻还有几天（向上取整，过去为 0）。 */
export function daysUntil(at: string | Date, now: Date = new Date()): number {
  const t = typeof at === 'string' ? new Date(at).getTime() : at.getTime()
  return Math.max(0, Math.ceil((t - now.getTime()) / DAY_MS))
}

const pad = (n: number) => String(n).padStart(2, '0')

/**
 * 日期 YYYY-MM-DD。给了 timeZone（按日用量接口回的切日时区，修订 R48 / R50）就按它，
 * 让到期日、重置日与用量柱的日期同一口径；没给或时区名无效时用浏览器本地时区。
 */
export function formatDate(at: string | Date, timeZone?: string): string {
  const d = typeof at === 'string' ? new Date(at) : at
  if (timeZone) {
    try {
      return new Intl.DateTimeFormat('en-CA', { timeZone, year: 'numeric', month: '2-digit', day: '2-digit' }).format(d)
    } catch {
      // 落到本地时区
    }
  }
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

/** 日期 MM-DD，时区口径同 formatDate */
export const shortDate = (at: string | Date, timeZone?: string) => formatDate(at, timeZone).slice(5)

// ---------------------------------------------------------------------------
// 到期：设计稿 7 天内转警示色并给「立即续费」主按钮
// ---------------------------------------------------------------------------
export interface ExpiryInfo {
  days: number
  date: string
  urgent: boolean
}

export function expiryInfo(periodEnd: string | null, now: Date = new Date(), timeZone?: string): ExpiryInfo | null {
  if (!periodEnd) return null
  const days = daysUntil(periodEnd, now)
  return { days, date: formatDate(periodEnd, timeZone), urgent: days <= 7 }
}

export type UsageLevel = 'ok' | 'warn' | 'danger'

/** 设计稿口径：已用 ≥ 95% 危险、≥ 80% 警示 */
export const usageLevel = (ratio: number): UsageLevel => (ratio >= 0.95 ? 'danger' : ratio >= 0.8 ? 'warn' : 'ok')

// ---------------------------------------------------------------------------
// 流量额度：quotas[] 里 metric = traffic.bytes 的那一行。现有接口不带周期字段，
// 待补字段上线后同一指标可能有多行，取周期覆盖当前时刻的那行。
// ---------------------------------------------------------------------------
export interface QuotaLike {
  metric: string
  limit: number | null
  consumed: number
  remaining: number | null
  period_start?: string | null
  period_end?: string | null
}

export const TRAFFIC_METRIC = 'traffic.bytes'

export function pickTrafficQuota<Q extends QuotaLike>(quotas: readonly Q[], now: Date = new Date()): Q | null {
  const rows = quotas.filter((q) => q.metric === TRAFFIC_METRIC)
  const t = now.getTime()
  const current = rows.find(
    (q) => (!q.period_start || new Date(q.period_start).getTime() <= t) && (!q.period_end || new Date(q.period_end).getTime() > t),
  )
  return current ?? rows[0] ?? null
}

export interface TrafficSummary {
  used: number
  /** 本期总量 = 已用 + 剩余；null = 不限 */
  total: number | null
  /** 套餐剩余（不含流量包）；null = 不限 */
  remaining: number | null
  /** 流量包余量，挂在用户身上，套餐扣完后才扣（5.A D-E-1） */
  pack: number
  /** 已用 / 总量，不限时为 0 */
  ratio: number
}

/** 契约映射：已用 = consumed；总量 = consumed + remaining；剩余 = remaining（null 为不限）。 */
export function trafficSummary(quota: QuotaLike | null, packBytes = 0): TrafficSummary | null {
  if (!quota) return null
  const remaining = quota.remaining === null ? null : Math.max(0, quota.remaining)
  const total = remaining === null ? null : quota.consumed + remaining
  const ratio = total ? Math.min(1, quota.consumed / total) : total === 0 ? 1 : 0
  return { used: quota.consumed, total, remaining, pack: Math.max(0, packBytes), ratio }
}

/**
 * 下次重置时刻：strategy=never 不重置；待补字段 next_reset_at 优先，其次是额度行的周期末，
 * 再其次是按日用量接口的 period_end（它的窗口就是当前流量周期，修订 R47）。
 */
export function resetAtOf(
  sub: { quota_reset_strategy?: string; next_reset_at?: string | null },
  quota: { period_end?: string | null } | null,
  usage: { period_end: string | null } | undefined,
): string | null {
  if (sub.quota_reset_strategy === 'never') return null
  return sub.next_reset_at ?? quota?.period_end ?? usage?.period_end ?? null
}

// ---------------------------------------------------------------------------
// 预测：「按目前的速度…」。套餐扣完再扣流量包，所以可用量 = 套餐剩余 + 流量包；
// 预计到重置时用量 ≥ (总量 + 流量包) 的 95% 视为不够用，给「买流量包 →」。
// ---------------------------------------------------------------------------
export interface Projection {
  text: string
  short: boolean
}

export function projectUsage(summary: TrafficSummary, avgDailyBytes: number, resetAt: string | null, now: Date = new Date()): Projection {
  if (summary.total === null) return { text: '本期流量不限量。', short: false }
  if (avgDailyBytes <= 0) return { text: '本期还没有产生用量。', short: false }
  const left = (summary.remaining ?? 0) + summary.pack
  const runout = Math.floor(left / avgDailyBytes)
  if (!resetAt) return { text: `按目前的速度，约 ${runout} 天后用完。`, short: runout <= 7 }
  const projected = summary.used + avgDailyBytes * daysUntil(resetAt, now)
  if (projected >= (summary.total + summary.pack) * 0.95) {
    return { text: `按目前的速度，约 ${runout} 天后用完，比重置早。`, short: true }
  }
  return { text: `按目前的速度，本期预计用到 ${formatGB(projected, 0)} GB，够用。`, short: false }
}

// ---------------------------------------------------------------------------
// 本期用量柱：days[] 是到今天为止（含今天）的每日字节，日期是接口所用时区的
// YYYY-MM-DD；period_end 之前剩下的日子画成灰色矮柱「未到」。
// ---------------------------------------------------------------------------
export interface UsageBar {
  date: string
  /** null = 未到 */
  bytes: number | null
  today: boolean
  /** 相对最高柱的百分比，0–100 */
  height: number
}

export interface UsageBars {
  bars: UsageBar[]
  start: string
  /** 右端标签：有周期末为「MM-DD 重置」 */
  endLabel: string
  range: string
}

const MAX_BARS = 93

function addDays(ymd: string, n: number): string {
  const [y, m, d] = ymd.split('-').map(Number) as [number, number, number]
  const t = new Date(Date.UTC(y, m - 1, d + n))
  return `${t.getUTCFullYear()}-${pad(t.getUTCMonth() + 1)}-${pad(t.getUTCDate())}`
}

export function buildUsageBars(usage: { timezone: string; period_end: string | null; days: ReadonlyArray<{ date: string; bytes: number }> }): UsageBars {
  const days = usage.days.slice(-MAX_BARS)
  const last = days[days.length - 1]?.date
  const endDate = usage.period_end ? formatDate(usage.period_end, usage.timezone) : null
  const future: string[] = []
  if (last && endDate) {
    for (let d = addDays(last, 1); d < endDate && days.length + future.length < MAX_BARS; d = addDays(d, 1)) future.push(d)
  }
  const max = Math.max(1, ...days.map((d) => d.bytes))
  const bars: UsageBar[] = [
    ...days.map((d) => ({ date: d.date, bytes: d.bytes, today: d.date === last, height: (d.bytes / max) * 100 })),
    ...future.map((date) => ({ date, bytes: null, today: false, height: 0 })),
  ]
  const start = days[0]?.date.slice(5) ?? ''
  const end = endDate ? endDate.slice(5) : (last?.slice(5) ?? '')
  return { bars, start, endLabel: endDate ? `${end} 重置` : end, range: start && end ? `${start} 至 ${end}` : '' }
}
