import { relativeTime } from '../../../core/format'
import type { Subscription, SubscriptionLink } from '../common/subscriptions'
import { expiryInfo } from '../common/traffic'

/**
 * 头部元信息：「N 天后到期 · 日期时刻 · M 台设备 · 当前在线 K」；最后 24 小时是「还剩 X 小时」，
 * 已到期是「已于 … 到期」；设备上限为 null 时写「不限设备」。
 */
export function metaLabel(sub: Subscription, now: Date = new Date(), timeZone?: string): string {
  const parts: string[] = []
  const expiry = expiryInfo(sub.current_period_end, now, timeZone)
  parts.push(expiry ? (expiry.expired ? expiry.label : `${expiry.label} · ${expiry.date}`) : '长期有效')
  parts.push(sub.device_limit === null ? '不限设备' : `${sub.device_limit} 台设备`)
  parts.push(`当前在线 ${sub.online_devices}`)
  return parts.join(' · ')
}

/** 拉取统计：「已被拉取 N 次 · 最近 X 前 · 近 24 小时 K 个来源」；K 超过设备上限视为可能泄露。 */
export function fetchStats(link: SubscriptionLink, deviceLimit: number | null | undefined, now: Date = new Date()): { text: string; leak: boolean } {
  const last = link.last_fetched_at ? relativeTime(link.last_fetched_at, now) : null
  const lastText = last === null ? '还没有被拉取过' : `最近 ${last.endsWith('分钟') || last.endsWith('小时') ? `${last}前` : last}`
  const text = `已被拉取 ${link.fetch_count} 次 · ${lastText} · 近 24 小时 ${link.distinct_sources_24h} 个来源`
  const leak = typeof deviceLimit === 'number' && link.distinct_sources_24h > deviceLimit
  return { text, leak }
}


/** 订阅已过期（状态已翻成 expired，或过期扫描还没赶上、周期末已过）：链接只读、不许更换。 */
export function isExpiredView(sub: Pick<Subscription, 'status' | 'current_period_end'>, now: Date = new Date()): boolean {
  return sub.status === 'expired' || (sub.current_period_end !== null && new Date(sub.current_period_end).getTime() <= now.getTime())
}
