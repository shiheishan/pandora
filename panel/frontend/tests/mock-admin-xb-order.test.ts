import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · admin xb-order (coupon intervals)', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>

  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
  })
  afterAll(() => close(server))

  const get = (path: string) => mockFetch(base, auth, 'GET', path)

  const reauthHeaders = async () => {
    await fetch(`${base}/__mock/expire-reauth`, { method: 'POST' })
    const reauth = await fetch(`${base}/v1/auth/reauth`, {
      method: 'POST',
      headers: auth,
      body: JSON.stringify({ password: MOCK_ACCOUNTS.admin.password }),
    })
    return bearer(((await reauth.json()) as { access_token: string }).access_token)
  }

  describe('coupon applicable_intervals', () => {
    it('creates coupon with intervals and lists them back', async () => {
      const fresh = await reauthHeaders()
      const body = {
        code: 'XBINT01',
        discount_type: 'percent',
        discount_value: 1000,
        max_redemptions_per_user: 1,
        applicable_intervals: ['month', 'year'],
      }
      const created = await mockFetch(base, fresh, 'POST', '/v1/coupons', body)
      expect(created.status).toBe(200)
      const { id } = (await created.json()) as { id: string }
      const list = (await (await get('/v1/coupons?limit=200')).json()) as { coupons: Array<{ id: string; applicable_intervals: string[] }> }
      const row = list.coupons.find((c) => c.id === id)
      expect(row?.applicable_intervals).toEqual(['month', 'year'])
    })

    it('rejects illegal interval values with 422', async () => {
      const fresh = await reauthHeaders()
      const res = await mockFetch(base, fresh, 'POST', '/v1/coupons', {
        code: 'XBINTBAD',
        discount_type: 'percent',
        discount_value: 1000,
        max_redemptions_per_user: 1,
        applicable_intervals: ['fortnight'],
      })
      expect(res.status).toBe(422)
      expect((await res.json()).error.fields.applicable_intervals).toBe('计费周期只能是 day、week、month、quarter、year、one_time')
    })
  })
})
