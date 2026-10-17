import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · admin subscription period-end and quota', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
  })
  afterAll(() => close(server))

  const get = (path: string) => mockFetch(base, auth, 'GET', path)
  const send = (method: string, path: string, body: unknown) => mockFetch(base, auth, method, path, body)
  type SubRow = {
    id: string
    status: string
    current_period_end: string | null
    quotas: Array<{ metric: string; period: string; limit: number | null }>
  }
  const subsOf = async (userId: string) => ((await (await get(`/v1/users/${userId}`)).json()) as { subscriptions: SubRow[] }).subscriptions

  const reauth = async () => {
    const res = await fetch(`${base}/v1/auth/reauth`, { method: 'POST', headers: auth, body: JSON.stringify({ password: MOCK_ACCOUNTS.admin.password }) })
    auth = { Authorization: `Bearer ${((await res.json()) as { access_token: string }).access_token}` }
  }

  async function pickUserWithLiveSub(): Promise<{ userId: string; sub: SubRow }> {
    const list = (await (await get('/v1/users?limit=100')).json()) as { users: Array<{ id: string }> }
    for (const u of list.users) {
      const sub = (await subsOf(u.id)).find((s) => s.status === 'active' && s.current_period_end)
      if (sub) return { userId: u.id, sub }
    }
    throw new Error('no live subscription in seed')
  }

  async function pickExpiredSub(): Promise<string> {
    const list = (await (await get('/v1/users?limit=100')).json()) as { users: Array<{ id: string }> }
    for (const u of list.users) {
      const sub = (await subsOf(u.id)).find((s) => s.status === 'expired')
      if (sub) return sub.id
    }
    throw new Error('no expired subscription in seed')
  }

  it('sets period-end after reauth and reflects in user detail; past time is 422', async () => {
    const { userId, sub } = await pickUserWithLiveSub()
    const path = `/v1/subscriptions/${sub.id}/period-end`
    const reason = '调整到期以对齐合同'

    await fetch(`${base}/__mock/expire-reauth`, { method: 'POST' })
    expect((await send('POST', path, { period_end: sub.current_period_end, reason })).status).toBe(403)
    await reauth()

    const past = new Date(Date.now() - 86_400_000).toISOString()
    const bad = await send('POST', path, { period_end: past, reason })
    expect(bad.status).toBe(422)
    expect(await bad.json()).toMatchObject({ error: { fields: { period_end: expect.any(String) } } })

    const newEnd = new Date(Date.now() + 60 * 86_400_000).toISOString()
    const done = await send('POST', path, { period_end: newEnd, reason })
    expect(done.status).toBe(200)
    const body = (await done.json()) as { period_end: string; subscription_id: string }
    expect(body).toMatchObject({ subscription_id: sub.id, period_end: newEnd })
    const after = await subsOf(userId)
    expect(after.find((s) => s.id === sub.id)!.current_period_end).toBe(newEnd)
  })

  it('sets quota limit to 50 GiB or null and rejects bad metric; expired sub gets 409', async () => {
    const { userId, sub } = await pickUserWithLiveSub()
    const path = `/v1/subscriptions/${sub.id}/quota`
    const reason = '调整流量上限以对齐合同'
    const fifty = 50 * 1024 ** 3

    await reauth()
    const done = await send('POST', path, { metric: 'traffic.bytes', period: 'cycle', limit_bytes: fifty, reason })
    expect(done.status).toBe(200)
    expect(await done.json()).toEqual({ ok: true })
    const q = (await subsOf(userId)).find((s) => s.id === sub.id)!.quotas.find((x) => x.metric === 'traffic.bytes')!
    expect(q.limit).toBe(fifty)

    expect((await send('POST', path, { metric: 'traffic.bytes', period: 'cycle', limit_bytes: null, reason })).status).toBe(200)
    const q2 = (await subsOf(userId)).find((s) => s.id === sub.id)!.quotas.find((x) => x.metric === 'traffic.bytes')!
    expect(q2.limit).toBeNull()

    const badMetric = await send('POST', path, { metric: 'x', period: 'cycle', limit_bytes: fifty, reason })
    expect(badMetric.status).toBe(422)
    expect(await badMetric.json()).toMatchObject({ error: { fields: { metric: '这一份没有这个配额' } } })

    const expiredId = await pickExpiredSub()
    const refused = await send('POST', `/v1/subscriptions/${expiredId}/quota`, { metric: 'traffic.bytes', period: 'cycle', limit_bytes: fifty, reason })
    expect(refused.status).toBe(409)
  })
})
