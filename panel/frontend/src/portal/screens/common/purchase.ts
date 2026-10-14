import { formatMoney } from '../../../core/format'
import type { Plan } from './catalog'
import { isLive, type Subscription, type SubscriptionLink } from './subscriptions'
import { pickTrafficQuota, trafficSummary } from './traffic'

// ---------------------------------------------------------------------------
// 购买流程的公共叫法与小工具（原型 index.html 第 7 节与 sn / dn / who）。
// 叫法：订阅叫「套餐」、数量用「一份」；订阅地址叫「链接」；客户端叫「App」。
// 渲染文字里不出现「订阅、导入、抵扣、折算、剩余价值、降级、客户端、落点」（tests 有扫描）。
// ---------------------------------------------------------------------------

/** 还在手上的一份：生效中，或过期 30 天内还能救回（续费或换套餐都在原链接上） */
export const isHeld = (s: Subscription) => isLive(s) || (s.status === 'expired' && (s.changeable || s.renewable))
/** 已过期但还能救回 */
export const isRevivable = (s: Subscription) => s.status === 'expired' && (s.changeable || s.renewable)
/** 彻底停用：已取消，或过期且窗口已关 */
export const isDeadSub = (s: Subscription) => !isHeld(s)

/** 「我的套餐」列出的份：接口顺序，只留还在手上的 */
export const heldSubs = (subs: readonly Subscription[]) => subs.filter(isHeld)

