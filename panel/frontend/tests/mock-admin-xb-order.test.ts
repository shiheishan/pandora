import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { memberDiscountSchema } from '../src/admin/screens/users/opsSchemas'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

// users 假后端的固定用户 id（标准版订阅用户）
const USER_ID = '1a2b3c41-0000-4000-8000-000000000001'

describe('mock api · admin xb-order (member discount, coupon intervals)', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>

  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
  })
  afterAll(() => close(server))

  const get = (path: string) => mockFetch(base, auth, 'GET', path)
  const send = (method: string, path: string, body?: unknown, key?: string) => mockFetch(base, auth, method, path, body, key)

  const reauthHeaders = async () => {
    await fetch(`${base}/__mock/expire-reauth`, { method: 'POST' })
    const reauth = await fetch(`${base}/v1/auth/reauth`, {
      method: 'POST',
      headers: auth,
      body: JSON.stringify({ password: MOCK_ACCOUNTS.admin.password }),
    })
    return bearer(((await reauth.json()) as { access_token: string }).access_token)
  }

  describe('member discount', () => {
    it('GET null → PUT 10 → GET 10 → DELETE → GET null', async () => {
      const path = `/v1/member-discounts/${USER_ID}`
      const g0 = memberDiscountSchema.parse(await (await get(path)).json())
      expect(g0.percent).toBeNull()
      expect(g0.updated_at).toBeNull()

      const fresh = await reauthHeaders()
      const put = await mockFetch(base, fresh, 'PUT', path, { percent: 10 })
      expect(put.status).toBe(200)
      const afterPut = memberDiscountSchema.parse(await put.json())
      expect(afterPut.percent).toBe(10)
      expect(afterPut.updated_at).not.toBeNull()

      const g1 = memberDiscountSchema.parse(await (await get(path)).json())
      expect(g1.percent).toBe(10)

      const del = await mockFetch(base, fresh, 'DELETE', path)
      expect(del.status).toBe(200)
      expect(await del.json()).toEqual({ ok: true })

      const g2 = memberDiscountSchema.parse(await (await get(path)).json())
      expect(g2.percent).toBeNull()
    })

    it('PUT 0 returns 422 on fields.percent', async () => {
      const fresh = await reauthHeaders()
      const res = await mockFetch(base, fresh, 'PUT', `/v1/member-discounts/${USER_ID}`, { percent: 0 })
      expect(res.status).toBe(422)
      expect((await res.json()).error.fields.percent).toBe('请填 1 到 100 的整数（10 表示打九折）')
    })

    it('PUT without reauth returns 403 reauth_required', async () => {
      await fetch(`${base}/__mock/expire-reauth`, { method: 'POST' })
      const res = await send('PUT', `/v1/member-discounts/${USER_ID}`, { percent: 5 })
      expect(res.status).toBe(403)
      expect((await res.json()).error.code).toBe('reauth_required')
    })
  })

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
