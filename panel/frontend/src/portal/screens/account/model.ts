/**
 * [INPUT]: 依赖 ../../../core/api 的 ApiError
 * [OUTPUT]: 对外提供 deviceName、validateNewPassword、passwordErrors / PasswordErrors、PREF_CATEGORIES / PREF_CHANNELS / PREF_ROWS / PrefCategory / PrefChannel、secondsLeft、formatCountdown、shortUserId、telegramDeepLink、sortSessions
 * [POS]: portal/screens/account 的纯逻辑（契约门户-10）：会话设备名由 user_agent 推出「浏览器 / 客户端 · 系统」；改密的本地校验（≥ 8 字符、字母与数字、≤ 256 字节）与错误落位（401 当前密码不正确落到当前密码框，fields.password 落到新密码框）；通知偏好三行 × 两列；快捷登录与绑定码的倒计时；有单元测试
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { ApiError } from '../../../core/api'

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

export function deviceName(ua: string): string {
  const client = CLIENTS.find(([re]) => re.test(ua))?.[1]
  const system = SYSTEMS.find(([re]) => re.test(ua))?.[1]
  if (client && system) return `${client} · ${system}`
  return client ?? system ?? '未知设备'
}

/** 当前会话排最前，其余按登录时间倒序（后端按 last_seen_at，而它现在等于 created_at，修订 R62） */
export function sortSessions<T extends { current: boolean; created_at: string }>(list: readonly T[]): T[] {
  return [...list].sort((a, b) => Number(b.current) - Number(a.current) || b.created_at.localeCompare(a.created_at))
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
// 通知偏好：后端固定 3 类 × 2 渠道；同一类里的到期、流量、工单回复只能一起开关
// ---------------------------------------------------------------------------
export const PREF_CATEGORIES = ['transactional', 'service', 'marketing'] as const
export const PREF_CHANNELS = ['email', 'telegram'] as const
export type PrefCategory = (typeof PREF_CATEGORIES)[number]
export type PrefChannel = (typeof PREF_CHANNELS)[number]

export const PREF_ROWS: ReadonlyArray<{ category: PrefCategory; label: string; hint: string }> = [
  { category: 'transactional', label: '交易通知', hint: '支付成功等，不可关闭' },
  { category: 'service', label: '服务提醒', hint: '到期、流量、工单回复' },
  { category: 'marketing', label: '营销与群发', hint: '活动与站点群发' },
]

// ---------------------------------------------------------------------------
// 倒计时
// ---------------------------------------------------------------------------
export const secondsLeft = (deadline: number, now: number) => Math.max(0, Math.ceil((deadline - now) / 1000))

/** 125 → 「2:05」 */
export const formatCountdown = (seconds: number) => `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`

/** 设计稿的数字编号后端没有；与后台一致显示 uuid 前 8 位（契约后台-03 通用事实） */
export const shortUserId = (id: string) => id.slice(0, 8)

/** 点开后 Telegram 自动发送 /start CODE（契约门户-10 绑定码映射） */
export const telegramDeepLink = (bot: string, code: string) => `https://t.me/${encodeURIComponent(bot)}?start=${encodeURIComponent(code)}`
