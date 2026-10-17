import type { Server } from 'node:http'
import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { subscriptionsSchema } from '../src/portal/subscription-schema'
import { quoteSchema } from '../src/portal/screens/common/quote-schema'
import { applyBalance, waiveSmallDue } from '../dev/mock/portal/purchase'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

// 购买模型（设计稿 2.2 / 2.4 / 2.6，10-16）：报价、建单比对 expect、同套餐未付款新购单、最低额、落点、一次性叠加
const STD = 'a1000000-0000-4000-8000-000000000002'
const PRO = 'a1000000-0000-4000-8000-000000000003'
const BASIC = 'a1000000-0000-4000-8000-000000000001'
// 非原型目录（default / multi 场景）：流量版（一次性 100G ¥30 有效 90 天）、经典版（关了新购）
const TRAFFIC = '6f1c2a10-0000-4000-8000-000000000004'
const CLASSIC = '6f1c2a10-0000-4000-8000-000000000006'

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

  it('换套餐按订阅展开：原型 S4 的 ¥21 与退 ¥9（订阅套餐只按剩余时间算）；三档都有算式所需的明细', async () => {
    const [sub] = (await subs()).subscriptions
    const byPlan = await quote({ action: 'change', subscription_id: sub!.id })
    expect(byPlan.min_payment).toBe(100)
    const pro = byPlan.quotes.find((q) => q.plan_id === PRO)!
    const basic = byPlan.quotes.find((q) => q.plan_id === BASIC)!
    expect([pro.credit, pro.total, pro.refund]).toEqual([2400, 2100, 0])
    expect([basic.total, basic.refund]).toEqual([0, 900])
    // 原来是订阅套餐：只看剩余付费时间（Go proration.go），流量两项为 0；到期多推了 30 分钟，比例略高于 80%
    expect(pro.credit_detail).toMatchObject({ paid: 3000, days_left: 24, days_total: 30, traffic_left: 0, traffic_total: 0 })
    expect(pro.credit_detail!.ratio_ppm).toBeGreaterThanOrEqual(800000)
    expect(pro.credit_detail!.ratio_ppm).toBeLessThan(801000)
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
    expect(r.with_balance).toEqual({ applied: 2900, payable: 100, kept: 50, forced: false, below_minimum: false, small_due: false, waived: 0 })
    const res = await call('POST', `/v1/me/subscriptions/${sub!.id}/renew`, { price_id: r.price_id, use_balance: 2900, as_of: q.as_of, expect: { total: 3000, balance_applied: 2900, payable: 100 } }, true)
    expect(await res.json()).toMatchObject({ balance_applied: 2900, payable_amount: 100 })
    await scenario('proto-s7b')
    const [s7b] = (await subs()).subscriptions
    const q2 = await quote({ action: 'renew', subscription_id: s7b!.id })
    const r2 = q2.quotes[0]!
    const done = await call('POST', `/v1/me/subscriptions/${s7b!.id}/renew`, { price_id: r2.price_id, use_balance: 3000, as_of: q2.as_of, expect: { total: 3000, balance_applied: 3000, payable: 0 } }, true)
    expect(await done.json()).toMatchObject({ status: 'fulfilled', payable_amount: 0 })
  })

  it('应付低于最低额（A 路）：续费不免、标 below_minimum、下单 422；换套餐 ≤ ¥0.99 的零头免掉', async () => {
    await scenario('proto-s7d')
    const [sub] = (await subs()).subscriptions
    const q = await quote({ action: 'renew', subscription_id: sub!.id, coupon_code: 'LUCKY99' })
    const r = q.quotes.find((x) => x.interval === 'month' && x.interval_count === 1)!
    expect(r.total).toBe(30)
    expect(r.with_balance).toMatchObject({ applied: 20, payable: 10, below_minimum: true, small_due: false })
    expect(r.without_balance).toMatchObject({ applied: 20, payable: 10, below_minimum: true })
    const res = await call('POST', `/v1/me/subscriptions/${sub!.id}/renew`, { price_id: r.price_id, coupon_code: 'LUCKY99', use_balance: 20, as_of: q.as_of, expect: { total: 30, balance_applied: 20, payable: 10 } }, true)
    expect(res.status).toBe(422)
    expect(((await res.json()) as { error: { message: string } }).error.message).toBe('应付金额低于支付渠道的最低付款额，请先充值或使用余额支付')
    // 只有换套餐 ≤ ¥0.99 的零头能免（purchase.WaiveSmallDue）
    expect(waiveSmallDue(applyBalance(30, 20, 20, 100), true)).toMatchObject({ applied: 20, payable: 0, waived: 10, small_due: true, below_minimum: false })
    expect(waiveSmallDue(applyBalance(30, 20, 20, 100), false)).toMatchObject({ payable: 10, below_minimum: true, small_due: false })
    expect(waiveSmallDue(applyBalance(500, 0, 0, 1000), true)).toMatchObject({ payable: 500, below_minimum: true })
    // B6c：开着余额、余额正好付清低于最低额的整单，同样 forced（两组口径一致）
    expect(applyBalance(50, 50, 50, 100)).toMatchObject({ applied: 50, payable: 0, forced: true })
    expect(applyBalance(100, 100, 100, 100)).toMatchObject({ applied: 100, payable: 0, forced: false })
  })

  it('BTCPay 首次 pay 模拟渠道建发票中：409 payment_in_progress，紧接着再 pay 成功', async () => {
    await scenario('proto-s3')
    const q = await quote({ action: 'new', plan_id: STD, new_copy: true })
    const r = q.quotes[0]!
    const created = await call('POST', '/v1/orders', { plan_id: STD, price_id: r.price_id, new_copy: true, label: '测试 BTCPay', as_of: q.as_of, expect: { total: r.total, balance_applied: 0, payable: r.total } }, true)
    const order = (await created.json()) as { order_id: string }
    const payBody = { provider: 'btcpay', method: '', return_url: `${base}/#/x` }
    const first = await call('POST', `/v1/orders/${order.order_id}/pay`, payBody)
    expect(first.status).toBe(409)
    expect(((await first.json()) as { error: { code: string } }).error.code).toBe('payment_in_progress')
    const second = await call('POST', `/v1/orders/${order.order_id}/pay`, payBody)
    expect(second.status).toBe(201)
  })

  it('过了付款期限的单：发起支付 409 order_lapsed，同款再买 order_pending 换成「已超过付款期限」并带 order_id 与 lapsed', async () => {
    await scenario('proto-s3')
    const q = await quote({ action: 'new', plan_id: STD, new_copy: true })
    const r = q.quotes[0]!
    const first = await call('POST', '/v1/orders', { plan_id: STD, price_id: r.price_id, new_copy: true, label: '妈妈的 iPad', as_of: q.as_of, expect: { total: r.total, balance_applied: 0, payable: r.total } }, true)
    const order = (await first.json()) as { order_id: string }
    expect((await call('POST', '/v1/__mock/expire-orders')).status).toBe(200)
    const pay = await call('POST', `/v1/orders/${order.order_id}/pay`, { provider: 'epay', method: 'alipay', return_url: `${base}/#/x` })
    expect(pay.status).toBe(409)
    expect(((await pay.json()) as { error: { code: string; message: string } }).error).toMatchObject({ code: 'order_lapsed', message: '这张订单已超过付款期限，请取消后重新下单' })
    const again = await call('POST', '/v1/orders', { plan_id: STD, price_id: r.price_id, new_copy: true, label: '爸爸的手机' }, true)
    const body = (await again.json()) as { error: { code: string; message: string; fields: Record<string, string> } }
    expect([again.status, body.error.code, body.error.fields.order_id]).toEqual([409, 'order_pending', order.order_id])
    expect(body.error.message).toBe('你有一张已超过付款期限的「标准版」订单，取消后再买')
    expect(body.error.fields.lapsed).toBe('true')
  })

  it('订单详情的 subscription_id：新买的履约后是新开的那份；续费建单时就指向原来那份（与 Go 同口径）', async () => {
    // Go 无 omitempty：恒在，没有时为 null（门户 orderDetailSchema 的 nullable）
    const detail = async (id: string) => ((await (await call('GET', `/v1/orders/${id}`)).json()) as { order: { subscription_id: string | null } }).order
    await scenario('proto-s7b')
    const [mine] = (await subs()).subscriptions
    const q = await quote({ action: 'new', plan_id: STD, new_copy: true })
    const r = q.quotes[0]!
    const bought = await call('POST', '/v1/orders', { plan_id: STD, price_id: r.price_id, new_copy: true, label: '妈妈的 iPad', use_balance: r.with_balance.applied, as_of: q.as_of, expect: { total: r.total, balance_applied: r.with_balance.applied, payable: r.with_balance.payable } }, true)
    const order = (await bought.json()) as { order_id: string; status: string }
    expect(order.status).toBe('fulfilled')
    const fresh = (await subs()).subscriptions.find((s) => s.label === '妈妈的 iPad')!
    expect(fresh.id).not.toBe(mine!.id)
    expect((await detail(order.order_id)).subscription_id).toBe(fresh.id)
    // 待支付的续费：订单建单时就记着那一份，详情照实给（「改用余额付」靠它回确认页）
    await scenario('proto-s3')
    const [s3] = (await subs()).subscriptions
    const rq = await quote({ action: 'renew', subscription_id: s3!.id })
    const rr = rq.quotes[0]!
    const renewal = await call('POST', `/v1/me/subscriptions/${s3!.id}/renew`, { price_id: rr.price_id, as_of: rq.as_of, expect: { total: rr.total, balance_applied: 0, payable: rr.total } }, true)
    const pending = (await renewal.json()) as { order_id: string; status: string }
    expect(pending.status).toBe('pending_payment')
    expect((await detail(pending.order_id)).subscription_id).toBe(s3!.id)
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
    // 与 Go quoteNewTx 一致：报价阶段就拦，带同一张单（门户确认页按码给「取消它 / 去付款」）
    const quoted = await call('POST', '/v1/me/checkout/quote', { action: 'new', plan_id: STD, new_copy: true })
    const qb = (await quoted.json()) as { error: { code: string; fields: Record<string, string> } }
    expect([quoted.status, qb.error.code, qb.error.fields.order_id]).toEqual([409, 'order_pending', pending.error.fields.order_id])
  })

  it('落点：new 在最前，同款一次性给叠加，别的给换；挂着待付续费单的那份不列；关了新购的回 409', async () => {
    await scenario('multi')
    const list = (await subs()).subscriptions
    const once = list.find((s) => s.plan_id === TRAFFIC)!
    const std = list.find((s) => s.plan_name === '标准版')!
    const res = await call('GET', `/v1/me/checkout/landings?plan_id=${TRAFFIC}`)
    expect(res.status).toBe(200)
    // 第一份专业版挂着待付的续费单（场景种子），不列
    expect(((await res.json()) as { options: unknown[] }).options).toEqual([{ mode: 'new' }, { mode: 'stack', subscription_id: once.id }, { mode: 'change', subscription_id: std.id }])
    const closed = await call('GET', `/v1/me/checkout/landings?plan_id=${CLASSIC}`)
    expect(closed.status).toBe(409)
    expect(((await closed.json()) as { error: { message: string } }).error.message).toBe('该套餐当前不接受新购')
    expect((await call('GET', '/v1/me/checkout/landings?plan_id=x')).status).toBe(422)
  })

  it('叠加：一次性同款总流量加上、天数顺延；一次性不能续费；不带 new_copy 新购同款回 409', async () => {
    await scenario('multi')
    const once = (await subs()).subscriptions.find((s) => s.plan_id === TRAFFIC)!
    expect([once.plan_kind, once.renewable, once.stackable, once.permanent]).toEqual(['one_time', false, true, false])
    const q = await quote({ action: 'stack', subscription_id: once.id })
    const r = q.quotes[0]!
    expect([r.total, r.validity_days, r.previous_end]).toEqual([3000, 90, once.current_period_end])
    expect(Date.parse(r.period_end!) - Date.parse(once.current_period_end!)).toBe(90 * 86_400_000)
    const renew = await call('POST', '/v1/me/checkout/quote', { action: 'renew', subscription_id: once.id })
    expect(renew.status).toBe(409)
    const dup = await call('POST', '/v1/me/checkout/quote', { action: 'new', plan_id: TRAFFIC })
    expect(((await dup.json()) as { error: { message: string } }).error.message).toBe('你已有这个一次性套餐，可以叠加到原来那一份，或选择另开一份')
    const created = await call('POST', `/v1/me/subscriptions/${once.id}/stack`, { price_id: r.price_id, as_of: q.as_of, expect: { total: 3000, balance_applied: 0, payable: 3000 } }, true)
    expect(created.status).toBe(201)
    const order = (await created.json()) as { order_id: string }
    // 同一份有在途的叠加单：再叠加回 409
    expect((await call('POST', `/v1/me/subscriptions/${once.id}/stack`, { price_id: r.price_id }, true)).status).toBe(409)
    const pay = await call('POST', `/v1/orders/${order.order_id}/pay`, { provider: 'epay', method: 'alipay', return_url: `${base}/#/x` })
    const intent = new URL(((await pay.json()) as { redirect_url: string }).redirect_url).searchParams.get('intent')!
    expect((await fetch(`${base}/v1/__mock/cashier/complete?intent=${intent}`, { redirect: 'manual' })).status).toBe(302)
    const after = (await subs()).subscriptions.find((s) => s.id === once.id)!
    expect(after.traffic_limit).toBe(200 * 1024 ** 3)
    expect(after.current_period_end).toBe(r.period_end)
    const detail = (await (await call('GET', `/v1/orders/${order.order_id}`)).json()) as { order: { kind: string; status: string } }
    expect(detail.order).toMatchObject({ kind: 'stack', status: 'fulfilled' })
  })
})
