/**
 * [INPUT]: 依赖 vitest，依赖 ./mock-helpers 的 serve / close / loginAs / bearer / mockFetch，依赖 ../dev/mock-api 的 MOCK_ACCOUNTS
 * [OUTPUT]: 对外提供门户「我已支付，刷新状态」假接口的测试
 * [POS]: tests 的门户查单假后端守卫（PAY-009）：响应带齐 Go OrderPaymentQuery 的字段（order-query.ts 依赖 tsx 进不了 node 侧类型检查，按字段断言；schema 本身在 common.test.ts 测）；发起支付没付答 unpaid，假收银台「回调丢失」之后查到已付并补记、订单变已履约，再查是 already_recorded；没发起过支付 409、种子处理中单 503、非 UUID 404、每账号每分钟 6 次后 429；列表与明细的 has_payment_intent 一致、发起支付前 false 之后 true
 */
import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

interface Plan { id: string; prices: Array<{ id: string; currency: string }> }
interface Queried { order_id: string; order_no: string; provider_code: string; channel_status: string; reconciled: boolean; already_recorded: boolean; order_status: string }
const FIELDS = ['order_id', 'order_no', 'provider_code', 'channel_status', 'reconciled', 'already_recorded', 'order_status']
async function queried(res: Response): Promise<Queried> {
  expect(res.status).toBe(200)
  const body = (await res.json()) as Queried
  expect(Object.keys(body).sort()).toEqual([...FIELDS].sort())
  return body
}

describe('mock api · portal order query', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('portal'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.portal)).access_token)
  })
  afterAll(() => close(server))

  const query = (id: string) => mockFetch(base, auth, 'POST', `/v1/orders/${id}/query`)

  it('answers unpaid, then reconciles a payment whose callback was lost', async () => {
    const plans = ((await (await mockFetch(base, auth, 'GET', '/v1/plans')).json()) as { plans: Plan[] }).plans
    const plan = plans.find((p) => p.prices.some((x) => x.currency === 'CNY'))!
    const placed = await mockFetch(base, auth, 'POST', '/v1/orders', { plan_id: plan.id, price_id: plan.prices.find((x) => x.currency === 'CNY')!.id }, 'pq-order-1')
    expect(placed.status).toBe(201)
    const { order_id } = (await placed.json()) as { order_id: string }

    const intentFlag = async () => {
      const detail = ((await (await mockFetch(base, auth, 'GET', `/v1/orders/${order_id}`)).json()) as { order: { has_payment_intent: boolean } }).order
      const list = ((await (await mockFetch(base, auth, 'GET', '/v1/orders?status=pending_payment')).json()) as { orders: Array<{ id: string; has_payment_intent: boolean }> }).orders
      const row = list.find((o) => o.id === order_id)!
      expect(row.has_payment_intent).toBe(detail.has_payment_intent)
      return detail.has_payment_intent
    }
    expect(await intentFlag()).toBe(false)

    // 没发起过支付：查不了
    const never = await query(order_id)
    expect(never.status).toBe(409)
    expect(((await never.json()) as { error: { message: string } }).error.message).toBe('该订单从未发起过支付，无法向渠道查单')

    const pay = await mockFetch(base, auth, 'POST', `/v1/orders/${order_id}/pay`, { provider: 'epay', method: 'alipay' })
    expect(pay.status).toBe(201)
    const intent = new URL(((await pay.json()) as { redirect_url: string }).redirect_url).searchParams.get('intent')!
    expect(await intentFlag()).toBe(true)

    const unpaid = await queried(await query(order_id))
    expect(unpaid).toMatchObject({ channel_status: 'unpaid', reconciled: false, order_status: 'pending_payment' })

    const lost = await fetch(`${base}/v1/__mock/cashier/lost-callback?intent=${intent}`, { redirect: 'manual' })
    expect(lost.status).toBe(302)
    const detail = async () => ((await (await mockFetch(base, auth, 'GET', `/v1/orders/${order_id}`)).json()) as { order: { status: string } }).order.status
    expect(await detail()).toBe('pending_payment')

    const paid = await queried(await query(order_id))
    expect(paid).toMatchObject({ channel_status: 'paid', reconciled: true, already_recorded: false, order_status: 'fulfilled' })
    expect(await detail()).toBe('fulfilled')
    const again = await queried(await query(order_id))
    expect(again).toMatchObject({ channel_status: 'paid', reconciled: false, already_recorded: true })
  })

  it('hides other orders and rate-limits per account', async () => {
    expect((await query('not-a-uuid')).status).toBe(404)
    expect((await query('00000000-0000-4000-8000-000000000000')).status).toBe(404)
    // 每账号每分钟 6 次（404 前置拒绝不计）：连查 7 次一定撞上 429，此前是种子处理中单的 503（发起过支付、假渠道查不到）
    const { orders } = (await (await mockFetch(base, auth, 'GET', '/v1/orders?status=processing')).json()) as { orders: Array<{ id: string; has_payment_intent: boolean }> }
    expect(orders[0]!.has_payment_intent).toBe(true)
    const target = orders[0]!.id
    const statuses: number[] = []
    let limited: Response | undefined
    for (let i = 0; i < 7 && !limited; i++) {
      const res = await query(target)
      statuses.push(res.status)
      if (res.status === 429) limited = res
    }
    expect(limited).toBeDefined()
    expect(statuses.slice(0, -1).every((s) => s === 503)).toBe(true)
    expect(((await limited!.json()) as { error: { code: string } }).error.code).toBe('rate_limited')
  })
})
