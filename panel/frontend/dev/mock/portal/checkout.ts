import { randomUUID } from 'node:crypto'
import type { MockModule, MockResult } from '../types.ts'
import { assertNoOpenChange, BillingError, createdView, fulfill, isUuid, placeOrder, readStrict, sweepExpired } from './billing.ts'
import { findPlan, MIN_PAYMENT, PAY_METHODS } from './catalog.ts'
import { gate, isLiveSub, isRevivable, portalState } from './fixtures.ts'
import { normalizeLabel, profileName } from './purchase.ts'
import { assertNoPendingNew, quoteRows, quoteTime, settle } from './quote.ts'

/** 业务拒绝写成可重放的结果（幂等表原样重放 4xx） */
function guard(run: () => MockResult): MockResult {
  try {
    return run()
  } catch (e) {
    if (e instanceof BillingError) return e.result()
    throw e
  }
}

// 收银台意图：intent → 订单与回跳地址；只在内存里。channelPaid 是「渠道收到了钱、回调却丢了」：
// 假收银台的「模拟支付成功但回调丢失」只置它不履约，留给「我已支付，刷新状态」去查单补记
interface MockIntent {
  userId: string
  orderId: string
  returnUrl: string
  provider: string
  method: string
  channelPaid?: boolean
}
const intents = new Map<string, MockIntent>()

/** 订单最近发起的支付意图（POST v1/orders/{id}/query 的假渠道用），没有发起过支付时为 undefined */
export function channelIntent(userId: string, orderId: string): MockIntent | undefined {
  let last: MockIntent | undefined
  for (const it of intents.values()) if (it.userId === userId && it.orderId === orderId) last = it
  return last
}

