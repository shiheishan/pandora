import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { planResetPricesSchema } from '../src/admin/screens/plans/api'
import { memberDiscountSchema } from '../src/admin/screens/users/api'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

// users 假后端的固定用户 id（标准版订阅用户）
const USER_ID = '1a2b3c41-0000-4000-8000-000000000001'
const STD = '9c0e1a2b-3333-4b00-8000-000000000001'

describe('mock api · admin xb-order (reset prices, member discount, coupon intervals)', () => {
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

  describe('plan reset prices', () => {
    it('GET returns seed CNY 1800 for first active plan', async () => {
      const res = await get(`/v1/plans/${STD}/reset-prices`)
      expect(res.status).toBe(200)
      const data = planResetPricesSchema.parse(await res.json())
      expect(data.reset_prices).toEqual([{ currency: 'CNY', unit_amount: 1800 }])
    })

    it('PUT replaces prices after reauth and replays with idempotency key', async () => {
      const fresh = await reauthHeaders()
      const body = { reset_prices: [{ currency: 'CNY', unit_amount: 2000 }, { currency: 'USD', unit_amount: 300 }] }
      const put = await mockFetch(base, fresh, 'PUT', `/v1/plans/${STD}/reset-prices`, body, 'reset-1')
      expect(put.status).toBe(200)
      expect(planResetPricesSchema.parse(await put.json()).reset_prices).toEqual(body.reset_prices)
      const replay = await mockFetch(base, fresh, 'PUT', `/v1/plans/${STD}/reset-prices`, body, 'reset-1')
      expect(replay.status).toBe(200)
    })

    it('PUT without reauth returns 403 reauth_required', async () => {
      await fetch(`${base}/__mock/expire-reauth`, { method: 'POST' })
      const res = await send('PUT', `/v1/plans/${STD}/reset-prices`, { reset_prices: [] }, 'reset-no-reauth')
      expect(res.status).toBe(403)
      expect((await res.json()).error.code).toBe('reauth_required')
    })

    it('PUT validates max 2 entries and positive amounts', async () => {
      const fresh = await reauthHeaders()
      const tooMany = await mockFetch(base, fresh, 'PUT', `/v1/plans/${STD}/reset-prices`, {
        reset_prices: [
          { currency: 'CNY', unit_amount: 100 },
          { currency: 'USD', unit_amount: 100 },
          { currency: 'CNY', unit_amount: 200 },
        ],
      }, 'reset-too-many')
      expect(tooMany.status).toBe(422)
      expect((await tooMany.json()).error.fields.reset_prices).toBe('最多配置 CNY、USD 各一档')

      const zero = await mockFetch(base, fresh, 'PUT', `/v1/plans/${STD}/reset-prices`, { reset_prices: [{ currency: 'CNY', unit_amount: 0 }] }, 'reset-zero')
      expect(zero.status).toBe(422)
      expect((await zero.json()).error.fields['reset_prices.0.unit_amount']).toBe('重置价格须大于 0，最多两位小数')
    })
  })

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