/** 链接尾号：链接最后一段的末 4 位（原型「····a3f9」） */
export function linkTail(url: string | undefined): string {
  if (!url) return ''
  const last = url.replace(/[?#].*$/, '').replace(/\/+$/, '').split('/').pop() ?? ''
  return last.slice(-4)
}

export interface Naming {
  /** 手上不止一份：多份时用名字区分，只有一份时直接说「你的标准版」 */
  multi: boolean
  tail(s: Subscription): string
  /** 短名：备注名；没备注时多份写「标准版 ····a3f9」，一份写「标准版」 */
  sn(s: Subscription): string
  /** 全名：「妈妈的 iPad · 基础版」；没备注写「标准版 ····a3f9」 */
  dn(s: Subscription): string
  /** 句子里的主语：多份「「妈妈的 iPad · 基础版」」，一份「你的标准版」 */
  who(s: Subscription): string
}

export function makeNaming(held: readonly Subscription[], links: readonly SubscriptionLink[] | undefined): Naming {
  const multi = held.length > 1
  const tail = (s: Subscription) => linkTail(links?.find((l) => l.subscription_id === s.id)?.url)
  const withTail = (s: Subscription) => (tail(s) ? `${s.plan_name} ····${tail(s)}` : s.plan_name)
  const dn = (s: Subscription) => (s.label ? `${s.label} · ${s.plan_name}` : withTail(s))
  return {
    multi,
    tail,
    sn: (s) => s.label || (multi ? withTail(s) : s.plan_name),
    dn,
    who: (s) => (multi ? `「${dn(s)}」` : `你的${s.plan_name}`),
  }
}

/** 配置名里的站点名：取服务端配置名的前半段（与下载头同源），拿不到用外观的站点名 */
export function siteNameOf(subs: readonly Subscription[], fallback: string): string {
  const fromServer = subs.find((s) => s.client_name.includes(' · '))?.client_name.split(' · ')[0]
  return fromServer || fallback
}

/** 预览配置名：subscription.ProfileName 的同一规则，只用于还没生效的名字（起名时预览） */
export const profileName = (site: string, label: string, planName: string) => `${site} · ${label.trim() || planName}`

// ---------------------------------------------------------------------------
// 名字：候选名排除别的份已用的（不排除自己）；同款没起名时新买的那份必须起名
// ---------------------------------------------------------------------------
export const NAME_IDEAS = ['我的', '妈妈的 iPad', '爸爸的手机', '工作电脑', '孩子的平板'] as const
/** 另买一份多半是给别人的：建议名不放「我的」（首次点击测试：预填「我的」容易把家人那份起成自己的） */
export const NEW_COPY_IDEAS = NAME_IDEAS.filter((n) => n !== '我的')

export function nameIdeas(subs: readonly Subscription[], self: Subscription | null, list: readonly string[] = NAME_IDEAS): string[] {
  const used = new Set(subs.filter((s) => s !== self && s.label).map((s) => s.label!.toLowerCase()))
  return list.filter((v) => !used.has(v.toLowerCase()))
}

/** 另买一份同款时，App 里会出现两个同名（已有那份没起名、同套餐）：起名必填 */
export const nameRequired = (held: readonly Subscription[], planId: string) => held.some((s) => s.plan_id === planId && !s.label)

// ---------------------------------------------------------------------------
// 流量：原型「剩 8G / 共 100G」；剩余 = 套餐本期剩余 + 这一份的流量包
// ---------------------------------------------------------------------------
export interface Left {
  /** 还能用（字节）；null = 不限 */
  left: number | null
  /** 本期总量 + 流量包；null = 不限 */
  total: number | null
  used: number
  pack: number
}

export function leftOf(sub: Subscription): Left {
  const summary = trafficSummary(pickTrafficQuota(sub.quotas), sub.pack_remaining_bytes)
  if (!summary || summary.total === null) return { left: null, total: null, used: summary?.used ?? 0, pack: sub.pack_remaining_bytes }
  return { left: (summary.remaining ?? 0) + summary.pack, total: summary.total + summary.pack, used: summary.used, pack: summary.pack }
}

/** 流量不足：剩余低于 15%（原型 isLow），此时卡片上「加流量」变成主按钮 */
export function isLow(sub: Subscription): boolean {
  const { left, total } = leftOf(sub)
  return left !== null && total !== null && total > 0 && left / total < 0.15
}

/** 原型的短写法：8G、100G、1.5T；不到 1G 写 MB */
export function gb(bytes: number): string {
  const g = Math.max(0, bytes) / 1024 ** 3
  if (g >= 1024) return `${trim(g / 1024)}T`
  if (g >= 1) return `${trim(g)}G`
  return `${Math.round(Math.max(0, bytes) / 1024 ** 2)}M`
}
const trim = (n: number) => (n >= 10 ? String(Math.round(n)) : String(Math.round(n * 10) / 10))

// ---------------------------------------------------------------------------
// 钱与日期
// ---------------------------------------------------------------------------
export const money = (minor: number) => formatMoney(minor, 'CNY')
/** 价签：整元不带小数（¥30），否则两位（¥42.60） */
export const moneyShort = (minor: number) => (minor % 100 === 0 ? `¥${minor / 100}` : formatMoney(minor, 'CNY'))

/** 「11月19日」；不是今年的带上年份 */
export function day(at: string | Date, now: Date = new Date()): string {
  const d = typeof at === 'string' ? new Date(at) : at
  return `${d.getFullYear() !== now.getFullYear() ? `${d.getFullYear()}年` : ''}${d.getMonth() + 1}月${d.getDate()}日`
}

/** 「10月30日 14:20」：到期精确到分钟（不设宽限期，到点就停） */
export function dayTime(at: string | Date, now: Date = new Date()): string {
  const d = typeof at === 'string' ? new Date(at) : at
  return `${day(d, now)} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

/** 价签后缀：/月、/3 个月、/年 */
export function perPeriod(interval: string, count: number): string {
  if (interval === 'month' && count === 1) return '/月'
  if ((interval === 'year' && count === 1) || (interval === 'month' && count === 12)) return '/年'
  return `/${periodLabel(interval, count)}`
}

/** 到期还剩几天（向上取整）；已过期为负数（过期 3 天 → -3） */
export function daysLeft(end: string | null, now: Date = new Date()): number | null {
  if (!end) return null
  const ms = new Date(end).getTime() - now.getTime()
  return ms > 0 ? Math.ceil(ms / 86_400_000) : -Math.floor(-ms / 86_400_000)
}

/** 「买多久」：1 个月 / 3 个月 / 1 年 */
export function periodLabel(interval: string, count: number): string {
  if (interval === 'month' && count % 12 === 0) return `${count / 12} 年`
  if (interval === 'quarter') return `${count * 3} 个月`
  const unit: Record<string, string> = { day: '天', week: '周', month: '个月', year: '年' }
  if (interval === 'one_time') return '一次'
  return `${count} ${unit[interval] ?? interval}`
}

/** 套餐每期流量的说法：「每月 100G」「每天 2G」「共 50G」「不限流量」 */
export function planTraffic(plan: Pick<Plan, 'quotas' | 'quota_reset_strategy'>): string {
  const q = plan.quotas.find((x) => x.metric === 'traffic.bytes')
  if (!q || q.limit === null) return '不限流量'
  const size = gb(q.limit)
  if (q.period === 'day') return `每天 ${size}`
  if (q.period === 'total') return `共 ${size}`
  if (q.period === 'month' || plan.quota_reset_strategy === 'natural_month' || plan.quota_reset_strategy === 'fixed_day') return `每月 ${size}`
  return `每期 ${size}`
}

/** 设备数：「3 台」「不限台数」 */
export const devicesText = (n: number | null) => (n === null ? '不限台数' : `${n} 台`)

/** 只用于展示「今天起到 11月7日」：与服务端 period.AddInterval 同语义（月按日历月加，溢出顺延） */
export function addPeriod(from: Date, interval: string, count: number): Date {
  const d = new Date(from)
  if (interval === 'day') d.setDate(d.getDate() + count)
  else if (interval === 'week') d.setDate(d.getDate() + 7 * count)
  else if (interval === 'quarter') d.setMonth(d.getMonth() + 3 * count)
  else if (interval === 'year') d.setFullYear(d.getFullYear() + count)
  else d.setMonth(d.getMonth() + count)
  return d
}