export const checkout: MockModule = {
  anonymous: {
    'GET /v1/__mock/cashier': (ctx) => {
      const intent = intents.get(ctx.query.get('intent') ?? '')
      if (!intent) return ctx.sendRaw(404, { contentType: 'text/html; charset=utf-8', text: '<p>收银台链接已失效</p>' })
      const order = portalState(intent.userId).orders.find((o) => o.id === intent.orderId)
      const done = `v1/__mock/cashier/complete?intent=${encodeURIComponent(ctx.query.get('intent')!)}`
      const lost = `v1/__mock/cashier/lost-callback?intent=${encodeURIComponent(ctx.query.get('intent')!)}`
      ctx.sendRaw(200, {
        contentType: 'text/html; charset=utf-8',
        text: `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>假收银台</title>
<body style="font-family:sans-serif;max-width:360px;margin:48px auto;padding:0 16px;line-height:1.8">
<h3>假收银台（仅开发环境）</h3><p>订单 ${order?.order_no ?? '?'} · ¥${((order?.payable_amount ?? 0) / 100).toFixed(2)} · ${intent.method}</p>
<p><a href="/${done}">模拟支付成功</a></p><p><a href="/${lost}">模拟支付成功但回调丢失</a></p><p><a href="${intent.returnUrl}">取消并返回商户</a></p></body>`,
      })
    },
    'GET /v1/__mock/cashier/complete': (ctx) => {
      const intent = intents.get(ctx.query.get('intent') ?? '')
      if (!intent) return ctx.sendRaw(404, { contentType: 'text/html; charset=utf-8', text: '<p>收银台链接已失效</p>' })
      const state = portalState(intent.userId)
      const order = state.orders.find((o) => o.id === intent.orderId)
      if (order && order.status === 'pending_payment') fulfill(state, order, { provider: intent.provider, method: intent.method })
      ctx.res.statusCode = 302
      ctx.res.setHeader('Location', intent.returnUrl)
      ctx.res.end()
    },
    'GET /v1/__mock/cashier/lost-callback': (ctx) => {
      const intent = intents.get(ctx.query.get('intent') ?? '')
      if (!intent) return ctx.sendRaw(404, { contentType: 'text/html; charset=utf-8', text: '<p>收银台链接已失效</p>' })
      intent.channelPaid = true
      ctx.res.statusCode = 302
      ctx.res.setHeader('Location', intent.returnUrl)
      ctx.res.end()
    },
  },
  routes: {
    // 修订 R61：payment_providers 里 enabled 且 accepting_new 的渠道展开
    'GET /v1/payment-methods': async (ctx) => {
      if (!(await gate(ctx))) return
      ctx.send(200, { methods: PAY_METHODS.map(({ provider, method, label, currencies }) => ({ provider, method, label, currencies })) })
    },

    // 新购（门户「另买一份」带 new_copy 与 label）；带 as_of + expect 时按报价比对（设计稿 2.2）
    'POST /v1/orders': async (ctx) => {
      const body = await readStrict(ctx, ['plan_id', 'price_id', 'use_balance', 'coupon_code', 'new_copy', 'label', 'as_of', 'expect'])
      if (!body) return
      await ctx.idempotent('order_create', () =>
        guard(() => {
          const state = portalState(ctx.user.userId)
          if (!isUuid(body.price_id)) throw new BillingError(422, 'validation_failed', '参数不合法', { price_id: '必填' })
          const plan = findPlan(body.plan_id)
          if (!plan) throw new BillingError(404, 'not_found', '套餐不存在')
          const newCopy = body.new_copy === true
          const label = labelOf(body.label)
          // RejectSamePlan 只在 !new_copy 时生效
          if (!newCopy && state.subs.some((s) => s.plan_id === plan.id && (isLiveSub(s) || isRevivable(s)))) {
            throw new BillingError(409, 'conflict', '你已有这个套餐的订阅，请在原订阅上续费，订阅链接不变')
          }
          // 会和已有一份在 App 里重名（已有那份没起名、同套餐）又没起名：必填
          if (newCopy && !label && state.subs.some((s) => s.plan_id === plan.id && !s.label && (isLiveSub(s) || isRevivable(s)))) {
            throw new BillingError(422, 'validation_failed', '参数不合法', { label: `不起名的话，App 里会有两个「${profileName('Pandora', null, plan.name)}」，分不清哪个是哪个` })
          }
          assertNoPendingNew(state, plan.id, plan.name)
          const { row, balance } = settle(state, quoteRows(state, { action: 'new', plan_id: plan.id, coupon_code: body.coupon_code }, quoteTime(body), true), body.price_id, body)
          const price = plan.prices.find((p) => p.id === row.price_id)!
          const order = placeOrder(state, {
            kind: 'new',
            subtotal: row.subtotal,
            discount: row.discount,
            total: row.total,
            applied: balance.applied,
            waived: balance.waived,
            couponCode: row.coupon?.code,
            planName: plan.name,
            itemName: plan.name,
            price,
            effect: { type: 'new', planId: plan.id, priceId: price.id, label },
          })
          return { status: 201, body: createdView(order) }
        }),
      )
    },

    // 门户-02 条目；price_id 不传沿用订阅当前价格；过期 30 天内的从付款时起算（恢复使用）
    'POST /v1/me/subscriptions/:id/renew': async (ctx) => {
      const body = await readStrict(ctx, ['price_id', 'use_balance', 'coupon_code', 'as_of', 'expect'])
      if (!body) return
      await ctx.idempotent('subscription_renewal_create', () =>
        guard(() => {
          const state = portalState(ctx.user.userId)
          const sub = state.subs.find((s) => s.id === ctx.params.id)
          if (!sub) throw new BillingError(404, 'not_found', '订阅不存在')
          assertNoOpenChange(state, sub.id)
          const rows = quoteRows(state, { action: 'renew', subscription_id: sub.id, coupon_code: body.coupon_code }, quoteTime(body), true)
          const { row, balance } = settle(state, rows, (body.price_id as string | undefined) ?? sub.price_id, body)
          const plan = findPlan(sub.plan_id)!
          const price = plan.prices.find((p) => p.id === row.price_id)!
          const order = placeOrder(state, {
            kind: 'renewal',
            subtotal: row.subtotal,
            discount: row.discount,
            total: row.total,
            applied: balance.applied,
            waived: balance.waived,
            couponCode: row.coupon?.code,
            planName: plan.name,
            itemName: plan.name,
            price,
            subscriptionId: sub.id,
            effect: { type: 'renewal', subId: sub.id, priceId: price.id },
          })
          return { status: 201, body: createdView(order) }
        }),
      )
    },

    // 修订 R36：升降级 kind 都是 upgrade，0 元当场履约并退余额；过期 30 天内的那份是「恢复并改成 X」
    'POST /v1/me/subscriptions/:id/change-plan': async (ctx) => {
      const body = await readStrict(ctx, ['plan_id', 'price_id', 'use_balance', 'coupon_code', 'as_of', 'expect'])
      if (!body) return
      await ctx.idempotent('subscription_change_plan_create', () =>
        guard(() => {
          const state = portalState(ctx.user.userId)
          const sub = state.subs.find((s) => s.id === ctx.params.id)
          if (!sub) throw new BillingError(404, 'not_found', '订阅不存在')
          if (!isUuid(body.plan_id)) throw new BillingError(422, 'validation_failed', '参数不合法', { plan_id: '必填' })
          if (!isUuid(body.price_id)) throw new BillingError(422, 'validation_failed', '参数不合法', { price_id: '必填' })
          assertNoOpenChange(state, sub.id)
          const rows = quoteRows(state, { action: 'change', subscription_id: sub.id, plan_id: body.plan_id, coupon_code: body.coupon_code }, quoteTime(body), true)
          const { row, balance } = settle(state, rows, body.price_id, body)
          const plan = findPlan(body.plan_id)!
          const price = plan.prices.find((p) => p.id === row.price_id)!
          const order = placeOrder(state, {
            kind: 'upgrade',
            subtotal: row.subtotal,
            discount: row.discount,
            credit: row.credit,
            total: row.total,
            applied: balance.applied,
            waived: balance.waived,
            couponCode: row.coupon?.code,
            planName: plan.name,
            itemName: plan.name,
            price,
            subscriptionId: sub.id,
            effect: { type: 'upgrade', subId: sub.id, planId: plan.id, priceId: price.id, credit: row.credit, refund: row.refund },
          })
          return { status: 201, body: createdView(order) }
        }),
      )
    },

    // 不幂等：同渠道重复点击复用在途意图（reused=true）
    'POST /v1/orders/:id/pay': async (ctx) => {
      const body = await readStrict(ctx, ['provider', 'method', 'return_url'])
      if (!body) return
      const state = portalState(ctx.user.userId)
      const order = state.orders.find((o) => o.id === ctx.params.id)
      if (typeof body.provider !== 'string' || body.provider === '') return ctx.fail(422, 'validation_failed', '参数不合法', { provider: '必填' })
      if (!order) return ctx.fail(404, 'not_found', '订单不存在')
      // 过了付款期限、过期扫描还没关它（billing.ErrOrderPaymentExpired，409 order_lapsed）
      if (order.status === 'pending_payment' && order.expires_at && new Date(order.expires_at).getTime() <= Date.now()) return ctx.fail(409, 'order_lapsed', '这张订单已超过付款期限，请取消后重新下单')
      sweepExpired(state)
      const channel = PAY_METHODS.find((m) => m.provider === body.provider && (body.method === undefined || m.method === body.method))
      if (!channel) return ctx.fail(404, 'not_found', '未知的支付渠道')
      if (order.status !== 'pending_payment') return ctx.fail(409, 'conflict', '该订单当前状态不可支付')
      if (order.payable_amount <= 0) return ctx.fail(409, 'conflict', '该订单无需外部支付')
      // 兜底（设计稿 2.6）：低于渠道最低额不发起，渠道自己的报错是英文或干脆没有
      if (order.payable_amount < MIN_PAYMENT) return ctx.fail(409, 'conflict', '支付金额低于该付款方式的最低额')
      const origin = `http://${ctx.req.headers.host}`
      const returnUrl = typeof body.return_url === 'string' && body.return_url.startsWith(`${origin}/`) ? body.return_url : `${origin}/#orders`
      const existing = [...intents.entries()].find(([, v]) => v.orderId === order.id && v.provider === channel.provider && v.method === channel.method)
      const intentId = existing?.[0] ?? randomUUID()
      order.hasIntent = true
      intents.set(intentId, { userId: ctx.user.userId, orderId: order.id, returnUrl, provider: channel.provider, method: channel.method })
      ctx.send(201, {
        intent_id: intentId,
        http_method: channel.httpMethod,
        redirect_url: `${origin}/v1/__mock/cashier?intent=${intentId}`,
        form_fields: channel.httpMethod === 'POST' ? { out_trade_no: order.order_no } : {},
        amount: order.payable_amount,
        currency: 'CNY',
        reused: existing !== undefined,
      })
    },
  },
}

/** 备注名经 NormalizeLabel；不合规回 422 fields.label */
function labelOf(raw: unknown): string | null {
  if (raw === undefined || raw === null) return null
  if (typeof raw !== 'string') throw new BillingError(422, 'validation_failed', '参数不合法', { label: '须为字符串' })
  const n = normalizeLabel(raw)
  if ('error' in n) throw new BillingError(422, 'validation_failed', '参数不合法', { label: n.error })
  return n.ok || null
}


