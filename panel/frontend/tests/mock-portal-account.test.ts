import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · portal account', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('portal'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.portal)).access_token)
  })
  afterAll(() => close(server))

  it('R114: sessions carry last_seen_at and come back most recently active first', async () => {
    const { sessions } = (await (await mockFetch(base, auth, 'GET', '/v1/me/sessions')).json()) as { sessions: Array<{ current: boolean; created_at: string; last_seen_at: string }> }
    expect(sessions.every((s) => typeof s.last_seen_at === 'string')).toBe(true)
    expect(sessions[0]!.current).toBe(true)
    const seen = sessions.map((s) => s.last_seen_at)
    expect(seen).toEqual([...seen].sort().reverse())
    // 种子会话的最近活跃晚于登录时间
    expect(sessions.some((s) => !s.current && s.last_seen_at > s.created_at)).toBe(true)
  })

  it('Telegram：默认场景未绑定、给机器人用户名；绑定码 8 位、10 分钟后过期', async () => {
    const tg = (await (await mockFetch(base, auth, 'GET', '/v1/me/telegram')).json()) as Record<string, unknown>
    expect(tg).toMatchObject({ bound: false, enabled: true, bot_username: expect.any(String) })
    expect(tg.username).toBeUndefined()
    const before = Date.now()
    const code = (await (await mockFetch(base, auth, 'POST', '/v1/me/telegram/bind-code')).json()) as { code: string; bot_username: string; expires_at: string }
    expect(code.code).toMatch(/^[ABCDEFGHJKMNPQRSTUVWXYZ23456789]{8}$/)
    const ttl = Date.parse(code.expires_at) - before
    expect(ttl).toBeGreaterThan(9 * 60_000)
    expect(ttl).toBeLessThanOrEqual(10 * 60_000 + 1000)
  })
})
