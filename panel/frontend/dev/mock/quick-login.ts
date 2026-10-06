import { randomUUID } from 'node:crypto'

const QUICK_LOGIN_TTL_MS = 60_000
const tokens = new Map<string, { userId: string; session: string; expires: number }>()

/** session 是签发会话的键（外壳令牌）：同一会话只留最新一条，旧令牌随即作废 */
export function issueQuickLogin(userId: string, session: string): { token: string; expires: number } {
  for (const [t, e] of tokens) if (e.session === session) tokens.delete(t)
  const token = randomUUID().replace(/-/g, '')
  const expires = Date.now() + QUICK_LOGIN_TTL_MS
  tokens.set(token, { userId, session, expires })
  return { token, expires }
}

/** 取走令牌（无论成败都作废），返回用户 id；无效或过期返回 null。 */
export function consumeQuickLogin(token: string): string | null {
  const entry = tokens.get(token)
  tokens.delete(token)
  return entry && entry.expires >= Date.now() ? entry.userId : null
}
