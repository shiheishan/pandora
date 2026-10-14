import { dayTime, gb, isLow, leftOf } from './purchase'
import type { Subscription, SubscriptionLink } from './subscriptions'
import { pickTrafficQuota } from './traffic'

// 一份的卡片文字：我的套餐与概览共用

const HOUR_MS = 3_600_000
const DAY_MS = 86_400_000

/** 过期了（状态已翻成 expired，或过期扫描还没赶上、周期末已过）：链接暂停，续费后自动恢复 */
export function isExpiredNow(sub: Pick<Subscription, 'status' | 'current_period_end'>, now: Date = new Date()): boolean {
  return sub.status === 'expired' || (sub.current_period_end !== null && new Date(sub.current_period_end).getTime() <= now.getTime())
}

export interface ExpiryText {
  main: string
  /** 第二行：到期时刻 */
  sub: string
  tone: 'ok' | 'warn' | 'danger'
}

/**
 * 卡片右上角的到期（原型 .exp）：7 天以上「还剩 23 天」，7 天内「5 天后到期」（警示色），
 * 最后 24 小时「还剩 6 小时」，过了「已过期 3 天」；第二行是精确到分钟的到期时刻。
 */
export function expiryText(sub: Pick<Subscription, 'status' | 'current_period_end'>, now: Date = new Date()): ExpiryText {
  if (!sub.current_period_end) return { main: '长期有效', sub: '', tone: 'ok' }
  const end = new Date(sub.current_period_end)
  const ms = end.getTime() - now.getTime()
  const at = dayTime(end, now)
  if (ms <= 0 || sub.status === 'expired') {
    const n = Math.floor(Math.max(0, -ms) / DAY_MS)
    return { main: n >= 1 ? `已过期 ${n} 天` : '今天已过期', sub: `${at} 到期`, tone: 'danger' }
  }
  if (ms < DAY_MS) return { main: `还剩 ${Math.ceil(ms / HOUR_MS)} 小时`, sub: `${at} 到期`, tone: 'warn' }
  const days = Math.ceil(ms / DAY_MS)
  if (days <= 7) return { main: `${days} 天后到期`, sub: at, tone: 'warn' }
  return { main: `还剩 ${days} 天`, sub: `${at} 到期`, tone: 'ok' }
}

/** 流量额度的周期叫法：本月 / 今天 / 本期 */
export function periodWord(sub: Pick<Subscription, 'quotas'>): string {
  const q = pickTrafficQuota(sub.quotas)
  if (!q) return '本期'
  return q.period === 'month' ? '本月' : q.period === 'day' ? '今天' : q.period === 'total' ? '总共' : '本期'
}

export interface UsageText {
  label: string
  /** 「剩 8G / 共 100G（含流量包 30G）」；不限量为「不限流量」 */
  right: string
  /** 已用百分比 0–100；不限量为 null */
  pct: number | null
  low: boolean
}

export function usageText(sub: Subscription): UsageText {
  const { left, total, used, pack } = leftOf(sub)
  const low = isLow(sub)
  const label = `${periodWord(sub)}流量${low ? ' · 快用完了' : ''}`
  if (left === null || total === null) return { label, right: '不限流量', pct: null, low: false }
  return {
    label,
    right: `剩 ${gb(left)} / 共 ${gb(total)}${pack > 0 ? `（含流量包 ${gb(pack)}）` : ''}`,
    pct: total > 0 ? Math.min(100, Math.round((used / total) * 100)) : 100,
    low,
  }
}

/** 近 24 小时的来源数超过设备上限：可能泄露（过期的不提示） */
export function leakSources(sub: Subscription, link: SubscriptionLink | undefined): number | null {
  if (!link || isExpiredNow(sub) || sub.device_limit === null) return null
  return link.distinct_sources_24h > sub.device_limit ? link.distinct_sources_24h : null
}

