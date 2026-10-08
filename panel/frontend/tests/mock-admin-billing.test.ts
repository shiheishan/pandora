import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import {
  adjustmentSchema,
  adjustmentsSchema,
  cancelledSchema,
  latePaymentsSchema,
  manualCreatedSchema,
  manualPreviewSchema,
  markedPaidSchema,
  orderQueriedSchema,
  orderResponseSchema,
  ordersSchema,
  paymentHistorySchema,
  providersSchema,
  providerWrittenSchema,
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
  const put = (token: string, path: string, body: unknown, key?: string) => mockFetch(base, bearer(token), 'PUT', `/v1/${path}`, body, key)
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
    // 种子用户已有别的套餐的订阅：落点不止一个，必须带 target（这里选另开一份）
    const body = { user_id: SEED_USER, plan_id, price_id, reason: '对公转账客户先开单', settlement: 'pending', target: { kind: 'new' } }
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

  // ---- 人工开单的落点（购买模型统一）----------------------------------------------
  // 种子用户（dev/mock/admin/users.ts 的落点演示数据）：
  //   …02 专业版「我的手机」+ 体验版「妈妈的 iPad」，两份都在用、不同款
  //   …06 两份专业版（「常用」「备用」），同款都在用
  //   …01 只有一份标准版
  const DUO = '1a2b3c42-0000-4000-8000-000000000002'
  const TWIN = '1a2b3c46-0000-4000-8000-000000000006'
  const SOLO = '1a2b3c41-0000-4000-8000-000000000001'
  type SubRow = { id: string; label: string | null; plan_name: string; status: string; current_period_end: string; current_period_start: string; quotas: Array<{ consumed: number }> }
  type Detail = { balance: number; subscriptions: Array<SubRow & { pack_remaining_bytes: number }>; recent_orders: Array<{ id: string; kind: string }>; unattached_pack_bytes: number }
  const detailOf = async (id: string) => json<Detail>(await get(admin, `users/${id}`))
  const planByName = async (name: string) => {
    const plans = await json<{ plans: Array<{ id: string; name: string; status: string; prices: Array<{ id: string; status: string; currency: string; unit_amount: number }> }> }>(await get(admin, 'plans'))
    const plan = plans.plans.find((p) => p.name === name && p.status === 'active')!
    const price = plan.prices.find((x) => x.status === 'active' && x.currency === 'CNY')!
    return { plan_id: plan.id, price_id: price.id, amount: price.unit_amount }
  }
  const preview = async (user: string, plan: { plan_id: string; price_id: string }, entry?: string, token = admin) =>
    manualPreviewSchema.parse(await json(await post(token, 'orders/manual/preview', { user_id: user, plan_id: plan.plan_id, price_id: plan.price_id, ...(entry ? { entry_subscription_id: entry } : {}) })))
  const reauthed = async () => {
    const res = await post(admin, 'auth/reauth', { password: MOCK_ACCOUNTS.admin.password })
    return (await json<{ access_token: string }>(res)).access_token
  }

  it('previews where a manual order can land: same plan renews, different plans never preselect', async () => {
    const standard = await planByName('标准版')
    const pro = await planByName('专业版')
    const duo = await detailOf(DUO)
    const phone = duo.subscriptions.find((x) => x.label === '我的手机')!
    const ipad = duo.subscriptions.find((x) => x.label === '妈妈的 iPad')!

    // 开一个用户两份都不是的套餐：新开 + 换掉任何一份，没有默认值（按钮置灰「先选落点」）
    const none = await preview(DUO, standard)
    expect(none.options.map((o) => o.kind).sort()).toEqual(['change', 'change', 'new'])
    expect(none.default_key).toBe('')
    const change = none.options.find((o) => o.kind === 'change' && o.subscription_id === phone.id)!
    expect(change).toMatchObject({ key: `change:${phone.id}`, label: '我的手机', plan_name: '专业版', state: 'live' })
    expect(change.new_period_end).toBeDefined()
    // 生效中的订阅换套餐有剩余价值可抵；每一项都写明落地之后的到期日
    expect(change.credit).toBeGreaterThan(0)
    expect(none.options.every((o) => o.new_period_end !== undefined)).toBe(true)
    expect(none.options.find((o) => o.kind === 'new')).toMatchObject({ key: 'new' })

    // 从订阅行点「给这份开单」进来：入口那份预选，换掉也行（管理员自己点的）
    expect((await preview(DUO, standard, ipad.id)).default_key).toBe(`change:${ipad.id}`)
    // 入口订阅不在选项里就当没给
    expect((await preview(DUO, standard, '00000000-0000-4000-8000-000000000000')).default_key).toBe('')

    // 开用户已有的那款：同款排最前并默认，徽标 same_plan；其余仍可选
    const same = await preview(DUO, pro)
    expect(same.options[0]).toMatchObject({ key: `renew:${phone.id}`, badge: 'same_plan', label: '我的手机' })
    expect(same.default_key).toBe(`renew:${phone.id}`)
    expect(same.options.map((o) => o.key)).toEqual([`renew:${phone.id}`, 'new', `change:${ipad.id}`])
    // 生效中的同款续一期：到期日接在原到期日后
    expect(Date.parse(same.options[0]!.new_period_end!)).toBeGreaterThan(Date.parse(same.options[0]!.period_end!))

    // 两份同款：按到期从早到晚，默认第一份
    const twin = await detailOf(TWIN)
    const byEnd = [...twin.subscriptions].sort((a, b) => a.current_period_end.localeCompare(b.current_period_end))
    const twinPreview = await preview(TWIN, pro)
    expect(twinPreview.options.map((o) => o.key)).toEqual([`renew:${byEnd[0]!.id}`, `renew:${byEnd[1]!.id}`, 'new'])
    expect(twinPreview.default_key).toBe(`renew:${byEnd[0]!.id}`)
    expect((await preview(TWIN, pro, byEnd[1]!.id)).default_key).toBe(`renew:${byEnd[1]!.id}`)

    // 只有一份而且就是同款：只剩一项，不让选
    const solo = await preview(SOLO, standard)
    expect(solo.options).toHaveLength(1)
    expect(solo.options[0]!.kind).toBe('renew')
    expect(solo.options[0]!.badge).toBeUndefined()
    expect(solo.default_key).toBe(solo.options[0]!.key)

    // 没有订阅的用户：只有新开一份
    const nobody = await json<{ users: Array<{ id: string }> }>(await get(admin, 'users?sub_state=none&limit=1'))
    const fresh = await preview(nobody.users[0]!.id, standard)
    expect(fresh.options.map((o) => o.key)).toEqual(['new'])
    expect(fresh.default_key).toBe('new')
  })

  it('guards the preview: permission, unknown fields, malformed ids land on their fields (F9)', async () => {
    const standard = await planByName('标准版')
    const body = { user_id: DUO, plan_id: standard.plan_id, price_id: standard.price_id }
    expect((await post(viewer, 'orders/manual/preview', body)).status).toBe(404)
    expect((await post(admin, 'orders/manual/preview', { ...body, settlement: 'grant' })).status).toBe(400)
    expect((await post(admin, 'orders/manual/preview', { ...body, user_id: 'nope' })).status).toBe(400)
    // 价格档、入口订阅不是合法标识：422 并带对应的 fields，两项一起报
    expect(await json(await post(admin, 'orders/manual/preview', { ...body, entry_subscription_id: 'nope' }))).toEqual({
      error: expect.objectContaining({ code: 'validation_failed', fields: { entry_subscription_id: '订阅标识不正确' } }),
    })
    const both = await post(admin, 'orders/manual/preview', { ...body, price_id: 'nope', entry_subscription_id: 'nope' })
    expect(both.status).toBe(422)
    expect(await json(both)).toMatchObject({ error: { fields: { price_id: '价格档标识不正确', entry_subscription_id: '订阅标识不正确' } } })
    // 合法但不存在的价格档不报错（offerPeriod 的 LEFT JOIN），新到期日按一个月算；不存在的用户 404
    expect(manualPreviewSchema.parse(await json(await post(admin, 'orders/manual/preview', { ...body, price_id: '00000000-0000-4000-8000-000000000000' }))).options.length).toBeGreaterThan(0)
    expect((await post(admin, 'orders/manual/preview', { ...body, user_id: '00000000-0000-4000-8000-000000000000' })).status).toBe(404)
    // 只读预览不要重新认证
    await fetch(`${base}/__mock/expire-reauth`, { method: 'POST' })
    expect((await post(admin, 'orders/manual/preview', body)).status).toBe(200)
  })

  it('refuses to guess the landing: no target with several options, a stale target, a malformed one', async () => {
    const standard = await planByName('标准版')
    admin = await reauthed()
    const body = { user_id: DUO, plan_id: standard.plan_id, price_id: standard.price_id, reason: '对公转账客户开单', settlement: 'grant' }
    const none = await post(admin, 'orders/manual', body, 'place-1')
    expect(none.status).toBe(422)
    expect(await json(none)).toMatchObject({ error: { code: 'validation_failed', message: '请选择这单落到哪一份' } })
    const stale = await post(admin, 'orders/manual', { ...body, target: { kind: 'renew', subscription_id: '00000000-0000-4000-8000-000000000000' } }, 'place-2')
    expect(stale.status).toBe(422)
    expect(await json(stale)).toMatchObject({ error: { message: '这个用法现在不能用了，请刷新后再选' } })
    // 同款才有 renew：标准版对「专业版」那份没有续费这个选项
    const duo = await detailOf(DUO)
    const wrongKind = await post(admin, 'orders/manual', { ...body, target: { kind: 'renew', subscription_id: duo.subscriptions[0]!.id } }, 'place-3')
    expect(wrongKind.status).toBe(422)
    expect((await post(admin, 'orders/manual', { ...body, target: { kind: 'new', extra: 1 } }, 'place-4')).status).toBe(400)
    expect((await post(admin, 'orders/manual', { ...body, target: 'new' }, 'place-5')).status).toBe(400)
    // 一个订单都没建出来
    expect((await detailOf(DUO)).subscriptions).toHaveLength(duo.subscriptions.length)
  })

  it('renews the chosen subscription in place: same link, later end, no new subscription', async () => {
    const pro = await planByName('专业版')
    admin = await reauthed()
    const before = await detailOf(TWIN)
    const byEnd = [...before.subscriptions].sort((a, b) => a.current_period_end.localeCompare(b.current_period_end))
    const [earlier, later] = [byEnd[0]!, byEnd[1]!]
    // 选第二份（不是默认的第一份）：只有它往后推
    const body = { user_id: TWIN, plan_id: pro.plan_id, price_id: pro.price_id, reason: '线下续费一个月', settlement: 'grant', target: { kind: 'renew', subscription_id: later.id } }
    const created = manualCreatedSchema.parse(await json(await post(admin, 'orders/manual', body, 'renew-1')))
    expect(created.status).toBe('fulfilled')
    expect(created.proration_credit).toBeUndefined()
    const after = await detailOf(TWIN)
    expect(after.subscriptions).toHaveLength(before.subscriptions.length)
    expect(Date.parse(after.subscriptions.find((x) => x.id === later.id)!.current_period_end)).toBeGreaterThan(Date.parse(later.current_period_end))
    expect(after.subscriptions.find((x) => x.id === earlier.id)!.current_period_end).toBe(earlier.current_period_end)
    const order = orderResponseSchema.parse(await json(await get(admin, `orders/${created.order_id}`))).order
    expect(order).toMatchObject({ kind: 'renewal', subscription_id: later.id })
  })

  it('changes the chosen subscription to the new plan, refunds what is left, and keeps the link', async () => {
    const standard = await planByName('标准版')
    admin = await reauthed()
    const before = await detailOf(DUO)
    const phone = before.subscriptions.find((x) => x.label === '我的手机')!
    const seen = await preview(DUO, standard)
    const credit = seen.options.find((o) => o.subscription_id === phone.id)!.credit!
    const body = { user_id: DUO, plan_id: standard.plan_id, price_id: standard.price_id, reason: '客户要求降级', settlement: 'grant', target: { kind: 'change', subscription_id: phone.id } }
    const created = manualCreatedSchema.parse(await json(await post(admin, 'orders/manual', body, 'change-1')))
    // 赠送：新价算 0 元，原套餐的剩余价值全额退进余额
    expect(created).toMatchObject({ status: 'fulfilled', total_amount: 0, payable_amount: 0 })
    expect(created.balance_refund).toBeGreaterThan(0)
    expect(Math.abs(created.balance_refund! - credit)).toBeLessThan(Math.max(5, credit * 0.01))
    const after = await detailOf(DUO)
    expect(after.subscriptions).toHaveLength(before.subscriptions.length)
    expect(after.subscriptions.find((x) => x.id === phone.id)).toMatchObject({ plan_name: '标准版', label: '我的手机', status: 'active' })
    expect(after.balance).toBe(before.balance + created.balance_refund!)
  })

  it('attaches unattached traffic to a first subscription, and leaves it alone when several are in use', async () => {
    const GiB = 1024 ** 3
    const standard = await planByName('标准版')
    admin = await reauthed()
    // 第 7 位种子用户没有订阅、兑过 20 GiB 的送流量卡：开第一份时自动挂上（billing provision）
    const found = await json<{ users: Array<{ id: string }> }>(await get(admin, 'users?q=grace.h@foxmail.com&limit=1'))
    const lone = found.users[0]!.id
    expect(await detailOf(lone)).toMatchObject({ subscriptions: [], unattached_pack_bytes: 20 * GiB })
    const first = await post(admin, 'orders/manual', { user_id: lone, plan_id: standard.plan_id, price_id: standard.price_id, reason: '首次开通赠送', settlement: 'grant' }, 'attach-1')
    expect(first.status).toBe(201)
    const after = await detailOf(lone)
    expect(after.unattached_pack_bytes).toBe(0)
    expect(after.subscriptions).toHaveLength(1)
    expect(after.subscriptions[0]!.pack_remaining_bytes).toBe(20 * GiB)
    // …02 两份不同款都在用，未分配的 5 GiB 不会被系统挑一份挂上
    expect((await detailOf(DUO)).unattached_pack_bytes).toBe(5 * GiB)
  })

  it('opens another copy when asked to, and a single option needs no target', async () => {
    const standard = await planByName('标准版')
    admin = await reauthed()
    const before = await detailOf(TWIN)
    const another = manualCreatedSchema.parse(await json(await post(admin, 'orders/manual', { user_id: TWIN, plan_id: standard.plan_id, price_id: standard.price_id, reason: '再开一份分开用', settlement: 'grant', target: { kind: 'new' } }, 'new-1')))
    expect(another.status).toBe('fulfilled')
    const after = await detailOf(TWIN)
    expect(after.subscriptions).toHaveLength(before.subscriptions.length + 1)
    expect(after.subscriptions.filter((x) => x.plan_name === '标准版')).toHaveLength(1)

    // 没有订阅的用户只有一个选项，不带 target 也行
    const nobody = await json<{ users: Array<{ id: string }> }>(await get(admin, 'users?sub_state=none&limit=1'))
    const only = await post(admin, 'orders/manual', { user_id: nobody.users[0]!.id, plan_id: standard.plan_id, price_id: standard.price_id, reason: '首次开通赠送', settlement: 'grant' }, 'new-2')
    expect(only.status).toBe(201)
    expect((await detailOf(nobody.users[0]!.id)).subscriptions).toHaveLength(1)
  })

  it('applies a pending renewal only when it is paid, and offsets the credit against the price', async () => {
    const pro = await planByName('专业版')
    admin = await reauthed()
    const solo = await detailOf(SOLO)
    const sub = solo.subscriptions[0]!
    const std = await planByName('标准版')
    // 待用户支付的续费单：订阅此刻不动
    const pending = manualCreatedSchema.parse(await json(await post(admin, 'orders/manual', { user_id: SOLO, plan_id: std.plan_id, price_id: std.price_id, reason: '先开单后付款', settlement: 'pending' }, 'pay-1')))
    expect(pending).toMatchObject({ status: 'pending_payment', payable_amount: std.amount })
    expect((await detailOf(SOLO)).subscriptions[0]!.current_period_end).toBe(sub.current_period_end)
    // 标记已支付之后才续上
    const paid = await post(admin, `orders/${pending.order_id}/mark-paid`, { reason: '客户已转账', reference: 'ICBC-PLACE-1' }, 'pay-2')
    expect(paid.status).toBe(200)
    expect(Date.parse((await detailOf(SOLO)).subscriptions[0]!.current_period_end)).toBeGreaterThan(Date.parse(sub.current_period_end))

    // 换套餐、不是赠送：应付 = 新价 − 剩余价值，抵不完的才付；线下已收款的凭证金额也是抵扣后的
    const seen = await preview(DUO, pro)
    // 体验版是免费套餐，没有剩余价值可抵；挑一份有剩余价值的
    const paying = seen.options.find((o) => o.kind === 'change' && (o.credit ?? 0) > 0)!
    const credit = paying.credit!
    expect(credit).toBeGreaterThan(0)
    const change = manualCreatedSchema.parse(
      await json(
        await post(admin, 'orders/manual', { user_id: DUO, plan_id: pro.plan_id, price_id: pro.price_id, reason: '客户升级补差价', settlement: 'pending', target: { kind: 'change', subscription_id: paying.subscription_id } }, 'pay-3'),
      ),
    )
    expect(change.payable_amount).toBe(Math.max(pro.amount - credit, 0))
    expect(change.proration_credit).toBe(credit)
    expect(change.balance_refund).toBe(0)
    // 待支付的换套餐单：付款前订阅还是原套餐
    expect((await detailOf(DUO)).subscriptions.find((x) => x.id === paying.subscription_id)!.plan_name).toBe(paying.plan_name)
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

  it('queries the channel with write permission and a key, no reauth, and reconciles a lost callback (PAY-009)', async () => {
    const pending = ordersSchema.parse(await json(await get(admin, 'orders?status=pending_payment,processing&limit=100'))).orders
    const lost = pending.find((o) => o.provider_code === 'epay' && Date.now() - Date.parse(o.created_at) > 3600 * 1000)!
    const fresh = pending.find((o) => o.provider_code === 'epay' && Date.now() - Date.parse(o.created_at) < 3600 * 1000)!
    const failing = pending.find((o) => o.provider_code === 'epay_backup')!
    const neverPaid = pending.find((o) => o.provider_code === null)!
    const query = (token: string, id: string, key?: string) => mockFetch(base, bearer(token), 'POST', `/v1/orders/${id}/query`, undefined, key)

    expect((await query(viewer, lost.id, 'pq-viewer')).status).toBe(404)
    expect((await query(admin, lost.id)).status).toBe(400)
    const first = await query(admin, lost.id, 'pq-lost')
    expect(first.status).toBe(200)
    const body = orderQueriedSchema.parse(await first.json())
    expect(body).toMatchObject({ channel_status: 'paid', reconciled: true, already_recorded: false, order_status: 'fulfilled' })
    // 同键重放拿回同一份结果；换键再查是已入账
    expect(orderQueriedSchema.parse(await (await query(admin, lost.id, 'pq-lost')).json())).toEqual(body)
    expect(orderQueriedSchema.parse(await (await query(admin, lost.id, 'pq-lost-2')).json())).toMatchObject({ reconciled: false, already_recorded: true })
    const after = orderResponseSchema.parse(await json(await get(admin, `orders/${lost.id}`))).order
    expect(after.status).toBe('fulfilled')
    expect(paymentHistorySchema.parse(await json(await get(admin, `orders/${lost.id}/payments`))).payments.length).toBe(1)

    expect(orderQueriedSchema.parse(await (await query(admin, fresh.id, 'pq-fresh')).json())).toMatchObject({ channel_status: 'unpaid', reconciled: false })
    const down = await query(admin, failing.id, 'pq-failing')
    expect(down.status).toBe(503)
    expect((await json<{ error: { message: string } }>(down)).error.message).toBe('渠道查单失败，请稍后再试')
    const never = await query(admin, neverPaid.id, 'pq-never')
    expect(never.status).toBe(409)
    expect((await json<{ error: { message: string } }>(never)).error.message).toBe('该订单从未发起过支付，无法向渠道查单')
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

  it('lists and applies ineligible-subscription late payments, and mark-paid quarantines ended renewals (R117)', async () => {
    const late = latePaymentsSchema.parse(await json(await get(admin, 'late-payments')))
    const seeded = late.cases.find((c) => c.case_kind === 'ineligible_subscription' && c.status === 'suspense')!
    expect(seeded).toBeDefined()
    expect((await post(admin, `late-payments/${seeded.id}/apply-to-balance`, { reason: '订阅已结束，续费款转入余额' }, 'late-ine-1')).status).toBe(200)

    const ended = ordersSchema.parse(await json(await get(admin, 'orders?status=pending_payment&limit=100'))).orders.filter((o) => o.kind === 'renewal')
    expect(ended).toHaveLength(1)
    const order = ended[0]!
    const markBody = { reason: '客户银行转账续费', reference: 'ICBC-ENDED-1' }
    const quarantined = await post(admin, `orders/${order.id}/mark-paid`, markBody, 'mark-ended-1')
    expect(quarantined.status).toBe(409)
    expect(await quarantined.json()).toMatchObject({ error: { code: 'conflict', message: '订阅已结束，款项已转入挂账，可在挂账里转入用户余额' } })
    expect(orderResponseSchema.parse(await json(await get(admin, `orders/${order.id}`))).order.status).toBe('pending_payment')
    const after = latePaymentsSchema.parse(await json(await get(admin, 'late-payments')))
    expect(after.cases.some((c) => c.case_kind === 'ineligible_subscription' && c.order_no === order.order_no && c.status === 'suspense')).toBe(true)
    const again = await post(admin, `orders/${order.id}/mark-paid`, markBody, 'mark-ended-2')
    expect(again.status).toBe(409)
    expect(await again.json()).toMatchObject({ error: { message: '凭证号 ICBC-ENDED-1 已经入过账，不能重复标记' } })
  })

  it('lists providers with card stats and toggles accepting_new (R66)', async () => {
    const list = providersSchema.parse(await json(await get(admin, 'payment-providers'))).providers
    expect(list.map((p) => p.code)).toEqual(['demo', 'epay', 'epay_backup', 'offline'])
    expect(list.find((p) => p.code === 'epay')!.today.CNY).toBeGreaterThan(0)
    // 最低付款额（分）：易支付默认 ¥1.00，演示与线下渠道没有这个概念回 0
    expect(list.map((p) => [p.code, p.min_amount])).toEqual([['demo', 0], ['epay', 100], ['epay_backup', 100], ['offline', 0]])
    expect((await post(admin, 'payment-providers/nope/toggle', { enabled: true, accepting_new: true })).status).toBe(404)
    expect((await post(admin, 'payment-providers/epay/toggle', { enabled: true })).status).toBe(200)
    const epay = providersSchema.parse(await json(await get(admin, 'payment-providers'))).providers.find((p) => p.code === 'epay')!
    expect(epay).toMatchObject({ enabled: true, accepting_new: false })
  })

  it('creates and edits an epay provider; secrets are write-only (w2pay)', async () => {
    const settings = { display_name: '易支付 · 新', base_url: 'https://pay3.example.com', submit_path: '', api_path: '', methods: ['wxpay', 'alipay'], default_method: '', allow_private_host: false }
    // 只读财务看得到渠道，建不了
    expect((await post(viewer, 'payment-providers', { code: 'epay3', adapter: 'epay', ...settings, merchant_id: '2001', key: 'k' }, 'prov-v')).status).toBe(404)
    expect(await json(await post(admin, 'payment-providers', { code: 'offline', adapter: 'demo', ...settings, methods: ['paypal'], merchant_id: '', key: '' }, 'prov-0'))).toMatchObject({
      error: { code: 'validation_failed', fields: { code: expect.any(String), adapter: expect.any(String), methods: expect.any(String), merchant_id: '必填', key: '必填' } },
    })
    const created = await post(admin, 'payment-providers', { code: 'epay3', adapter: 'epay', ...settings, merchant_id: '2001', key: 'secret-2001' }, 'prov-1')
    expect(created.status).toBe(201)
    expect(providerWrittenSchema.parse(await json(created))).toMatchObject({ code: 'epay3', credentials_changed: true })
    expect((await post(admin, 'payment-providers', { code: 'epay3', adapter: 'epay', ...settings, merchant_id: '2001', key: 'x' }, 'prov-2')).status).toBe(409)

    const listed = await get(admin, 'payment-providers')
    const raw = await listed.text()
    expect(raw).not.toContain('secret-2001')
    expect(raw).not.toContain('"merchant_id"')
    const epay3 = providersSchema.parse(JSON.parse(raw)).providers.find((p) => p.code === 'epay3')!
    expect(epay3).toMatchObject({ enabled: true, accepting_new: false, has_credentials: true, methods: ['wxpay', 'alipay'], default_method: 'wxpay', submit_path: '/submit.php' })

    // 编辑：不收 code / adapter；凭据留空 = 不改
    expect((await put(admin, 'payment-providers/epay3', { code: 'other', ...settings, merchant_id: '', key: '' }, 'prov-3')).status).toBe(400)
    // 易支付只出支付宝和微信：勾 QQ 钱包被拒
    expect(await json(await put(admin, 'payment-providers/epay3', { ...settings, methods: ['alipay', 'qqpay'], default_method: 'qqpay', merchant_id: '', key: '' }, 'prov-4q'))).toMatchObject({
      error: { fields: { methods: '支付方式只能从支付宝、微信支付中选' } },
    })
    // 最低付款额：1–100000 分，越界 422 标在 min_amount；不是整数在解码时就 400；新建不带按默认 100
    const minOf = async (code: string) => providersSchema.parse(await json(await get(admin, 'payment-providers'))).providers.find((p) => p.code === code)!.min_amount
    for (const bad of [100_001, -1]) {
      expect(await json(await put(admin, 'payment-providers/epay3', { ...settings, min_amount: bad, merchant_id: '', key: '' }, `prov-min-${String(bad)}`))).toMatchObject({
        error: { fields: { min_amount: expect.any(String) } },
      })
    }
    for (const bad of [1.5, '100']) expect((await put(admin, 'payment-providers/epay3', { ...settings, min_amount: bad, merchant_id: '', key: '' }, `prov-min-${String(bad)}`)).status).toBe(400)
    expect(await minOf('epay3')).toBe(100)
    providerWrittenSchema.parse(await json(await put(admin, 'payment-providers/epay3', { ...settings, min_amount: 250, merchant_id: '', key: '' }, 'prov-min-ok')))
    expect(await minOf('epay3')).toBe(250)
    // 编辑时不传或传 0 = 保留原值（A 路修复），不会被重置成默认值
    providerWrittenSchema.parse(await json(await put(admin, 'payment-providers/epay3', { ...settings, merchant_id: '', key: '' }, 'prov-min-keep')))
    expect(await minOf('epay3')).toBe(250)
    providerWrittenSchema.parse(await json(await put(admin, 'payment-providers/epay3', { ...settings, min_amount: 0, merchant_id: '', key: '' }, 'prov-min-keep0')))
    expect(await minOf('epay3')).toBe(250)
    // 新建时传 0 等于没传：按默认 ¥1.00
    expect((await post(admin, 'payment-providers', { code: 'epay4', adapter: 'epay', ...settings, min_amount: 0, merchant_id: '2002', key: 'k' }, 'prov-min-new')).status).toBe(201)
    expect(await minOf('epay4')).toBe(100)
    const kept = providerWrittenSchema.parse(await json(await put(admin, 'payment-providers/epay3', { ...settings, methods: ['alipay', 'wxpay'], default_method: 'wxpay', merchant_id: '', key: '' }, 'prov-4')))
    expect(kept.credentials_changed).toBe(false)
    const rotated = providerWrittenSchema.parse(await json(await put(admin, 'payment-providers/epay3', { ...settings, merchant_id: '', key: 'secret-rotated' }, 'prov-5')))
    expect(rotated.credentials_changed).toBe(true)
    // 没有凭据的旧渠道编辑时必须补齐；offline 与演示渠道只读
    expect(await json(await put(admin, 'payment-providers/epay_backup', { ...settings, merchant_id: '', key: '' }, 'prov-6'))).toMatchObject({
      error: { fields: { merchant_id: expect.any(String), key: expect.any(String) } },
    })
    expect((await put(admin, 'payment-providers/offline', { ...settings, merchant_id: 'm', key: 'k' }, 'prov-7')).status).toBe(409)
    expect((await put(admin, 'payment-providers/demo', { ...settings, merchant_id: 'm', key: 'k' }, 'prov-8')).status).toBe(409)
    expect((await put(admin, 'payment-providers/nope', { ...settings, merchant_id: 'm', key: 'k' }, 'prov-9')).status).toBe(404)
  })

  it('refuses a pending manual order below the minimum payment, but grants and offline receipts go through (F4)', async () => {
    const standard = await planByName('标准版') // ¥25
    admin = await reauthed()
    // 站点门槛 = 启用且收新单的 CNY 渠道里最小的 min_amount：把收银台打开并调到 ¥30
    expect((await post(admin, 'payment-providers/epay/toggle', { enabled: true, accepting_new: true })).status).toBe(200)
    const epay = { display_name: '聚合收银台', base_url: 'https://pay.example.com', submit_path: '/submit.php', api_path: '/api.php', methods: ['alipay', 'wxpay'], default_method: 'alipay', allow_private_host: false, merchant_id: '', key: '' }
    providerWrittenSchema.parse(await json(await put(admin, 'payment-providers/epay', { ...epay, min_amount: 3000 }, 'min-epay-up')))
    // …01 只有一份标准版：唯一的选项是续它，不带 target
    const body = { user_id: SOLO, plan_id: standard.plan_id, price_id: standard.price_id, reason: '小额补单测试' }
    // preview 提交前就给出门槛与每个落点的应付、是否低于它（只有开单权限也拿得到，不用读渠道列表）
    const preview = manualPreviewSchema.parse(await json(await post(admin, 'orders/manual/preview', { user_id: SOLO, plan_id: standard.plan_id, price_id: standard.price_id })))
    expect(preview.min_payment).toBe(3000)
    expect(preview.options.map((o) => [o.kind, o.due, o.below_minimum])).toEqual([['renew', 2500, true]])
    const pending = await post(admin, 'orders/manual', { ...body, settlement: 'pending' }, 'min-pending')
    expect(pending.status).toBe(422)
    const failure = await json<{ error: { code: string; message: string; fields?: Record<string, string> } }>(pending)
    const msg = '应付金额低于支付渠道的最低付款额，用户无法在线支付，请改用赠送或线下已收款'
    // 带 fields.settlement（billing.errManualBelowMinimum）：后台按 fields 落到「结算方式」上，不靠文案
    expect(failure.error).toEqual({ code: 'validation_failed', message: msg, fields: { settlement: msg } })
    expect((await post(admin, 'orders/manual', { ...body, settlement: 'grant' }, 'min-grant')).status).toBe(201)
    expect((await post(admin, 'orders/manual', { ...body, settlement: 'offline', reference: 'BANK-MIN-001' }, 'min-offline')).status).toBe(201)
    // 门槛降回 ¥1.00：同一张单能开成待支付
    providerWrittenSchema.parse(await json(await put(admin, 'payment-providers/epay', { ...epay, min_amount: 100 }, 'min-epay-down')))
    const lower = manualPreviewSchema.parse(await json(await post(admin, 'orders/manual/preview', { user_id: SOLO, plan_id: standard.plan_id, price_id: standard.price_id })))
    expect([lower.min_payment, lower.options[0]!.below_minimum]).toEqual([100, false])
    expect(manualCreatedSchema.parse(await json(await post(admin, 'orders/manual', { ...body, settlement: 'pending' }, 'min-pending-ok'))).status).toBe('pending_payment')
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
