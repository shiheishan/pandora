import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { inviteSchema } from '../src/portal/screens/referral/api'
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

  it('GET /v1/me/invite 返回 codes 与 max_codes', async () => {
    expect((await mockFetch(base, auth, 'POST', '/v1/__mock/portal-scenario', { name: 'default' })).status).toBeLessThan(300)
    const res = await mockFetch(base, auth, 'GET', '/v1/me/invite')
    expect(res.status).toBe(200)
    const body = inviteSchema.parse(await res.json())
    expect(body.max_codes).toBe(5)
    expect(body.codes.length).toBeGreaterThan(0)
    expect(body.codes[0]).toMatchObject({ visits: expect.any(Number) })
    // active 在前：第一个非 active 的码之后不再出现 active
    const firstInactive = body.codes.findIndex((c) => c.status !== 'active')
    if (firstInactive >= 0) expect(body.codes.slice(firstInactive).every((c) => c.status !== 'active')).toBe(true)
  })

  it('POST /v1/me/invite/codes 满 5 个有效码后回 409', async () => {
    expect((await mockFetch(base, auth, 'POST', '/v1/__mock/portal-scenario', { name: 'empty' })).status).toBeLessThan(300)
    let invite = inviteSchema.parse(await (await mockFetch(base, auth, 'GET', '/v1/me/invite')).json())
    while (invite.codes.filter((c) => c.status === 'active').length < 5) {
      const post = await mockFetch(base, auth, 'POST', '/v1/me/invite/codes', {})
      expect(post.status).toBe(200)
      invite = inviteSchema.parse(await (await mockFetch(base, auth, 'GET', '/v1/me/invite')).json())
    }
    expect(invite.codes.filter((c) => c.status === 'active').length).toBe(5)
    const conflict = await mockFetch(base, auth, 'POST', '/v1/me/invite/codes', {})
    expect(conflict.status).toBe(409)
    const err = (await conflict.json()) as { error: { message: string } }
    expect(err.error.message).toBe('最多同时持有 5 个邀请码')
  })
})
