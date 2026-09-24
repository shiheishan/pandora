/**
 * [INPUT]: 依赖 ../../../core/format 的 relativeTime，依赖 ../common/traffic 的 expiryInfo，依赖 ../common/subscriptions 的 Subscription / SubscriptionLink 类型
 * [OUTPUT]: 对外提供 metaLabel、fetchStats
 * [POS]: portal/screens/subs 的文案映射：头部元信息与订阅地址拉取统计（含泄露判定），纯函数、有单元测试，页面组件只负责摆放
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { relativeTime } from '../../../core/format'
import type { Subscription, SubscriptionLink } from '../common/subscriptions'
import { expiryInfo } from '../common/traffic'

/** 头部元信息：「N 天后到期 · 日期 · M 台设备 · 当前在线 K」，待补字段缺席时省掉对应段。 */
export function metaLabel(sub: Subscription, now: Date = new Date(), timeZone?: string): string {
  const parts: string[] = []
  const expiry = expiryInfo(sub.current_period_end, now, timeZone)
  parts.push(expiry ? `${expiry.days} 天后到期 · ${expiry.date}` : '长期有效')
  if (sub.device_limit !== undefined) parts.push(sub.device_limit === null ? '不限设备' : `${sub.device_limit} 台设备`)
  if (sub.online_devices !== undefined) parts.push(`当前在线 ${sub.online_devices}`)
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

