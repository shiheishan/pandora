import type { Server } from 'node:http'
import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { portalState } from '../dev/mock/portal/fixtures'
import { setMemberPercent } from '../dev/mock/portal/reset'
import { quoteSchema } from '../src/portal/screens/common/quote-schema'
import { subscriptionsSchema } from '../src/portal/subscription-schema'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

const STD_PLAN = '6f1c2a10-0000-4000-8000-000000000001'
const FAM_PLAN = '6f1c2a10-0000-4000-8000-000000000003'

describe('mock api · portal traffic reset & member discount', () => {
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
  beforeEach(() => scenario('default'))

  it('报价 reset：有配价且生效中的有一条且金额对；不卖的套餐或过期为空', async () => {
    const list = (await subs()).subscriptions
    const sub = list[0]!
    expect(sub.status).toBe('active')

    const q = await quote({ action: 'reset', subscription_id: sub.id })
    expect(q.quotes).toHaveLength(1)
    const row = q.quotes[0]!
    expect(row.subscription_id).toBe(sub.id)
    expect(row.plan_id).toBe(sub.plan_id)
    expect(row.pack_id).toBeNull()
    expect(row.price_id).toBeNull()
    expect(row.interval).toBe('')
    expect(row.interval_count).toBe(0)
    expect(row.period_start).toBeNull()
    expect(row.period_end).toBeNull()
    expect(row.credit).toBe(0)
    expect(row.refund).toBe(0)
    expect(row.subtotal).toBe(2500) // 专业版配价 2500
    expect(row.total).toBe(2500)
    expect(row.member_discount).toBe(0)
    expect(row.member_percent).toBe(0)

    // 不卖的套餐为空：修改一份订阅的 plan_id 为家庭版 (FAM_PLAN)
    const state = portalState(userId)
    const fixture = state.subs.find((s) => s.id === sub.id)!
    fixture.plan_id = FAM_PLAN
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
    const order = (await res.json()) as { order_id: string; status: string; member_discount_amount: number; payable_amount: number }
    expect(order.status).toBe('pending_payment')
    expect(order.member_discount_amount).toBe(0)
    expect(order.payable_amount).toBe(2500)

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

  it('会员折扣：重置报价不打折，新购照打', async () => {
    const state = portalState(userId)
    setMemberPercent(state, 10)

    const [sub] = (await subs()).subscriptions
    const q = await quote({ action: 'reset', subscription_id: sub!.id })
    const r = q.quotes[0]!
    expect(r.subtotal).toBe(2500)
    expect(r.member_percent).toBe(0)
    expect(r.member_discount).toBe(0)
    expect(r.total).toBe(2500)

    // 新购报价也同样计算会员折扣
    const qNew = await quote({ action: 'new', plan_id: STD_PLAN, new_copy: true })
    const rNew = qNew.quotes[0]!
    expect(rNew.member_percent).toBe(10)
    expect(rNew.member_discount).toBe(Math.floor((rNew.subtotal * 10) / 100))
    expect(rNew.total).toBe(Math.max(rNew.subtotal - rNew.discount - rNew.member_discount - rNew.credit, 0))
  })

  it('重置不收券：报价落 coupon_error，建单 422', async () => {
    const [sub] = (await subs()).subscriptions
    const res = await call('POST', '/v1/me/checkout/quote', { action: 'reset', subscription_id: sub!.id, coupon_code: 'SAVE5' })
    expect(res.status).toBe(200)
    const q = quoteSchema.parse(await res.json())
    expect(q.quotes[0]!.coupon_error).toBe('流量重置不能使用优惠码')
    expect(q.quotes[0]!.discount).toBe(0)
    expect(q.quotes[0]!.total).toBe(2500)

    const createRes = await call('POST', `/v1/me/subscriptions/${sub!.id}/traffic-reset`, { coupon_code: 'SAVE5' }, true)
    expect(createRes.status).toBe(422)
    const err = ((await createRes.json()) as { error: { code: string; message: string } }).error
    expect(err.code).toBe('validation_failed')
    expect(err.message).toBe('流量重置不能使用优惠码')
  })
})
