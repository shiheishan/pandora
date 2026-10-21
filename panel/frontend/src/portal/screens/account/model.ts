import { ApiError } from '../../../core/api'
import { relativeTime } from '../../../core/format'

// ---------------------------------------------------------------------------
// 设备名：门户会话都是网页登录，UA 多为浏览器；认不出的给「未知设备」
// ---------------------------------------------------------------------------
const CLIENTS: ReadonlyArray<readonly [RegExp, string]> = [
  [/Shadowrocket/i, 'Shadowrocket'],
  [/Hiddify/i, 'Hiddify'],
  [/clash-verge|Clash Verge/i, 'Clash Verge'],
  [/sing-box|SFA|SFI|SFM/, 'sing-box'],
  [/Stash/i, 'Stash'],
  [/v2rayN/i, 'v2rayN'],
  [/Edg(e|A|iOS)?\//, 'Edge'],
  [/OPR\/|Opera/, 'Opera'],
  [/Firefox\/|FxiOS\//, 'Firefox'],
  [/Chrome\/|CriOS\//, 'Chrome'],
  [/Safari\//, 'Safari'],
]

const SYSTEMS: ReadonlyArray<readonly [RegExp, string]> = [
  [/iPad/, 'iPad'],
  [/iPhone|iOS/, 'iPhone'],
  [/HarmonyOS|OpenHarmony/, 'HarmonyOS'],
  [/Android/, 'Android'],
  [/Windows/, 'Windows'],
  [/Mac OS X|Macintosh|macOS/, 'macOS'],
  [/CrOS/, 'ChromeOS'],
  [/Linux/, 'Linux'],
]

/** 设备图标：手机类系统用手机，其余（含认不出的）用电脑 */
export function deviceKind(ua: string): 'phone' | 'desk' {
  return /iPhone|iPad|iOS|Android|HarmonyOS|OpenHarmony/.test(ua) ? 'phone' : 'desk'
}

export function deviceName(ua: string): string {
  const client = CLIENTS.find(([re]) => re.test(ua))?.[1]
  const system = SYSTEMS.find(([re]) => re.test(ua))?.[1]
  if (client && system) return `${client} · ${system}`
  return client ?? system ?? '未知设备'
}

/** 会话的「最近活跃」：刚刚活跃 / N 分钟前活跃 / N 小时前活跃 / MM-DD 活跃（R114） */
export function lastSeenLabel(at: string, now: Date = new Date()): string {
  const rel = relativeTime(at, now)
  if (rel === '刚刚') return '刚刚活跃'
  return /分钟|小时/.test(rel) ? `${rel}前活跃` : `${rel} 活跃`
}

/** 登录时间「MM-DD HH:mm」（样稿 `登录于 10-16 09:12`），按浏览器本地时区 */
export function loginAtLabel(at: string): string {
  const d = new Date(at)
  if (Number.isNaN(d.getTime())) return at
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

/** 当前会话排最前，其余按最近活跃倒序（与后端同口径，R62 / R114） */
export function sortSessions<T extends { current: boolean; last_seen_at: string }>(list: readonly T[]): T[] {
  return [...list].sort((a, b) => Number(b.current) - Number(a.current) || b.last_seen_at.localeCompare(a.last_seen_at))
}

// ---------------------------------------------------------------------------
// 改密码（契约门户-10 POST v1/me/password）
// ---------------------------------------------------------------------------
/** 与后端 crypto.ValidatePassword 同一套规则与先后：≥ 8 字符、≤ 256 字节、同时含字母和数字（unicode.IsLetter / IsDigit）；通过返回 null */
export function validateNewPassword(pw: string): string | null {
  if ([...pw].length < 8) return '密码至少需要 8 个字符'
  if (new TextEncoder().encode(pw).length > 256) return '密码过长'
  if (!/\p{L}/u.test(pw) || !/\p{Nd}/u.test(pw)) return '密码必须同时包含字母和数字'
  return null
}

export interface PasswordErrors {
  old?: string
  next?: string
  /** 落不到字段上的（断网、5xx 等） */
  form?: string
}

/**
 * 错误落位：401 是「当前密码不正确」（不是会话失效，api 的 passwordCheck 已拦下登出）；
 * 400 是新旧相同；422 的 fields 键名是 old_password / new_password（任一为空时两个都回）与 password（新密码规则）。
 */
export function passwordErrors(error: unknown): PasswordErrors {
  if (!(error instanceof ApiError)) return { form: '修改失败，请稍后重试' }
  if (error.status === 401) return { old: error.message || '当前密码不正确' }
  if (error.status === 400) return { next: error.message }
  const f = error.fields
  if (f.old_password || f.new_password || f.password) {
    return { ...(f.old_password ? { old: f.old_password } : {}), ...(f.password || f.new_password ? { next: f.password ?? f.new_password } : {}) }
  }
  return { form: error.message || '修改失败，请稍后重试' }
}

// ---------------------------------------------------------------------------
// 通知偏好：后端固定 5 项（3 类 × 2 渠道，营销类只发邮件）；同一类里的到期、流量、工单回复只能一起开关
// ---------------------------------------------------------------------------
export const PREF_CATEGORIES = ['transactional', 'service', 'marketing'] as const
export const PREF_CHANNELS = ['email', 'telegram'] as const
export type PrefCategory = (typeof PREF_CATEGORIES)[number]
export type PrefChannel = (typeof PREF_CHANNELS)[number]

export const PREF_ROWS: ReadonlyArray<{
  category: PrefCategory
  label: string
  hint: string
  channels: ReadonlyArray<PrefChannel>
}> = [
  { category: 'transactional', label: '交易通知', hint: '支付成功等，不能关', channels: ['email', 'telegram'] },
  { category: 'service', label: '服务提醒', hint: '到期、流量、工单回复', channels: ['email', 'telegram'] },
  { category: 'marketing', label: '活动与群发', hint: '优惠活动、站点群发，只发邮件', channels: ['email'] },
]

// ---------------------------------------------------------------------------
// 倒计时
// ---------------------------------------------------------------------------
export const secondsLeft = (deadline: number, now: number) => Math.max(0, Math.ceil((deadline - now) / 1000))

/** 后端 BindCodeTTL：绑定码固定 10 分钟 */
export const BIND_CODE_TTL_MS = 10 * 60_000

/**
 * 绑定码的本地截止时刻（审计 C3）：取码响应只有绝对时间 expires_at，本机时钟有偏差时直接倒数会错。
 * 按收到响应的本地时刻起算，剩余时长夹在 (0, 10 分钟] 里；落在外面（时钟偏差、解析失败）就按 10 分钟算。
 */
export function bindDeadline(expiresAt: string, receivedAt: number): number {
  const remaining = Date.parse(expiresAt) - receivedAt
  return receivedAt + (remaining > 0 && remaining <= BIND_CODE_TTL_MS ? remaining : BIND_CODE_TTL_MS)
}

/** 125 → 「2:05」 */
export const formatCountdown = (seconds: number) => `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`

/** 设计稿的数字编号后端没有；与后台一致显示 uuid 前 8 位（契约后台-03 通用事实） */
export const shortUserId = (id: string) => id.slice(0, 8)

/** 点开后 Telegram 自动发送 /start CODE（契约门户-10 绑定码映射） */
export const telegramDeepLink = (bot: string, code: string) => `https://t.me/${encodeURIComponent(bot)}?start=${encodeURIComponent(code)}`
