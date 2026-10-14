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

  it('default scenario: commission summary has withdraw fields and direct_rate_bp, entries have level', async () => {
    await scopeIn('default')
    const res = await mockFetch(base, auth, 'GET', '/v1/me/commission')
    expect(res.status).toBe(200)
    const json = (await res.json()) as {
      summary: { withdraw_enabled: boolean; withdraw_methods: string[]; direct_rate_bp: number; site_currency: string; paid_invitees: number }
      entries: Array<{ level: number; from: string }>
    }
    expect(json.summary.withdraw_enabled).toBe(true)
    expect(json.summary.site_currency).toBe('CNY')
    // 付费好友只算第 1 级；第 2、3 级条目不带邮箱
    expect(json.summary.paid_invitees).toBe(new Set(json.entries.filter((e) => e.level === 1).map((e) => e.from)).size)
    for (const e of json.entries.filter((x) => x.level > 1)) expect(e.from).toBe('')
    expect(json.summary.withdraw_methods).toContain('usdt_trc20')
    expect(json.summary.direct_rate_bp).toBe(2000)
    expect(json.entries.length).toBeGreaterThan(0)
    for (const e of json.entries) {
      expect(typeof e.level).toBe('number')
    }
  })

  it('POST /v1/me/withdrawals validation and error branches', async () => {
    await scopeIn('default')
    // method 不在开放列表中 -> 422
    const resMethod = await mockFetch(base, auth, 'POST', '/v1/me/withdrawals', { amount: 10000, method: 'wechat', payout_detail: 'x' }, 'key-method-fail')
    expect(resMethod.status).toBe(422)
    const jsonMethod = (await resMethod.json()) as { error?: { fields?: Record<string, string> } }
    expect(jsonMethod.error?.fields?.method).toBeDefined()

    // USDT 地址格式不对 -> 422
    const resPayout = await mockFetch(base, auth, 'POST', '/v1/me/withdrawals', { amount: 10000, method: 'usdt_trc20', payout_detail: '0x123' }, 'key-payout-fail')
    expect(resPayout.status).toBe(422)
    const jsonPayout = (await resPayout.json()) as { error?: { fields?: Record<string, string> } }
    expect(jsonPayout.error?.fields?.payout_detail).toBeDefined()

    // 未知字段 -> 400
    const resExtra = await mockFetch(base, auth, 'POST', '/v1/me/withdrawals', { amount: 10000, payout_detail: 'x', extra: 1 }, 'key-extra-fail')
    expect(resExtra.status).toBe(400)
  })

  it('legacy scenario: withdraw disabled returns 409 conflict', async () => {
    await scopeIn('legacy')
    const resGet = await mockFetch(base, auth, 'GET', '/v1/me/commission')
    const jsonGet = (await resGet.json()) as { summary: { withdraw_enabled: boolean } }
    expect(jsonGet.summary.withdraw_enabled).toBe(false)

    const resPost = await mockFetch(
      base,
      auth,
      'POST',
      '/v1/me/withdrawals',
      { amount: 10000, method: 'alipay', payout_detail: '13800000000 张三' },
      'key-legacy-withdraw',
    )
    expect(resPost.status).toBe(409)

    // 用例结束切回 default 场景
    await scopeIn('default')
  })
})
