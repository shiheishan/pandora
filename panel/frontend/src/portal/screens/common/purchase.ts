import { formatMoney } from '../../../core/format'
import { isLive, type Subscription, type SubscriptionLink } from './subscriptions'

// 套餐事实的说法搬到了 catalog.ts（选购页与这里共用），这里转出给老的引用处
export { devicesText, gb } from './catalog'

// ---------------------------------------------------------------------------
// 购买流程的公共叫法与小工具（原型 index.html 第 7 节与 sn / dn / who）。
// 叫法：订阅叫「套餐」、数量用「一份」；订阅地址叫「链接」；客户端叫「App」。
// 渲染文字里不出现「订阅、导入、抵扣、折算、剩余价值、降级、客户端、落点」（tests 有扫描）。
// ---------------------------------------------------------------------------

/** 还在手上的一份：生效中，或过期 30 天内还能救回（续费或换套餐都在原链接上） */
export const isHeld = (s: Subscription) => isLive(s) || (s.status === 'expired' && (s.changeable || s.renewable))
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

/** 预览配置名：与订阅模块 render.ProfileName 同一规则，只用于还没生效的名字（起名时预览） */
export const profileName = (site: string, label: string, planName: string) => `${site} · ${label.trim() || planName}`

// ---------------------------------------------------------------------------
// 名字：候选名排除别的份已用的（不排除自己）；同款没起名时新买的那份必须起名
// ---------------------------------------------------------------------------
const NAME_IDEAS = ['我的', '妈妈的 iPad', '爸爸的手机', '工作电脑', '孩子的平板'] as const
/** 另买一份多半是给别人的：建议名不放「我的」（首次点击测试：预填「我的」容易把家人那份起成自己的） */
export const NEW_COPY_IDEAS = NAME_IDEAS.filter((n) => n !== '我的')

export function nameIdeas(subs: readonly Subscription[], self: Subscription | null, list: readonly string[] = NAME_IDEAS): string[] {
  const used = new Set(subs.filter((s) => s !== self && s.label).map((s) => s.label!.toLowerCase()))
  return list.filter((v) => !used.has(v.toLowerCase()))
}

/** 另买一份同款时，App 里会出现两个同名（已有那份没起名、同套餐）：起名必填 */
export const nameRequired = (held: readonly Subscription[], planId: string) => held.some((s) => s.plan_id === planId && !s.label)

// ---------------------------------------------------------------------------
// 流量：原型「剩 8G / 共 100G」。订阅套餐是本期，一次性套餐是总量；送的流量算在里面
// （订阅套餐的送流量只在本期有效）。能用的 = 上限 + 送的 − 已用。
// ---------------------------------------------------------------------------
interface Left {
  /** 还能用（字节）；null = 不限 */
  left: number | null
  /** 上限 + 送的；null = 不限 */
  total: number | null
  used: number
  /** 送的流量 */
  bonus: number
}

export function leftOf(sub: Pick<Subscription, 'traffic_limit' | 'bonus_bytes' | 'traffic_used'>): Left {
  const { traffic_limit: limit, bonus_bytes: bonus, traffic_used: used } = sub
  if (limit === null) return { left: null, total: null, used, bonus }
  const total = limit + bonus
  return { left: Math.max(0, total - used), total, used, bonus }
}

/** 「流量快用完」的提醒线：本期（一次性为总量）已用过 80%，含送的流量（用户 10-16 定） */
export const LOW_USED_RATIO = 0.8

/** 流量快用完：已用 ≥ 80%（含送的）；不限流量的不算 */
export function isLow(sub: Pick<Subscription, 'traffic_limit' | 'bonus_bytes' | 'traffic_used'>): boolean {
  const { used, total } = leftOf(sub)
  return total !== null && (total <= 0 || used / total >= LOW_USED_RATIO)
}

/** 付费流量重置还能买几次（本期 / 有效期内）：没开为 null，不限次数为 Infinity */
export function resetsLeft(sub: Pick<Subscription, 'reset_price' | 'reset_limit' | 'resets_used'>): number | null {
  if (sub.reset_price === null) return null
  if (sub.reset_limit === null) return Number.POSITIVE_INFINITY
  return Math.max(0, sub.reset_limit - sub.resets_used)
}

// ---------------------------------------------------------------------------
// 钱与日期
// ---------------------------------------------------------------------------
export const money = (minor: number) => formatMoney(minor)
/** 价签：整元不带小数（¥30），否则两位（¥42.60） */
export const moneyShort = (minor: number) => (minor % 100 === 0 ? `¥${minor / 100}` : formatMoney(minor))

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

/** 价签后缀：/月、/3 个月、/半年、/年；一次性为空串 */
export function perPeriod(interval: string, count: number): string {
  if (interval === 'one_time') return ''
  if (interval === 'month' && count === 1) return '/月'
  if (interval === 'month' && count === 6) return '/半年'
  if ((interval === 'year' && count === 1) || (interval === 'month' && count === 12)) return '/年'
  return `/${periodLabel(interval, count)}`
}

/** 到期还剩几天（向上取整）；已过期为负数（过期 3 天 → -3） */
export function daysLeft(end: string | null, now: Date = new Date()): number | null {
  if (!end) return null
  const ms = new Date(end).getTime() - now.getTime()
  return ms > 0 ? Math.ceil(ms / 86_400_000) : -Math.floor(-ms / 86_400_000)
}

/** 「买多久」：1 个月 / 3 个月 / 半年 / 1 年；一次性为「一次」 */
export function periodLabel(interval: string, count: number): string {
  if (interval === 'one_time') return '一次'
  if (interval === 'month' && count % 12 === 0) return `${count / 12} 年`
  if (interval === 'month' && count === 6) return '半年'
  const unit: Record<string, string> = { day: '天', month: '个月', year: '年' }
  return `${count} ${unit[interval] ?? interval}`
}

/** 只用于展示「今天起到 11月7日」：与服务端 period.AddInterval 同语义（月按日历月加，溢出顺延） */
export function addPeriod(from: Date, interval: string, count: number): Date {
  const d = new Date(from)
  if (interval === 'day') d.setDate(d.getDate() + count)
  else if (interval === 'year') d.setFullYear(d.getFullYear() + count)
  else d.setMonth(d.getMonth() + count)
  return d
}

/** 买这一档从 from 起到哪天：一次性套餐按有效天数加，永久为 null；订阅套餐同 addPeriod（只用于展示） */
export function endAfter(from: Date, p: { billing_interval: string; interval_count: number; validity_days: number | null }): Date | null {
  if (p.billing_interval !== 'one_time') return addPeriod(from, p.billing_interval, p.interval_count)
  if (p.validity_days === null) return null
  const d = new Date(from)
  d.setDate(d.getDate() + p.validity_days)
  return d
}
