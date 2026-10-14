import { randomUUID } from 'node:crypto'

const MAIL_LINK_TTL_MS = 15 * 60 * 1000
const SEND_WINDOW_MS = 15 * 60 * 1000
const MAX_SENDS_PER_EMAIL = 3

const tokens = new Map<string, { userId: string; expires: number }>()
const sendLog = new Map<string, number[]>()
let lastIssued: string | null = null

/** 15 分钟有效；同一用户只留最新一条 */
export function issueMailLink(userId: string): string {
  for (const [t, e] of tokens) if (e.userId === userId) tokens.delete(t)
  const token = randomUUID().replace(/-/g, '')
  tokens.set(token, { userId, expires: Date.now() + MAIL_LINK_TTL_MS })
  lastIssued = token
  return token
}

/** 取走即作废，返回 userId；无效或过期返回 null。 */
export function consumeMailLink(token: string): string | null {
  const entry = tokens.get(token)
  tokens.delete(token)
  return entry && entry.expires >= Date.now() ? entry.userId : null
}

/** 最近签发的那条（只给假后端的 /__mock 路由与测试用） */
export function lastMailLink(): string | null {
  return lastIssued
}

/** 按邮箱 15 分钟最多 3 次 */
export function allowMailLinkSend(email: string): boolean {
  const key = email.trim().toLowerCase()
  const now = Date.now()
  const recent = (sendLog.get(key) ?? []).filter((t) => now - t < SEND_WINDOW_MS)
  if (recent.length >= MAX_SENDS_PER_EMAIL) {
    sendLog.set(key, recent)
    return false
  }
  recent.push(now)
  sendLog.set(key, recent)
  return true
}
