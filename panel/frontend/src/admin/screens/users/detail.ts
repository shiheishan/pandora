import type { ProfileIpActivity, SubscriptionRow, UserRow } from './api'
import {
  currentSubscription,
  extendableSubscriptions,
  isLive,
  isOrderTarget,
  isPermanent,
  planKind,
  rotatableSubscriptions,
  subExpiryView,
  trafficQuota,
  USER_STATUS_VIEW,
  type Tone,
} from './model'

// 用户列表与用户详情页（样稿 admin-v4 #p-users）的展示口径：纯函数，组件只负责摆放

// ===========================================================================
// 状态圆点（kit StatusDot 的四种 tone）
// ===========================================================================
export type DotTone = 'ok' | 'warn' | 'off' | 'bad'

/** 标签色 → 状态圆点：危险是 bad，中性与信息都是 off */
export function dotTone(tone: Tone): DotTone {
  if (tone === 'ok') return 'ok'
  if (tone === 'warn') return 'warn'
  if (tone === 'danger') return 'bad'
  return 'off'
}

/**
 * 列表「状态」列：账号正常但当前订阅已过期时写「已过期」（与「已过期」筛选的 sub_state 同口径），
 * 其余照账号状态
 */
export function listStatusView(u: Pick<UserRow, 'status' | 'current_subscription'>): { label: string; tone: Tone } {
  if (u.status === 'active' && u.current_subscription?.status === 'expired') return { label: '已过期', tone: 'warn' }
  return USER_STATUS_VIEW[u.status]
}

// ===========================================================================
// 详情页头的「续费这一份 / 加流量」：作用在当前生效的那份
// ===========================================================================
type SubPick = Pick<SubscriptionRow, 'status' | 'current_period_end'>

/** 生效中的订阅里到期最晚的那份（多份时取它；没有生效中的返回 undefined） */
export function liveTarget<T extends SubPick>(subs: readonly T[]): T | undefined {
  return currentSubscription(subs.filter((s) => isLive(s.status)))
}

export const NO_LIVE_REASON = '没有生效中的订阅'

/** 页头两个按钮的目标、续费按钮的文案（一次性写「给这份开单」）与禁用原因 */
export function headerTarget<T extends SubPick & Pick<SubscriptionRow, 'quotas'>>(
  subs: readonly T[],
): { target: T | undefined; renewLabel: string; reason: string | null } {
  const target = liveTarget(subs)
  if (!target) return { target: undefined, renewLabel: '续费这一份', reason: NO_LIVE_REASON }
  return { target, renewLabel: planKind(target) === 'one_time' ? '给这份开单' : '续费这一份', reason: null }
}

// ===========================================================================
// 订阅表每行的「操作」菜单：哪些能点、不能点的原因（与原订阅卡同口径）
// ===========================================================================
export type SubActionKey = 'extend' | 'period' | 'traffic' | 'reset' | 'quota' | 'devices' | 'order' | 'rotate'

export interface SubAction {
  key: SubActionKey
  label: string
  /** null 表示可点；否则是灰着时写在菜单项右侧的原因 */
  reason: string | null
}

type SubFull = Pick<SubscriptionRow, 'id' | 'status' | 'current_period_end' | 'quotas'>

/** 一份订阅的全部操作，顺序即菜单顺序；all 是这个用户的全部订阅（换发链接的候选要看全体） */
export function subActions(s: SubFull, all: readonly SubFull[], now: Date): SubAction[] {
  const perm = isPermanent(s.current_period_end, now)
  const active = s.status === 'active'
  const extendable = !perm && extendableSubscriptions([s], now).length > 0
  const quota = trafficQuota(s.quotas)
  const rotatable = rotatableSubscriptions(all).some((r) => r.id === s.id)
  const oneTime = planKind(s) === 'one_time'
  return [
    {
      key: 'extend',
      label: '加时长',
      reason: extendable ? null : perm ? '永久有效' : '已过期满 30 天',
    },
    { key: 'period', label: '改到期', reason: perm ? '永久有效' : active ? null : '不在生效中' },
    { key: 'traffic', label: '加流量', reason: isLive(s.status) ? null : '不在使用中' },
    { key: 'reset', label: '重置流量', reason: active ? null : '不在生效中' },
    { key: 'quota', label: '改流量上限', reason: !active ? '不在生效中' : quota ? null : '没有流量配额' },
    { key: 'devices', label: '改设备上限', reason: null },
    {
      key: 'order',
      label: oneTime ? '给这份开单' : '续费这一份',
      reason: isOrderTarget(s, now) ? null : '已过期满 30 天',
    },
    { key: 'rotate', label: '换发订阅链接', reason: rotatable ? null : '先换发生效中的那份' },
  ]
}

// ===========================================================================
// 资料页「当前套餐」卡的副行
// ===========================================================================
/** 「订阅套餐 · 新周期清零 · 27 天后到期」/「一次性套餐 · 不清零，用完即止 · 永久有效」 */
export function planMetaLine(s: Pick<SubscriptionRow, 'quotas' | 'current_period_end'>, now: Date): string {
  const kind = planKind(s) === 'one_time' ? '一次性套餐 · 不清零，用完即止' : '订阅套餐 · 新周期清零'
  return `${kind} · ${subExpiryView(s.current_period_end, now).text}`
}

// ===========================================================================
// 风控页「最近登录 IP」：画像 ips[] 按最近一次出现倒序
// ===========================================================================
export function recentIps<T extends ProfileIpActivity>(ips: readonly T[]): T[] {
  return [...ips].sort((a, b) => new Date(b.last).getTime() - new Date(a.last).getTime())
}
