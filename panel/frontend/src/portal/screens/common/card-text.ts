import { gbParts } from './catalog'
import { day, dayTime, daysLeft, gb, isLow, leftOf, moneyShort, perPeriod, resetsLeft } from './purchase'
import type { Subscription, SubscriptionLink } from './subscriptions'

// 我的套餐里一份的文字（样稿 v5 #p-subs 的主卡、切换条、「这一份」操作行）；只拼字，不取数

const HOUR_MS = 3_600_000
const DAY_MS = 86_400_000

/** 过期了（状态已翻成 expired，或过期扫描还没赶上、周期末已过）：链接暂停，续费后自动恢复；一次性永久不看哨兵日期 */
export function isExpiredNow(sub: Pick<Subscription, 'status' | 'current_period_end' | 'permanent'>, now: Date = new Date()): boolean {
  if (sub.permanent) return sub.status === 'expired'
  return sub.status === 'expired' || (sub.current_period_end !== null && new Date(sub.current_period_end).getTime() <= now.getTime())
}

interface ExpiryText {
  main: string
  /** 第二行：到期时刻 */
  sub: string
  tone: 'ok' | 'warn' | 'danger'
}

/**
 * 到期（主卡右上角的胶囊）：7 天以上「还剩 23 天」，7 天内「5 天后到期」（警示），
 * 最后 24 小时「还剩 6 小时」，过了「已过期 3 天」；第二行是精确到分钟的到期时刻。
 */
