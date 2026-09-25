/**
 * [INPUT]: 依赖 vitest，依赖 ./mock-helpers 的 serve / close，依赖 ../dev/mock-api 的 MOCK_ACCOUNTS
 * [OUTPUT]: 对外提供门户假接口的测试
 * [POS]: tests 的门户假后端守卫：外框读接口来自各页面模块、快捷登录令牌一次性往返、同一会话重新生成作废旧令牌、下线外壳会话让那枚令牌失效
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

  it('binds quick-login tokens to the issuing session: regenerating voids the previous one', async () => {
    const login = await fetch(`${base}/v1/auth/login`, { method: 'POST', body: JSON.stringify(MOCK_ACCOUNTS.portal) })
    const auth = { Authorization: `Bearer ${((await login.json()) as { access_token: string }).access_token}` }
    const issue = async () => ((await (await fetch(`${base}/v1/me/quick-login`, { method: 'POST', headers: auth })).json()) as { token: string }).token
    const consume = (token: string) => fetch(`${base}/v1/auth/quick-login`, { method: 'POST', body: JSON.stringify({ token }) })
    const first = await issue()
    const second = await issue()
    expect((await consume(first)).status).toBe(401)
    expect((await consume(second)).status).toBe(200)
  })

  it('lists the other shell sessions and revoking one logs that token out', async () => {
    const login = async () => ((await (await fetch(`${base}/v1/auth/login`, { method: 'POST', body: JSON.stringify(MOCK_ACCOUNTS.portal) })).json()) as { access_token: string }).access_token
    const mine = { Authorization: `Bearer ${await login()}` }
    const phone = { Authorization: `Bearer ${await login()}` }
    type Row = { id: string; current: boolean }
    const list = async () => ((await (await fetch(`${base}/v1/me/sessions`, { headers: mine })).json()) as { sessions: Row[] }).sessions
    // 手机那一枚的 id：它自己看到的当前会话
    const phoneId = ((await (await fetch(`${base}/v1/me/sessions`, { headers: phone })).json()) as { sessions: Row[] }).sessions.find((s) => s.current)!.id
    expect((await list()).some((s) => s.id === phoneId && !s.current)).toBe(true)
    expect((await fetch(`${base}/v1/me/sessions/${phoneId}`, { method: 'DELETE', headers: mine })).status).toBe(200)
    expect((await fetch(`${base}/v1/me`, { headers: phone })).status).toBe(401)
    expect((await list()).some((s) => s.id === phoneId)).toBe(false)
    expect((await fetch(`${base}/v1/me`, { headers: mine })).status).toBe(200)
  })
})
