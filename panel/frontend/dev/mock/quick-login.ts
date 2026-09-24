/**
 * [INPUT]: 依赖 node:crypto 的 randomUUID
 * [OUTPUT]: 对外提供 issueQuickLogin、consumeQuickLogin
 * [POS]: dev/mock 的快捷登录令牌表：签发在 portal/account.ts（账号安全），消费在 mock-api.ts 的外壳接口 POST v1/auth/quick-login，两处共用这一份内存状态；60 秒有效、一次性
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomUUID } from 'node:crypto'

const QUICK_LOGIN_TTL_MS = 60_000
const tokens = new Map<string, { userId: string; expires: number }>()

export function issueQuickLogin(userId: string): { token: string; expires: number } {
  const token = randomUUID().replace(/-/g, '')
  const expires = Date.now() + QUICK_LOGIN_TTL_MS
  tokens.set(token, { userId, expires })
  return { token, expires }
}

/** 取走令牌（无论成败都作废），返回用户 id；无效或过期返回 null。 */
export function consumeQuickLogin(token: string): string | null {
  const entry = tokens.get(token)
  tokens.delete(token)
  return entry && entry.expires >= Date.now() ? entry.userId : null
}
