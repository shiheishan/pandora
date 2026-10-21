import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { inviteSchema } from '../src/portal/screens/referral/schemas'
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
    expect(body.single_use).toBe(false)
    expect(body.codes.length).toBeGreaterThan(0)
    expect(body.codes[0]).toMatchObject({ visits: expect.any(Number) })
    // active 在前：第一个非 active 的码之后不再出现 active
    const firstInactive = body.codes.findIndex((c) => c.status !== 'active')
    if (firstInactive >= 0) expect(body.codes.slice(firstInactive).every((c) => c.status !== 'active')).toBe(true)
  })

  it('GET /v1/me/invite 在「邀请码永不失效」关闭时 single_use 为 true', async () => {
    expect((await mockFetch(base, auth, 'POST', '/v1/__mock/portal-scenario', { name: 'default' })).status).toBeLessThan(300)
    const patch = await fetch(`${base}/__mock/portal-signup`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ invite_never_expire: false }),
    })
    expect(patch.status).toBe(200)
    const res = await mockFetch(base, auth, 'GET', '/v1/me/invite')
    expect(res.status).toBe(200)
    const body = inviteSchema.parse(await res.json())
    expect(body.single_use).toBe(true)
    const reset = await fetch(`${base}/__mock/portal-signup`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ reset: true }),
    })
    expect(reset.status).toBe(200)
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

  it('default scenario: commission summary has withdraw fields and direct_rate_bp, entries have level', async () => {
    await scopeIn('default')
    const res = await mockFetch(base, auth, 'GET', '/v1/me/commission')
    expect(res.status).toBe(200)
    const json = (await res.json()) as {
      summary: { withdraw_enabled: boolean; withdraw_methods: string[]; direct_rate_bp: number; paid_invitees: number }
      entries: Array<{ level: number; from: string; status: string }>
    }
    expect(json.summary.withdraw_enabled).toBe(true)
    expect(json.summary).not.toHaveProperty('site_currency')
    // 付费好友只算第 1 级、不算已作废的；第 2、3 级条目不带邮箱
    expect(json.summary.paid_invitees).toBe(
      new Set(json.entries.filter((e) => e.level === 1 && e.status !== 'reversed').map((e) => e.from)).size,
    )
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
