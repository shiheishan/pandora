import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { subscriptionExtrasSchema } from '../src/admin/screens/nodes/subscription'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · admin subscription', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
  })
  afterAll(() => close(server))

  const call = (method: string, path: string, body?: unknown) => mockFetch(base, auth, method, path, body)
  const json = async (res: Response) => (await res.json()) as Record<string, unknown>

  it('reads subscription extras defaults and saves them; rejects link_base_urls as unknown field', async () => {
    const initial = subscriptionExtrasSchema.parse(await json(await call('GET', '/v1/settings/subscription')))
    expect(initial).toEqual({ info_nodes: false, protocol_prefix: false, fetch_limit_per_hour: 60 })

    const saved = subscriptionExtrasSchema.parse(
      await json(await call('POST', '/v1/settings/subscription', { info_nodes: true, protocol_prefix: true })),
    )
    expect(saved.info_nodes).toBe(true)
    expect(saved.protocol_prefix).toBe(true)
  })

  it('validates fetch_limit_per_hour like Go (1–10000) and keeps state on 422', async () => {
    const bad = await call('POST', '/v1/settings/subscription', { fetch_limit_per_hour: 0 })
    expect(bad.status).toBe(422)
    expect(await json(bad)).toMatchObject({
      error: { code: 'validation_failed', fields: { fetch_limit_per_hour: '每小时拉取上限要在 1 到 10000 之间' } },
    })
    const ok = subscriptionExtrasSchema.parse(await json(await call('POST', '/v1/settings/subscription', { fetch_limit_per_hour: 120 })))
    expect(ok.fetch_limit_per_hour).toBe(120)
  })

  it('rejects link_base_urls with 400 bad_request', async () => {
    expect(await json(await call('POST', '/v1/settings/subscription', { link_base_urls: ['https://sub.example.com'] }))).toMatchObject({
      error: { code: 'bad_request' },
    })
  })

  it('returns reauth_required on POST extras after expire-reauth', async () => {
    await fetch(`${base}/__mock/expire-reauth`, { method: 'POST' })
    const res = await call('POST', '/v1/settings/subscription', { info_nodes: false })
    expect(res.status).toBe(403)
    expect(await json(res)).toMatchObject({ error: { code: 'reauth_required' } })
  })
})
