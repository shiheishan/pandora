import { describe, expect, it, vi } from 'vitest'
import { createApiClient } from '../core/api'
import { createTokenStore } from '../core/token'
import { consumeQuickLogin } from './AuthPage'
import { consumeMailLogin } from './MailLinkLogin'

// 登录类接口在响应里 Set-Cookie（refresh 值）。fetch 用 credentials: 'omit' 时浏览器连 Set-Cookie 也丢掉，
// 登录 15 分钟后第一次刷新就会掉线，所以这些请求必须是 same-origin。
function setup() {
  const seen: RequestCredentials[] = []
  const fetch = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    seen.push(init?.credentials ?? 'same-origin')
    return new Response(JSON.stringify({ access_token: 'tok', token_type: 'Bearer', expires_in: 900, user_id: 'u1' }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as unknown as typeof globalThis.fetch
  const tokens = createTokenStore('portal', { storage: null, events: null })
  const api = createApiClient({ tokens, fetch, baseUrl: () => 'https://portal.example/' })
  return { api, tokens, seen }
}

describe('登录类请求带 cookie', () => {
  it('快捷登录', async () => {
    const env = setup()
    await consumeQuickLogin(env.api, env.tokens, 'q'.repeat(43))
    expect(env.seen).toEqual(['same-origin'])
    expect(env.tokens.get()).toBe('tok')
  })

  it('邮件链接登录', async () => {
    const env = setup()
    await consumeMailLogin(env.api, env.tokens, 'm'.repeat(43))
    expect(env.seen).toEqual(['same-origin'])
    expect(env.tokens.get()).toBe('tok')
  })
})
