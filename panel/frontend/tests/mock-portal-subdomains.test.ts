import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { probeTicketCalls } from '../dev/mock/portal/subdomains'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · portal subdomains', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>

  const call = (method: string, path: string, body?: unknown) => mockFetch(base, auth, method, path, body)

  beforeAll(async () => {
    ;({ server, base } = await serve('portal'))
    const login = await loginAs(base, MOCK_ACCOUNTS.portal)
    auth = bearer(login.access_token)
  })

  afterAll(async () => {
    await close(server)
  })

  it('登录后 POST /v1/sub-probe/ticket 回 200 且 ticket 为空串', async () => {
    const before = probeTicketCalls()
    const res = await call('POST', '/v1/sub-probe/ticket')
    expect(res.status).toBe(200)
    const data = (await res.json()) as { ticket: string }
    expect(data).toEqual({ ticket: '' })
    expect(probeTicketCalls()).toBe(before + 1)
  })

  it('不带登录头 POST /v1/sub-probe/ticket 回 401', async () => {
    const res = await mockFetch(base, {}, 'POST', '/v1/sub-probe/ticket')
    expect(res.status).toBe(401)
  })

  it('GET /v1/me/subscription-domains 回 404', async () => {
    const res = await call('GET', '/v1/me/subscription-domains')
    expect(res.status).toBe(404)
  })
})
