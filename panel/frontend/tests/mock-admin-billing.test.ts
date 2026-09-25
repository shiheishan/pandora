/**
 * [INPUT]: 依赖 vitest，依赖 ./mock-helpers 的 serve / close / bearer / mockFetch，依赖 ../dev/mock-api 的 MOCK_ACCOUNTS，依赖 ../src/admin/screens/billing/schemas 的订单与收款 schema
 * [OUTPUT]: 对外提供订单与收款（后台-05）假接口的测试
 * [POS]: tests 的后台订单与收款假后端守卫：只读账号只看得到订单列表（支付记录、挂账、渠道、调整整块 404，人工开单先 404 不弹 reauth）；仪表盘「超时未支付」与待支付筛选同一份数据；订单列表能被页面 schema 接住、多值状态与未知状态 400、按 user_id 精确筛选且与用户详情的最近订单同一份数据；人工开单先 reauth、三种结算、201 重放、余额扣除 422、凭证号重复 409（中文原文，R114）；标记已支付开通订阅；取消的 state_version CAS、重放与已支付拒绝（与 Go 同序同文案，R114）；挂账按币种合计、转入余额记到用户余额且只能一次；渠道启停；收入调整登记、生效日上限、冲销与重复冲销 409。起服务与发请求用 tests/mock-helpers.ts，登录与 reauth 辅助留在本文件（登录带状态断言）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import {
  adjustmentSchema,
  adjustmentsSchema,
  cancelledSchema,
  latePaymentsSchema,
  manualCreatedSchema,
  markedPaidSchema,
  orderResponseSchema,
  ordersSchema,
  paymentHistorySchema,
  providersSchema,
} from '../src/admin/screens/billing/schemas'
import { bearer, close, mockFetch, serve } from './mock-helpers'

describe('mock api · admin billing', () => {
  let server: Server
  let base: string
  let admin: string
  let viewer: string
  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    admin = await login(MOCK_ACCOUNTS.admin)
    viewer = await login(MOCK_ACCOUNTS.viewer)
  })
  afterAll(() => close(server))

  async function login(account: { email: string; password: string }): Promise<string> {
    const res = await fetch(`${base}/v1/auth/login`, { method: 'POST', body: JSON.stringify(account) })
    expect(res.status).toBe(200)
    return ((await res.json()) as { access_token: string }).access_token
  }
  const get = (token: string, path: string) => mockFetch(base, bearer(token), 'GET', `/v1/${path}`)
  const post = (token: string, path: string, body: unknown, key?: string) => mockFetch(base, bearer(token), 'POST', `/v1/${path}`, body, key)
  const json = async <T>(res: Response) => (await res.json()) as T

  // dev/mock/admin/users.ts 的第 3 个种子用户（id 固定）
  const SEED_USER = '1a2b3c43-0000-4000-8000-000000000003'
  const firstPlan = async () => {
    const plans = await json<{ plans: Array<{ id: string; status: string; prices: Array<{ id: string; status: string; currency: string }> }> }>(await get(admin, 'plans'))
    const plan = plans.plans.find((p) => p.status === 'active')!
    return { plan_id: plan.id, price_id: plan.prices.find((x) => x.status === 'active' && x.currency === 'CNY')!.id }
  }

  it('shows the viewer the order list and nothing else', async () => {
    const list = ordersSchema.parse(await json(await get(viewer, 'orders')))
    expect(list.total).toBeGreaterThan(list.orders.length - 1)
    expect((await get(viewer, `orders/${list.orders[0]!.id}/payments`)).status).toBe(404)
    expect((await get(viewer, 'late-payments')).status).toBe(404)
    expect((await get(viewer, 'payment-providers')).status).toBe(404)
    expect((await get(viewer, 'revenue/adjustments')).status).toBe(404)
    // 权限先于 reauth：只读账号拿到的是 404，不是 reauth 提示
    expect((await post(viewer, 'orders/manual', {}, 'viewer-manual')).status).toBe(404)
  })

  it('counts the dashboard stale-pending card from the same orders the pending filter lists', async () => {
    const pending = ordersSchema.parse(await json(await get(admin, 'orders?status=pending_payment&limit=100'))).orders
    const stale = pending.filter((o) => Date.now() - Date.parse(o.created_at) > 1800 * 1000).length
    expect(stale).toBeGreaterThan(0)
    const tasks = await json<{ items: Array<{ kind: string; count?: number }> }>(await get(admin, 'dashboard/tasks'))
    expect(tasks.items.find((t) => t.kind === 'orders_pending_stale')!.count).toBe(stale)
  })

  it('serves orders the page schemas accept, with multi-status and user filters (R63)', async () => {
    const pending = ordersSchema.parse(await json(await get(admin, 'orders?status=draft,pending_payment,processing&limit=100')))
    expect(pending.orders.length).toBeGreaterThan(0)
    expect(pending.orders.every((o) => ['draft', 'pending_payment', 'processing'].includes(o.status))).toBe(true)
    expect((await get(admin, 'orders?status=pending')).status).toBe(400)
    expect((await get(admin, 'orders?user_id=nope')).status).toBe(400)
    const all = ordersSchema.parse(await json(await get(admin, 'orders?limit=100')))
    expect(all.orders.some((o) => o.kind === 'addon' && o.plan_name.includes('加油包'))).toBe(true)
    expect(all.orders.some((o) => o.provider_name === null && o.balance_applied === o.total_amount && o.total_amount > 0)).toBe(true)

    // 用户详情的最近订单与订单页同一份：深链 #/billing/orders/<id> 落得到
    const user = await json<{ recent_orders: Array<{ id: string }> }>(await get(admin, `users/${SEED_USER}`))
    const mine = ordersSchema.parse(await json(await get(admin, `orders?user_id=${SEED_USER}`)))
    expect(mine.orders.map((o) => o.id).sort()).toEqual(user.recent_orders.map((o) => o.id).sort())
    const detail = orderResponseSchema.parse(await json(await get(admin, `orders/${user.recent_orders[0]!.id}`)))
    expect(detail.order.user_id).toBe(SEED_USER)
    paymentHistorySchema.parse(await json(await get(admin, `orders/${detail.order.id}/payments`)))
    expect((await get(admin, 'orders/not-a-uuid')).status).toBe(404)
  })

  it('creates manual orders after reauth, replays the 201, and refuses balance and reused receipts', async () => {
    const { plan_id, price_id } = await firstPlan()
    const body = { user_id: SEED_USER, plan_id, price_id, reason: '对公转账客户先开单', settlement: 'pending' }
    await fetch(`${base}/__mock/expire-reauth`, { method: 'POST' })
    const blocked = await post(admin, 'orders/manual', body, 'manual-1')
    expect(blocked.status).toBe(403)
    expect(await blocked.json()).toMatchObject({ error: { code: 'reauth_required' } })
    const reauth = await post(admin, 'auth/reauth', { password: MOCK_ACCOUNTS.admin.password })
    admin = (await json<{ access_token: string }>(reauth)).access_token

    const first = await post(admin, 'orders/manual', body, 'manual-1')
    expect(first.status).toBe(201)
    const created = manualCreatedSchema.parse(await first.json())
    expect(created.status).toBe('pending_payment')
    const replay = await post(admin, 'orders/manual', body, 'manual-1')
    expect(replay.status).toBe(201)
    expect(await replay.json()).toEqual(created)

    const detail = orderResponseSchema.parse(await json(await get(admin, `orders/${created.order_id}`))).order
    expect(detail.manual_reason).toBe(body.reason)
    expect(detail.created_by_email).toBe(MOCK_ACCOUNTS.admin.email)
    const user = await json<{ recent_orders: Array<{ id: string }> }>(await get(admin, `users/${SEED_USER}`))
    expect(user.recent_orders.some((o) => o.id === created.order_id)).toBe(true)

    expect(await json(await post(admin, 'orders/manual', { ...body, settlement: 'balance' }, 'manual-2'))).toMatchObject({ error: { code: 'validation_failed', fields: { settlement: expect.any(String) } } })
    expect(await json(await post(admin, 'orders/manual', { ...body, settlement: 'offline' }, 'manual-3'))).toMatchObject({ error: { fields: { reference: expect.any(String) } } })
    expect((await post(admin, 'orders/manual', { ...body, extra: 1 }, 'manual-4')).status).toBe(400)

    const offline = await post(admin, 'orders/manual', { ...body, settlement: 'offline', reference: 'ICBC-TEST-1' }, 'manual-5')
    expect(offline.status).toBe(201)
    expect(manualCreatedSchema.parse(await offline.json()).status).toBe('fulfilled')
    const reused = await post(admin, 'orders/manual', { ...body, settlement: 'offline', reference: 'ICBC-TEST-1' }, 'manual-6')
    expect(reused.status).toBe(409)
    expect(await reused.json()).toMatchObject({ error: { message: '凭证号已用于其他订单' } })

    const grant = manualCreatedSchema.parse(await json(await post(admin, 'orders/manual', { ...body, settlement: 'grant' }, 'manual-7')))
    expect(grant).toMatchObject({ status: 'fulfilled', total_amount: 0, payable_amount: 0 })
    expect(grant.discount_amount).toBeGreaterThan(0)

    // 标记已支付：凭证号不能与别的单重复，成功后开通
    const markBody = { reason: '客户已对公转账到账', reference: 'ICBC-TEST-1' }
    const clash = await post(admin, `orders/${created.order_id}/mark-paid`, markBody, 'mark-1')
    expect(clash.status).toBe(409)
    const paid = markedPaidSchema.parse(await json(await post(admin, `orders/${created.order_id}/mark-paid`, { ...markBody, reference: 'ICBC-TEST-2' }, 'mark-2')))
    expect(paid).toMatchObject({ processed: true, already_handled: false })
    expect(paid.subscription_id).not.toBe('')
    const after = orderResponseSchema.parse(await json(await get(admin, `orders/${created.order_id}`))).order
    expect(after.status).toBe('fulfilled')
    const history = paymentHistorySchema.parse(await json(await get(admin, `orders/${created.order_id}/payments`)))
    expect(history.payments[0]).toMatchObject({ provider_code: 'offline', provider_payment_id: 'offline:ICBC-TEST-2', payment_intent_id: null })
    expect((await post(admin, `orders/${created.order_id}/mark-paid`, { ...markBody, reference: 'ICBC-TEST-3' }, 'mark-3')).status).toBe(409)
  })

  it('cancels with a state_version compare-and-swap and refuses paid orders', async () => {
    const pending = ordersSchema.parse(await json(await get(admin, 'orders?status=pending_payment&limit=100'))).orders[0]!
    const order = orderResponseSchema.parse(await json(await get(admin, `orders/${pending.id}`))).order
    const stale = await post(admin, `orders/${order.id}/cancel`, { expected_state_version: order.state_version + 5, reason: '用户要求取消订单' }, 'cancel-1')
    expect(stale.status).toBe(409)
    expect((await post(admin, `orders/${order.id}/cancel`, { expected_state_version: order.state_version, reason: '短' }, 'cancel-2')).status).toBe(400)
    const body = { expected_state_version: order.state_version, reason: '用户要求取消订单' }
    const done = cancelledSchema.parse(await json(await post(admin, `orders/${order.id}/cancel`, body, 'cancel-3')))
    expect(done).toMatchObject({ already_terminal: false, order: { status: 'cancelled', state_version: order.state_version + 1 } })
    expect(cancelledSchema.parse(await json(await post(admin, `orders/${order.id}/cancel`, body, 'cancel-3')))).toEqual(done)

    const paid = ordersSchema.parse(await json(await get(admin, 'orders?status=paid,fulfilled&limit=100'))).orders.find((o) => o.paid_amount > 0)!
    const paidDetail = orderResponseSchema.parse(await json(await get(admin, `orders/${paid.id}`))).order
    const refused = await post(admin, `orders/${paid.id}/cancel`, { expected_state_version: paidDetail.state_version, reason: '用户要求取消订单' }, 'cancel-4')
    expect(await refused.json()).toMatchObject({ error: { code: 'conflict', message: '订单已支付，不能取消' } })
  })

  it('totals late payments per currency and applies one to the balance exactly once (R3)', async () => {
    const late = latePaymentsSchema.parse(await json(await get(admin, 'late-payments')))
    expect(Object.keys(late.pending_amounts).sort()).toEqual(['CNY', 'USD'])
    expect(late.cases[0]!.status).toBe('suspense')
    expect(late.cases.find((c) => c.status === 'suspense')!.resolved_at).toBeUndefined()
    expect((await get(admin, 'late-payments?status=overdue')).status).toBe(400)

    const c = late.cases.find((x) => x.status === 'suspense' && x.currency === 'CNY')!
    const balanceOf = async () => (await json<{ balance: number }>(await get(admin, `users/${c.user_id}`))).balance
    const before = await balanceOf()
    expect(await json(await post(admin, `late-payments/${c.id}/apply-to-balance`, { reason: '短' }, 'late-1'))).toMatchObject({ error: { fields: { reason: expect.any(String) } } })
    expect((await post(admin, `late-payments/${c.id}/apply-to-balance`, { reason: '用户确认转入余额' }, 'late-2')).status).toBe(200)
    expect(await balanceOf()).toBe(before + c.amount)
    const again = await post(admin, `late-payments/${c.id}/apply-to-balance`, { reason: '用户确认转入余额' }, 'late-3')
    expect(await again.json()).toMatchObject({ error: { code: 'conflict', message: '这笔挂账已经处理过了' } })
    const applied = latePaymentsSchema.parse(await json(await get(admin, 'late-payments?status=applied')))
    expect(applied.cases.some((x) => x.id === c.id && x.resolution_reason === '用户确认转入余额')).toBe(true)
  })

  it('lists providers with card stats and toggles accepting_new (R66)', async () => {
    const list = providersSchema.parse(await json(await get(admin, 'payment-providers'))).providers
    expect(list.map((p) => p.code)).toEqual(['demo', 'epay', 'epay_backup', 'offline'])
    expect(list.find((p) => p.code === 'epay')!.today.CNY).toBeGreaterThan(0)
    expect((await post(admin, 'payment-providers/nope/toggle', { enabled: true, accepting_new: true })).status).toBe(404)
    expect((await post(admin, 'payment-providers/epay/toggle', { enabled: true })).status).toBe(200)
    const epay = providersSchema.parse(await json(await get(admin, 'payment-providers'))).providers.find((p) => p.code === 'epay')!
    expect(epay).toMatchObject({ enabled: true, accepting_new: false })
  })

  it('records, lists and reverses revenue adjustments once', async () => {
    const future = new Date(Date.now() + 3 * 86_400_000).toISOString().slice(0, 10)
    expect(await json(await post(admin, 'revenue/adjustments', { currency: 'CNY', amount: 100, reason: '补录线下收入', effective_on: future }, 'adj-1'))).toMatchObject({
      error: { fields: { effective_on: expect.any(String) } },
    })
    expect(await json(await post(admin, 'revenue/adjustments', { currency: 'EUR', amount: 0, reason: '短' }, 'adj-2'))).toMatchObject({
      error: { fields: { currency: expect.any(String), amount: expect.any(String), reason: expect.any(String) } },
    })
    const created = adjustmentSchema.parse(await json(await post(admin, 'revenue/adjustments', { currency: 'CNY', amount: -2050, reason: '重复扣款退回' }, 'adj-3')))
    expect(created).toMatchObject({ amount: -2050, reversed: false, created_by_email: MOCK_ACCOUNTS.admin.email })
    expect(created.reversal_of).toBeUndefined()
    const usd = adjustmentsSchema.parse(await json(await get(admin, 'revenue/adjustments?currency=USD'))).adjustments
    expect(usd.every((a) => a.currency === 'USD')).toBe(true)
    expect((await get(admin, 'revenue/adjustments?currency=EUR')).status).toBe(422)

    const reversal = adjustmentSchema.parse(await json(await post(admin, `revenue/adjustments/${created.id}/reverse`, { reason: '冲销：重复扣款退回' }, 'rev-1')))
    expect(reversal).toMatchObject({ amount: 2050, reversal_of: created.id, effective_on: created.effective_on })
    const list = adjustmentsSchema.parse(await json(await get(admin, 'revenue/adjustments'))).adjustments
    expect(list[0]!.id).toBe(reversal.id)
    expect(list.find((a) => a.id === created.id)!.reversed).toBe(true)
    expect(await json(await post(admin, `revenue/adjustments/${created.id}/reverse`, { reason: '再冲销一次试试' }, 'rev-2'))).toMatchObject({ error: { message: '该调整已撤销' } })
    expect(await json(await post(admin, `revenue/adjustments/${reversal.id}/reverse`, { reason: '冲销反向记录' }, 'rev-3'))).toMatchObject({ error: { message: '反向记录不能再次撤销' } })
  })
})
