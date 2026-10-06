import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · portal referral', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('portal'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.portal)).access_token)
  })
  afterAll(() => close(server))

  const scopeIn = async (name: string) => {
    expect((await mockFetch(base, auth, 'POST', '/v1/__mock/portal-scenario', { name })).status).toBeLessThan(300)
    return ((await (await mockFetch(base, auth, 'GET', '/v1/me/commission')).json()) as { summary: { scope: string } }).summary.scope
  }

  it('R114: commission summary carries the scope in every scenario', async () => {
    expect(await scopeIn('multi')).toBe('first_order')
    expect(await scopeIn('legacy')).toBe('every_order')
    expect(await scopeIn('default')).toBe('every_order')
  })
})
