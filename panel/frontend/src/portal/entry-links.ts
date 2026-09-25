/**
 * [INPUT]: 依赖浏览器 location / history / sessionStorage（均可注入，便于测试）
 * [OUTPUT]: 对外提供 INVITE_STORAGE_KEY、takeInviteFromUrl、readStoredInvite、quickLoginLink、quickLoginTokenFromHash、quickLoginTokenFromInput
 * [POS]: portal 入口页加载时的两类外来链接：邀请链接 /?invite=CODE（转大写存 sessionStorage 后抹掉查询串）与快捷登录 /#/quick-login/<token>（令牌在 hash 里，不进服务器与 nginx 日志）；账号安全页生成链接用同文件的 quickLoginLink，生成与识别同一格式
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { matchPath, parseHash } from '../core/router'

export const INVITE_STORAGE_KEY = 'pandora-invite'

interface Env {
  location: Pick<Location, 'search' | 'pathname' | 'hash'>
  history: Pick<History, 'replaceState'>
  storage: Pick<Storage, 'getItem' | 'setItem'> | null
}

function sessionStore(): Env['storage'] {
  try {
    return window.sessionStorage
  } catch {
    return null
  }
}

const browserEnv = (): Env => ({ location: window.location, history: window.history, storage: sessionStore() })

/**
 * 邀请链接用 /?invite= 而不是 /r/CODE（两段路径会撞订阅通配，契约 1.1）。
 * 读到就存起来并从地址栏抹掉，返回邀请码；没有返回 null。
 */
export function takeInviteFromUrl(env: Env = browserEnv()): string | null {
  const params = new URLSearchParams(env.location.search)
  const raw = params.get('invite')?.trim()
  if (!raw) return null
  const code = raw.toUpperCase()
  try {
    env.storage?.setItem(INVITE_STORAGE_KEY, code)
  } catch {
    // 存不进去也照样预填本次
  }
  params.delete('invite')
  const rest = params.toString()
  env.history.replaceState(null, '', `${env.location.pathname}${rest ? `?${rest}` : ''}${env.location.hash}`)
  return code
}

export function readStoredInvite(env: Env = browserEnv()): string | null {
  try {
    return env.storage?.getItem(INVITE_STORAGE_KEY) ?? null
  } catch {
    return null
  }
}

/**
 * 账号安全页生成的快捷登录链接（契约门户-10）：相对入口页解析，门户在子路径下也成立；
 * 不用 /q/<token>，两段式路径会撞订阅通配，令牌放 fragment 也不进访问日志与 Referer。
 */
export function quickLoginLink(token: string, base: string = window.location.href): string {
  return new URL(`./#/quick-login/${encodeURIComponent(token)}`, base).href
}

/** #/quick-login/<token> → token；其它地址返回 null。 */
export function quickLoginTokenFromHash(hash: string): string | null {
  return matchPath('/quick-login/:token', parseHash(hash).path)?.token ?? null
}

/** 用户粘贴的可能是整条链接，也可能只是令牌。 */
export function quickLoginTokenFromInput(input: string): string | null {
  const text = input.trim()
  if (!text) return null
  const hashAt = text.indexOf('#')
  if (hashAt >= 0) return quickLoginTokenFromHash(text.slice(hashAt))
  return /^[A-Za-z0-9_-]+$/.test(text) ? text : null
}