export function expiryText(
  sub: Pick<Subscription, 'status' | 'current_period_end' | 'permanent'>,
  now: Date = new Date(),
): ExpiryText {
  if (sub.permanent) return { main: '永久有效', sub: '', tone: 'ok' }
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

/** 流量额度的叫法：一次性「总」；订阅套餐「本期」 */
export function periodWord(sub: Pick<Subscription, 'plan_kind'>): string {
  return sub.plan_kind === 'one_time' ? '总' : '本期'
}

/** 近 24 小时的来源数超过设备上限：可能泄露（过期的不提示） */
export function leakSources(sub: Subscription, link: SubscriptionLink | undefined): number | null {
  if (!link || isExpiredNow(sub) || sub.device_limit === null) return null
  return link.distinct_sources_24h > sub.device_limit ? link.distinct_sources_24h : null
}

// ---------------------------------------------------------------------------
// 主卡（样稿 .hero）
// ---------------------------------------------------------------------------

/** 眉标（样稿 .eyebrow）：有备注名时写「站点 · 套餐名」，一次性没备注写「站点 · 一次性」，否则只写站点 */
export function eyebrowText(site: string, sub: Pick<Subscription, 'label' | 'plan_name' | 'plan_kind'>): string {
  if (sub.label) return `${site} · ${sub.plan_name}`
  if (sub.plan_kind === 'one_time') return `${site} · 一次性`
  return site
}

/** 贴纸上的名字（卡片标题，浏览器测试按它找卡片）：备注名，没有就是套餐名 */
export const titleOf = (sub: Pick<Subscription, 'label' | 'plan_name'>) => sub.label || sub.plan_name

/** 右上角胶囊：过期写「已到期」，其余同 expiryText 的主句 */
export function pillText(sub: Pick<Subscription, 'status' | 'current_period_end' | 'permanent'>, now: Date = new Date()): string {
  return isExpiredNow(sub, now) ? '已到期' : expiryText(sub, now).main
}

export interface HeroTraffic {
  /** 大数字：剩余流量拆成数和单位（「208」「GB」）；不限流量为 null */
  big: { value: string; unit: string } | null
  /** 进度条：剩余占比 0–1（样稿画的是剩余）；不限流量为 null */
  ratio: number | null
  /** 已用百分比（整数、向下取整、最多 100）；不限流量为 null */
  usedPct: number | null
  /** 大数字下面那行的开头：「本期剩余」/「剩余」 */
  scope: string
  /** 总量「520G」（上限 + 送的）；不限流量为 null */
  total: string | null
  /** 送的流量「20G」；没有为 null */
  bonus: string | null
  /** 送的那句的尾巴：订阅套餐「本期有效」（送的只在本期有效），一次性为空串 */
  bonusNote: string
  /** 一次性套餐的说明「不清零，用完为止」；订阅套餐为空串 */
  tail: string
}

/** 主卡的流量：剩余 = 上限 + 送的 − 已用（leftOf）；订阅套餐的送流量注明「本期有效」 */
export function heroTraffic(sub: Pick<Subscription, 'plan_kind' | 'traffic_limit' | 'bonus_bytes' | 'traffic_used'>): HeroTraffic {
  const { left, total, used, bonus } = leftOf(sub)
  const once = sub.plan_kind === 'one_time'
  const base = {
    scope: once ? '剩余' : '本期剩余',
    bonus: bonus > 0 ? gb(bonus) : null,
    bonusNote: bonus > 0 && !once ? '本期有效' : '',
    tail: once ? '不清零，用完为止' : '',
  }
  if (left === null || total === null) return { ...base, big: null, ratio: null, usedPct: null, total: null }
  return {
    ...base,
    big: gbParts(left),
    ratio: total > 0 ? left / total : 0,
    usedPct: total > 0 ? Math.min(100, Math.floor((used / total) * 100)) : 100,
    total: gb(total),
  }
}

/** 主卡底下那行灰字：「11月27日 16:03 到期 · 3 / 5 台设备在线」；永久「没有到期日 · …」；不限台数「3 台设备在线 · 不限台数」 */
export function metaLine(sub: Pick<Subscription, 'permanent' | 'current_period_end' | 'online_devices' | 'device_limit'>, now: Date = new Date()): string {
  const when = sub.permanent ? '没有到期日' : sub.current_period_end ? `${dayTime(sub.current_period_end, now)} 到期` : '长期有效'
  const devices = sub.device_limit === null ? `${sub.online_devices} 台设备在线 · 不限台数` : `${sub.online_devices} / ${sub.device_limit} 台设备在线`
  return `${when} · ${devices}`
}

/** 已到期的大字（样稿 .gone）：「10月2日到期了」 */
export function goneText(sub: Pick<Subscription, 'current_period_end'>, now: Date = new Date()): string {
  return sub.current_period_end ? `${day(sub.current_period_end, now)}到期了` : '已经到期了'
}

/**
 * 流量快用完的就地提示（样稿 .low-tip）：已用过 80%（isLow）才有，过期、不限流量的没有。
 * 「到下一期恢复」只在下一次清零（next_reset_at）早于到期日时成立；最后一期写「续上后恢复」，不能续的不写怎么恢复。
 */
export function lowTip(sub: Subscription, now: Date = new Date()): { title: string; text: string } | null {
  if (isExpiredNow(sub, now) || !isLow(sub)) return null
  const pct = heroTraffic(sub).usedPct ?? 100
  let recover = ''
  if (sub.plan_kind === 'subscription') {
    const nextInPeriod = sub.next_reset_at !== null && sub.current_period_end !== null && new Date(sub.next_reset_at) < new Date(sub.current_period_end)
    recover = nextInPeriod ? '到下一期恢复' : sub.renew_until !== null ? '续上后恢复' : ''
  }
  const scope = sub.plan_kind === 'one_time' ? '' : '本期'
  if (pct >= 100) return { title: '流量用完了', text: `节点已经停了${recover ? `，${recover}` : ''}` }
  return { title: '流量快用完了', text: `${scope}已用 ${pct}%，用完会停${recover ? `，${recover}` : ''}` }
}

/**
 * 主卡的续费按钮：生效中「续费 · 到 12月27日」；已到期「续费，恢复使用 · ¥59/月」，
 * 续费价那档已下架或拿不到时写「续费，恢复使用 · 续到 …」（确认页会默认别的档，按钮价对不上）。没有 renew_until 不能续，返回 null。
 */
export function renewText(sub: Subscription, now: Date = new Date()): string | null {
  if (sub.renew_until === null) return null
  if (!isExpiredNow(sub, now)) return `续费 · 到 ${day(sub.renew_until, now)}`
  const p = sub.renewal_price
  if (p?.available && p.billing_interval !== 'one_time') return `续费，恢复使用 · ${moneyShort(p.unit_amount)}${perPeriod(p.billing_interval, p.interval_count)}`
  return `续费，恢复使用 · 续到 ${day(sub.renew_until, now)}`
}

/** 订阅套餐生效中但不再续费（归档、关了续费）：到期后结束，给「换个套餐」的出路 */
export function noRenewNote(sub: Subscription, now: Date = new Date()): string | null {
  return sub.plan_kind === 'subscription' && !isExpiredNow(sub, now) && !sub.renewable ? '这个套餐不再续费，到期后结束，可以换个套餐。' : null
}

// ---------------------------------------------------------------------------
// 多份切换条（样稿 .copies .cp）
// ---------------------------------------------------------------------------

/** 切换条上每份的小字：「208G · 42 天」「63G · 永久」「已到期」 */
export function copyTabText(sub: Subscription, now: Date = new Date()): string {
  if (isExpiredNow(sub, now)) return '已到期'
  const { left } = leftOf(sub)
  const traffic = left === null ? '不限流量' : gb(left)
  if (sub.permanent) return `${traffic} · 永久`
  const d = daysLeft(sub.current_period_end, now)
  return d === null ? `${traffic} · 长期` : `${traffic} · ${d} 天`
}

// ---------------------------------------------------------------------------
// 「这一份」操作行（样稿 .rows）
// ---------------------------------------------------------------------------

/** 「换个套餐」行的小字（审计 #16）：订阅套餐按剩余时间；一次性取剩余流量与天数里少的，永久的只看流量 */
export function changeHint(sub: Pick<Subscription, 'plan_kind' | 'permanent'>): string {
  if (sub.plan_kind === 'subscription') return '差价按剩余时间算，换之前先看报价'
  return sub.permanent ? '差价按剩余流量算，换之前先看报价' : '差价按剩余流量和天数算（取少的），换之前先看报价'
}

export interface ResetRowText {
  title: string
  sub: string
  /** 价签「¥15」 */
  price: string
  /** 次数用完时不能点 */
  open: boolean
}

/**
 * 「重置本期流量」行：没开付费重置、已到期或不限流量时没有这一行。重置只把已用清零，送的流量保留，
 * 所以「马上能用满」写上限 + 送的（leftOf 的 total）。
 */
export function resetRow(sub: Subscription, now: Date = new Date()): ResetRowText | null {
  if (sub.reset_price === null || isExpiredNow(sub, now)) return null
  const { total } = leftOf(sub)
  if (total === null) return null
  const once = sub.plan_kind === 'one_time'
  const scope = once ? '有效期内' : '本期'
  const left = resetsLeft(sub)
  const title = once ? '重置流量' : '重置本期流量'
  const price = moneyShort(sub.reset_price)
  if (left === 0) return { title, sub: `${scope}的 ${sub.reset_limit} 次已用完`, price, open: false }
  const times = left === null || left === Number.POSITIVE_INFINITY ? '' : ` · ${scope}还能买 ${left} 次`
  return { title, sub: `已用的清零，马上能用满 ${gb(total)}${times}`, price, open: true }
}
