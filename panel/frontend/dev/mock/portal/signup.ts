import type { Json } from '../types.ts'
import { onScenarioChange } from './scenario.ts'

// ---------------------------------------------------------------------------
// 门户假后端的注册与发信配置：GET v1/site-config、v1/signup-config、注册 / 找回 / 邮件登录第 1 步、
// GET v1/me/invite 的 single_use 都按它回。
// 缺省与 Go 一致：开放注册、不验证邮箱、邀请码永不失效、两个降级开关都没停用。
// 两处是演示环境的取舍（Go 缺省不同，真实环境由后台决定）：当作已配好发信（password_reset 为真）、
// 开了邮件链接登录（Go 的 signup.mail_link_login 缺省关），好让找回密码与邮件登录在演示里能走。
// 后台假后端是另一个 vite 进程，后台里改注册方式、降级开关传不到这里；测试与演示用
// POST /__mock/portal-signup 改（只在假后端里，不进 Go），切门户场景时恢复缺省。
// ---------------------------------------------------------------------------

export type RegistrationMode = 'open' | 'invite_only' | 'closed'

export interface PortalSignup {
  /** 设置里的注册方式（Go settings 的 registration_mode） */
  registration_mode: RegistrationMode
  /** 注册要不要验证邮箱 */
  email_verification: boolean
  /** 降级开关 auth.registration 停用（后台「暂停新用户注册」打开）：对外注册方式一律 closed */
  registration_paused: boolean
  /** 降级开关 notify.email 停用（后台「暂停邮件投递」打开）：要发信的第 1 步回 503，找回密码与邮件登录入口不给 */
  mail_paused: boolean
  /** 设置「邀请码永不失效」；关掉时个人邀请码用一次就失效（GET v1/me/invite 的 single_use） */
  invite_never_expire: boolean
  /** 设置「邮件链接登录」 */
  mail_link_login: boolean
}

const DEFAULTS: PortalSignup = {
  registration_mode: 'open',
  email_verification: false,
  registration_paused: false,
  mail_paused: false,
  invite_never_expire: true,
  mail_link_login: true,
}

let state: PortalSignup = { ...DEFAULTS }

onScenarioChange(() => {
  state = { ...DEFAULTS }
})

/** 当前配置（只读快照） */
export const portalSignup = (): Readonly<PortalSignup> => state

/** 对外的注册方式：同 Go RegistrationPolicy，auth.registration 停用时一律 closed */
export const effectiveRegistrationMode = (): RegistrationMode => (state.registration_paused ? 'closed' : state.registration_mode)

/** 个人邀请码是否用一次就失效：同 Go personalCodeSingleUse */
export const personalCodeSingleUse = (): boolean => !state.invite_never_expire

const BOOL_KEYS = ['email_verification', 'registration_paused', 'mail_paused', 'invite_never_expire', 'mail_link_login'] as const

/**
 * POST /__mock/portal-signup 的请求体：只改给出的键，`reset: true` 先恢复缺省再改。
 * 有不认识的键或类型不对时什么都不改，回字段错误（键名照请求体）。
 */
export function patchPortalSignup(body: Json): Record<string, string> | null {
  const fields: Record<string, string> = {}
  for (const [k, v] of Object.entries(body)) {
    if (k === 'reset') {
      if (typeof v !== 'boolean') fields[k] = '必须是布尔值'
    } else if (k === 'registration_mode') {
      if (v !== 'open' && v !== 'invite_only' && v !== 'closed') fields[k] = '只能是 open / invite_only / closed'
    } else if ((BOOL_KEYS as readonly string[]).includes(k)) {
      if (typeof v !== 'boolean') fields[k] = '必须是布尔值'
    } else {
      fields[k] = '不认识的字段'
    }
  }
  if (Object.keys(fields).length > 0) return fields
  const next: PortalSignup = body.reset === true ? { ...DEFAULTS } : { ...state }
  if (typeof body.registration_mode === 'string') next.registration_mode = body.registration_mode as RegistrationMode
  for (const k of BOOL_KEYS) if (typeof body[k] === 'boolean') next[k] = body[k]
  state = next
  return null
}
