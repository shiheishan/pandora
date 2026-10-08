import type { Server } from 'node:http'
import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { subscriptionsSchema } from '../src/portal/subscription-schema'
import { quoteSchema } from '../src/portal/screens/common/quote-schema'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

// 购买模型（设计稿 2.2 / 2.4 / 2.6）：报价、建单比对 expect、同套餐未付款新购单、最低额、流量包必须挂一份
const STD = 'a1000000-0000-4000-8000-000000000002'
const PRO = 'a1000000-0000-4000-8000-000000000003'
const BASIC = 'a1000000-0000-4000-8000-000000000001'
const PACK100 = 'a2000000-0000-4000-8000-000000000100'

describe('mock api · portal checkout (purchase model)', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  let n = 0
  const key = () => `k-${++n}`
  const call = (method: string, path: string, body?: unknown, idem = false) => mockFetch(base, auth, method, path, body, idem ? key() : undefined)
  const scenario = async (name: string) => expect((await call('POST', '/v1/__mock/portal-scenario', { name })).status).toBe(200)
  const subs = async () => subscriptionsSchema.parse(await (await call('GET', '/v1/me/subscriptions')).json())
  const quote = async (body: unknown) => {
    const res = await call('POST', '/v1/me/checkout/quote', body)
    expect(res.status, JSON.stringify(body)).toBe(200)
    return quoteSchema.parse(await res.json())
  }

  beforeAll(async () => {
    ;({ server, base } = await serve('portal'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.portal)).access_token)
  })
  afterAll(() => close(server))
  beforeEach(() => scenario('proto-s4'))

  it('换套餐按订阅展开：原型 S4 的 ¥21 与退 ¥9；三档都有算式所需的明细', async () => {
    const [sub] = (await subs()).subscriptions
    const byPlan = await quote({ action: 'change', subscription_id: sub!.id })
    expect(byPlan.min_payment).toBe(100)
    const pro = byPlan.quotes.find((q) => q.plan_id === PRO)!
    const basic = byPlan.quotes.find((q) => q.plan_id === BASIC)!
    expect([pro.credit, pro.total, pro.refund]).toEqual([2400, 2100, 0])
    expect([basic.total, basic.refund]).toEqual([0, 900])
    expect(pro.credit_detail).toMatchObject({ paid: 3000, days_left: 25, days_total: 30, ratio_ppm: 800000 })
    // 与 A 路一致：只给订阅时每个套餐的全部价格档都返回
    expect(byPlan.quotes.filter((q) => q.plan_id === PRO)).toHaveLength(3)
    const tiers = await quote({ action: 'change', subscription_id: sub!.id, plan_id: PRO })
    expect(tiers.quotes.map((q) => q.total)).toEqual([2100, 10200, 42600])
  })

  it('建单带 expect：金额对得上就下单；对不上回 409 quote_changed；as_of 超过 10 分钟也回', async () => {
    const [sub] = (await subs()).subscriptions
    const q = await quote({ action: 'change', subscription_id: sub!.id, plan_id: PRO })
    const r = q.quotes[0]!
    const body = { plan_id: PRO, price_id: r.price_id, as_of: q.as_of, expect: { total: r.total, balance_applied: 0, payable: r.total } }
    const bad = await call('POST', `/v1/me/subscriptions/${sub!.id}/change-plan`, { ...body, expect: { ...body.expect, total: r.total - 1 } }, true)
    expect(bad.status).toBe(409)
    expect(((await bad.json()) as { error: { code: string } }).error.code).toBe('quote_changed')
    const stale = await call('POST', `/v1/me/subscriptions/${sub!.id}/change-plan`, { ...body, as_of: new Date(Date.now() - 11 * 60_000).toISOString() }, true)
    expect(stale.status).toBe(409)
    const ok = await call('POST', `/v1/me/subscriptions/${sub!.id}/change-plan`, body, true)
    expect(ok.status).toBe(201)
    expect(await ok.json()).toMatchObject({ status: 'pending_payment', payable_amount: 2100 })
  })

  it('余额经 ApplyBalance：S7c 余额只用 ¥29、付 ¥1，¥0.50 留着；S7b 全用余额当场完成', async () => {
    await scenario('proto-s7c')
    const [sub] = (await subs()).subscriptions
    const q = await quote({ action: 'renew', subscription_id: sub!.id })
    const r = q.quotes.find((x) => x.interval === 'month' && x.interval_count === 1)!
    expect(r.with_balance).toEqual({ applied: 2900, payable: 100, kept: 50, forced: false, small_due: false, waived: 0 })
    const res = await call('POST', `/v1/me/subscriptions/${sub!.id}/renew`, { price_id: r.price_id, use_balance: 2900, as_of: q.as_of, expect: { total: 3000, balance_applied: 2900, payable: 100 } }, true)
    expect(await res.json()).toMatchObject({ balance_applied: 2900, payable_amount: 100 })
    await scenario('proto-s7b')
    const [s7b] = (await subs()).subscriptions
    const q2 = await quote({ action: 'renew', subscription_id: s7b!.id })
    const r2 = q2.quotes[0]!
    const done = await call('POST', `/v1/me/subscriptions/${s7b!.id}/renew`, { price_id: r2.price_id, use_balance: 3000, as_of: q2.as_of, expect: { total: 3000, balance_applied: 3000, payable: 0 } }, true)
    expect(await done.json()).toMatchObject({ status: 'fulfilled', payable_amount: 0 })
  })

  it('另买一份：不带 new_copy 拦同款；同款没起名时名字必填；同一套餐只能有一张未付款的新购单', async () => {
    await scenario('proto-s3')
    const q = await quote({ action: 'new', plan_id: STD, new_copy: true })
    const r = q.quotes[0]!
    const base = { plan_id: STD, price_id: r.price_id, as_of: q.as_of, expect: { total: r.total, balance_applied: 0, payable: r.total } }
    expect((await call('POST', '/v1/orders', base, true)).status).toBe(409)
    const noName = await call('POST', '/v1/orders', { ...base, new_copy: true }, true)
    expect(noName.status).toBe(422)
    expect(((await noName.json()) as { error: { fields: Record<string, string> } }).error.fields.label).toContain('App 里会有两个')
    expect((await call('POST', '/v1/orders', { ...base, new_copy: true, label: '妈妈的 iPad' }, true)).status).toBe(201)
    const again = await call('POST', '/v1/orders', { ...base, new_copy: true, label: '爸爸的手机' }, true)
    expect(again.status).toBe(409)
    const pending = (await again.json()) as { error: { code: string; fields: Record<string, string> } }
    expect(pending.error.code).toBe('order_pending')
    expect(pending.error.fields.order_id).toMatch(/^[0-9a-f-]{36}$/)
  })

  it('流量包必须挂到一份在用的上；付款前的支付最低额兜底', async () => {
    await scenario('proto-s1')
    const [sub] = (await subs()).subscriptions
    expect((await call('POST', '/v1/me/traffic-pack-orders', { pack_id: PACK100 }, true)).status).toBe(422)
    const q = await quote({ action: 'pack', pack_id: PACK100, subscription_id: sub!.id })
    const ok = await call('POST', '/v1/me/traffic-pack-orders', { pack_id: PACK100, subscription_id: sub!.id, as_of: q.as_of, expect: { total: 1800, balance_applied: 0, payable: 1800 } }, true)
    expect(ok.status).toBe(201)
    const order = (await ok.json()) as { order_id: string }
    const paid = await mockFetch(base, auth, 'GET', `/v1/__mock/cashier/complete?intent=none`)
    expect(paid.status).toBe(404)
    expect((await call('POST', `/v1/orders/${order.order_id}/pay`, { provider: 'epay', method: 'alipay', return_url: `${base}/#/x` })).status).toBe(201)
    await scenario('proto-s8')
    const [expired] = (await subs()).subscriptions
    expect((await call('POST', '/v1/me/traffic-pack-orders', { pack_id: PACK100, subscription_id: expired!.id }, true)).status).toBe(409)
  })
})
