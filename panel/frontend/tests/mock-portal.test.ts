import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { planSchema } from '../src/portal/screens/common/catalog-schema'
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

  it('serves a plan catalog the page schema accepts: two kinds, throttle, recommended, reset price, no highlights or currency', async () => {
    const res = (await (await fetch(`${base}/v1/plans`)).json()) as { plans: Array<Record<string, unknown>> }
    const plans = res.plans.map((p) => planSchema.parse(p))
    expect(plans.filter((p) => p.recommended).map((p) => p.name)).toEqual(['专业版'])
    expect(plans.find((p) => p.name === '家庭版')).toMatchObject({ kind: 'subscription', throttle_kbps: 100_000, reset: { price: 1500, limit: null } })
    expect(plans.find((p) => p.name === '永久版')).toMatchObject({ kind: 'one_time', prices: [{ billing_interval: 'one_time', interval_count: 1, validity_days: null }] })
    // 关了新购的经典版照样列出（Go 目录 DTO 没有开关字段）
    expect(plans.some((p) => p.name === '经典版')).toBe(true)
    for (const raw of res.plans) for (const gone of ['highlights', 'currency', 'quotas', 'code']) expect(Object.keys(raw)).not.toContain(gone)
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
