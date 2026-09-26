/**
 * [INPUT]: 依赖 vitest，依赖 ./mock-helpers 的 serve / close / loginAs / bearer / mockFetch，依赖 ../dev/mock-api 的 MOCK_ACCOUNTS
 * [OUTPUT]: 对外提供门户账号安全假接口的测试
 * [POS]: tests 的门户账号安全假后端守卫（R114）：会话列表每条都带 last_seen_at（account/api.ts 带 tsx 依赖进不了 node 侧类型检查，按字段断言），并按最近活跃倒序（当前会话刚刷新，排最前）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
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
})
