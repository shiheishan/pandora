/**
 * [INPUT]: 依赖 vitest，依赖 ./mock-helpers 的 serve / close，依赖 ../dev/mock-api 的 MOCK_ACCOUNTS
 * [OUTPUT]: 对外提供门户假接口的测试
 * [POS]: tests 的门户假后端守卫：外框读接口来自各页面模块、快捷登录令牌一次性往返
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { close, serve } from './mock-helpers'

describe('mock api · portal', () => {
  let server: Server
  let base: string
  beforeAll(async () => ({ server, base } = await serve('portal')))
  afterAll(() => close(server))

  it('serves the shell top-bar reads from the page modules and round-trips a quick-login token', async () => {
    const login = await fetch(`${base}/v1/auth/login`, { method: 'POST', body: JSON.stringify(MOCK_ACCOUNTS.portal) })
    const { access_token } = (await login.json()) as { access_token: string }
    const auth = { Authorization: `Bearer ${access_token}` }
    for (const path of ['/v1/me', '/v1/me/balance', '/v1/me/subscriptions', '/v1/me/commission', '/v1/me/notifications']) {
      expect((await fetch(`${base}${path}`, { headers: auth })).status, path).toBe(200)
    }
    const issued = await fetch(`${base}/v1/me/quick-login`, { method: 'POST', headers: auth })
    const { token } = (await issued.json()) as { token: string }
    const consume = () => fetch(`${base}/v1/auth/quick-login`, { method: 'POST', body: JSON.stringify({ token }) })
    expect((await consume()).status).toBe(200)
    expect((await consume()).status).toBe(401)
  })
})
