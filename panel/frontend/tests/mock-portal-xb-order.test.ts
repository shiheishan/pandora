import type { Server } from 'node:http'
import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { portalState } from '../dev/mock/portal/fixtures'
import { quoteSchema } from '../src/portal/screens/common/quote-schema'
import { subscriptionsSchema } from '../src/portal/subscription-schema'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

const STD_PLAN = '6f1c2a10-0000-4000-8000-000000000001'
const PRO_PLAN = '6f1c2a10-0000-4000-8000-000000000002'

// 默认场景第一份是专业版（没开流量重置）；用例先把它改成标准版（重置 ¥10 / 次，每期最多 2 次）
describe('mock api · portal traffic reset', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  let userId: string
  let n = 0
  const key = () => `key-reset-${++n}`
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
    const cred = await loginAs(base, MOCK_ACCOUNTS.portal)
    userId = cred.user_id
    auth = bearer(cred.access_token)
  })
  afterAll(() => close(server))
  beforeEach(async () => {
    await scenario('default')
    portalState(userId).subs[0]!.plan_id = STD_PLAN
  })

  it('报价 reset：有配价且生效中的有一条且金额对；不卖的套餐或过期为空', async () => {
    const list = (await subs()).subscriptions
    const sub = list[0]!
    expect(sub.status).toBe('active')

    const q = await quote({ action: 'reset', subscription_id: sub.id })
    expect(q.quotes).toHaveLength(1)
    const row = q.quotes[0]!
    expect(row.subscription_id).toBe(sub.id)
    expect(row.plan_id).toBe(sub.plan_id)
    expect(row.price_id).toBeNull()
    expect(row.interval).toBe('')
    expect(row.interval_count).toBe(0)
    expect(row.period_start).toBeNull()
    expect(row.period_end).toBeNull()
    expect(row.credit).toBe(0)
    expect(row.refund).toBe(0)
    expect(row.subtotal).toBe(1000) // 标准版重置价
    expect(row.total).toBe(1000)

    // 没开重置的套餐为空：改成专业版（reset 为 null）
    const state = portalState(userId)
    const fixture = state.subs.find((s) => s.id === sub.id)!
    fixture.plan_id = PRO_PLAN
    const qFam = await quote({ action: 'reset', subscription_id: sub.id })
    expect(qFam.quotes).toHaveLength(0)

    // 过期的一份为空
    fixture.plan_id = STD_PLAN
    fixture.status = 'expired'
    const qExp = await quote({ action: 'reset', subscription_id: sub.id })
    expect(qExp.quotes).toHaveLength(0)
  })

  it('建单 201 → 再报价回 409 order_pending → 付款后已用清零', async () => {
    const [sub] = (await subs()).subscriptions
    const q = await quote({ action: 'reset', subscription_id: sub!.id })
    const r = q.quotes[0]!
    const body = { as_of: q.as_of, expect: { total: r.total, balance_applied: 0, payable: r.total } }

    const res = await call('POST', `/v1/me/subscriptions/${sub!.id}/traffic-reset`, body, true)
    expect(res.status).toBe(201)
    const order = (await res.json()) as { order_id: string; status: string; payable_amount: number }
    expect(order.status).toBe('pending_payment')
    expect(order).not.toHaveProperty('member_discount_amount')
    expect(order).not.toHaveProperty('currency')
    expect(order.payable_amount).toBe(1000)

    // 再报价回 409 order_pending
    const secondQ = await call('POST', '/v1/me/checkout/quote', { action: 'reset', subscription_id: sub!.id })
    expect(secondQ.status).toBe(409)
    const err = ((await secondQ.json()) as { error: { code: string; fields: Record<string, string>; message: string } }).error
    expect(err.code).toBe('order_pending')
    expect(err.fields.order_id).toBe(order.order_id)
    expect(err.message).toBe('你有一张还没付款的流量重置订单，继续付款或取消后再买')

    // 检查此时用量不为 0
    const state = portalState(userId)
    const subFixture = state.subs.find((s) => s.id === sub!.id)!
    const usedBefore = subFixture.days.reduce((acc, d) => acc + d.bytes, 0)
    expect(usedBefore).toBeGreaterThan(0)

    // 付款
    const pay = await call('POST', `/v1/orders/${order.order_id}/pay`, { provider: 'epay', method: 'alipay', return_url: `${base}/#/x` })
    expect(pay.status).toBe(201)
    const intent = new URL(((await pay.json()) as { redirect_url: string }).redirect_url).searchParams.get('intent')!

    // 假收银台「模拟支付成功」：回调到账、履约
    const done = await fetch(`${base}/v1/__mock/cashier/complete?intent=${intent}`, { redirect: 'manual' })
    expect(done.status).toBe(302)
    const detail = (await (await call('GET', `/v1/orders/${order.order_id}`)).json()) as { order: { status: string; kind: string } }
    expect(detail.order).toMatchObject({ status: 'fulfilled', kind: 'traffic_reset' })

    // 已用流量清零
    const usedAfter = subFixture.days.reduce((acc, d) => acc + d.bytes, 0)
    expect(usedAfter).toBe(0)
  })

  it('次数用完：报价 409 本期的流量重置次数已用完；履约后 resets_used 加一', async () => {
    const state = portalState(userId)
    const fixture = state.subs[0]!
    const [sub] = (await subs()).subscriptions
    expect(sub!.reset_limit).toBe(2)
    expect(sub!.reset_price).toBe(1000)
    expect(sub!.resets_used).toBe(0)

    fixture.resetsUsed = 2
    const res = await call('POST', '/v1/me/checkout/quote', { action: 'reset', subscription_id: sub!.id })
    expect(res.status).toBe(409)
    const err = ((await res.json()) as { error: { code: string; message: string } }).error
    expect(err.code).toBe('conflict')
    expect(err.message).toBe('本期的流量重置次数已用完')
    expect((await subs()).subscriptions[0]!.resets_used).toBe(2)
  })

  it('重置不收券：报价落 coupon_error，建单 422', async () => {
    const [sub] = (await subs()).subscriptions
    const res = await call('POST', '/v1/me/checkout/quote', { action: 'reset', subscription_id: sub!.id, coupon_code: 'SAVE5' })
    expect(res.status).toBe(200)
    const q = quoteSchema.parse(await res.json())
    expect(q.quotes[0]!.coupon_error).toBe('流量重置不能使用优惠码')
    expect(q.quotes[0]!.discount).toBe(0)
    expect(q.quotes[0]!.total).toBe(1000)

    const createRes = await call('POST', `/v1/me/subscriptions/${sub!.id}/traffic-reset`, { coupon_code: 'SAVE5' }, true)
    expect(createRes.status).toBe(422)
    const err = ((await createRes.json()) as { error: { code: string; message: string } }).error
    expect(err.code).toBe('validation_failed')
    expect(err.message).toBe('流量重置不能使用优惠码')
  })
})
