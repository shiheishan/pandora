/**
 * [INPUT]: 依赖 node:crypto 的 randomUUID，依赖 ../types 的 MockModule / MockContext，依赖 ./catalog 的目录与支付方式，依赖 ./fixtures 的 portalState / gate，依赖 ./billing 的校验、下单、履约与变更折算
 * [OUTPUT]: 对外提供 checkout 模块的假接口 MockModule
 * [POS]: dev/mock/portal 的「确认订单（门户-03 结账）」假接口，归门户前端；形状、错误码与幂等照 api-contract.md（含修订 R8、R35–R37、R61）：支付方式、新购（order_create）、续费（subscription_renewal_create，门户-02 条目但由结账页调用）、变更套餐试算与下单（subscription_change_plan_create）、发起支付；另挂 dev 专用的假收银台（匿名）：GET v1/__mock/cashier 出一页 HTML，「模拟支付成功」履约后 302 回 return_url
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomUUID } from 'node:crypto'
import type { MockContext, MockModule, MockResult } from '../types.ts'
import { assertNoOpenChange, BillingError, changeQuote, createdView, fulfill, isUuid, placeOrder, readStrict, sweepExpired } from './billing.ts'
import { findPlan, findPrice, PAY_METHODS } from './catalog.ts'
import { gate, portalState } from './fixtures.ts'

const LIVE = new Set(['active', 'trialing', 'grace', 'past_due'])

/** 业务拒绝写成可重放的结果（幂等表原样重放 4xx） */
function guard(run: () => MockResult): MockResult {
  try {
    return run()
  } catch (e) {
    if (e instanceof BillingError) return e.result()
    throw e
  }
}

function ownedSub(ctx: MockContext) {
  const sub = portalState(ctx.user.userId).subs.find((s) => s.id === ctx.params.id)
  if (!sub) throw new BillingError(404, 'not_found', '订阅不存在')
  return sub
}

function planAndPrice(planId: unknown, priceId: unknown) {
  if (!isUuid(planId)) throw new BillingError(422, 'validation_failed', '参数不合法', { plan_id: '必填' })
  if (!isUuid(priceId)) throw new BillingError(422, 'validation_failed', '参数不合法', { price_id: '必填' })
  const plan = findPlan(planId)
  if (!plan) throw new BillingError(404, 'not_found', '套餐不存在')
  const price = findPrice(plan, priceId)
  if (!price) throw new BillingError(409, 'conflict', '该价格已下架')
  return { plan, price }
}

// 收银台意图：intent → 订单与回跳地址；只在内存里
const intents = new Map<string, { userId: string; orderId: string; returnUrl: string; provider: string; method: string }>()

export const checkout: MockModule = {
  anonymous: {
    'GET /v1/__mock/cashier': (ctx) => {
      const intent = intents.get(ctx.query.get('intent') ?? '')
      if (!intent) return ctx.sendRaw(404, { contentType: 'text/html; charset=utf-8', text: '<p>收银台链接已失效</p>' })
      const order = portalState(intent.userId).orders.find((o) => o.id === intent.orderId)
      const done = `v1/__mock/cashier/complete?intent=${encodeURIComponent(ctx.query.get('intent')!)}`
      ctx.sendRaw(200, {
        contentType: 'text/html; charset=utf-8',
        text: `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>假收银台</title>
<body style="font-family:sans-serif;max-width:360px;margin:48px auto;padding:0 16px;line-height:1.8">
<h3>假收银台（仅开发环境）</h3><p>订单 ${order?.order_no ?? '?'} · ¥${((order?.payable_amount ?? 0) / 100).toFixed(2)} · ${intent.method}</p>
<p><a href="/${done}">模拟支付成功</a></p><p><a href="${intent.returnUrl}">取消并返回商户</a></p></body>`,
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
  },
  routes: {
    // 修订 R61：payment_providers 里 enabled 且 accepting_new 的渠道展开
    'GET /v1/payment-methods': async (ctx) => {
      if (!(await gate(ctx))) return
      ctx.send(200, { methods: PAY_METHODS.map(({ provider, method, label, currencies }) => ({ provider, method, label, currencies })) })
    },

    'POST /v1/orders': async (ctx) => {
      const body = await readStrict(ctx, ['plan_id', 'price_id', 'use_balance', 'coupon_code'])
      if (!body) return
      await ctx.idempotent('order_create', () =>
        guard(() => {
          const { plan, price } = planAndPrice(body.plan_id, body.price_id)
          const order = placeOrder(portalState(ctx.user.userId), {
            kind: 'new',
            subtotal: price.unit_amount,
            coupon: body.coupon_code,
            priceId: price.id,
            useBalance: body.use_balance,
            planName: plan.name,
            itemName: plan.name,
            price,
            effect: { type: 'new', planId: plan.id, priceId: price.id },
          })
          return { status: 201, body: createdView(order) }
        }),
      )
    },

    // 门户-02 条目；price_id 不传沿用订阅当前价格
    'POST /v1/me/subscriptions/:id/renew': async (ctx) => {
      const body = await readStrict(ctx, ['price_id', 'use_balance', 'coupon_code'])
      if (!body) return
      await ctx.idempotent('subscription_renewal_create', () =>
        guard(() => {
          const state = portalState(ctx.user.userId)
          const sub = ownedSub(ctx)
          if (!LIVE.has(sub.status)) throw new BillingError(409, 'conflict', '这条订阅当前不能续费')
          const plan = findPlan(sub.plan_id)
          if (!plan?.allow_renewal) throw new BillingError(409, 'conflict', '该套餐当前不允许续费')
          const price = findPrice(plan, body.price_id ?? sub.price_id)
          if (!price) throw new BillingError(409, 'conflict', '所选价格已下架，请重新选择')
          assertNoOpenChange(state, sub.id)
          const order = placeOrder(state, {
            kind: 'renewal',
            subtotal: price.unit_amount,
            coupon: body.coupon_code,
            priceId: price.id,
            useBalance: body.use_balance,
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

    // 修订 R35：不落库，不幂等
    'POST /v1/me/subscriptions/:id/change-plan/preview': async (ctx) => {
      const body = await readStrict(ctx, ['plan_id', 'price_id', 'coupon_code'])
      if (!body) return
      const result = guard(() => {
        const sub = ownedSub(ctx)
        const { plan, price } = planAndPrice(body.plan_id, body.price_id)
        assertNoOpenChange(portalState(ctx.user.userId), sub.id)
        return { status: 200, body: changeQuote(sub, plan, price, body.coupon_code) }
      })
      ctx.send(result.status, result.body)
    },

    // 修订 R36：升降级 kind 都是 upgrade，0 元当场履约并退余额
    'POST /v1/me/subscriptions/:id/change-plan': async (ctx) => {
      const body = await readStrict(ctx, ['plan_id', 'price_id', 'use_balance', 'coupon_code'])
      if (!body) return
      await ctx.idempotent('subscription_change_plan_create', () =>
        guard(() => {
          const state = portalState(ctx.user.userId)
          const sub = ownedSub(ctx)
          const { plan, price } = planAndPrice(body.plan_id, body.price_id)
          assertNoOpenChange(state, sub.id)
          const quote = changeQuote(sub, plan, price, body.coupon_code)
          const order = placeOrder(state, {
            kind: 'upgrade',
            subtotal: price.unit_amount,
            credit: quote.proration_credit,
            coupon: body.coupon_code,
            priceId: price.id,
            useBalance: body.use_balance,
            planName: plan.name,
            itemName: plan.name,
            price,
            subscriptionId: sub.id,
            effect: { type: 'upgrade', subId: sub.id, planId: plan.id, priceId: price.id, credit: quote.proration_credit, refund: quote.balance_refund },
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
      sweepExpired(state)
      const order = state.orders.find((o) => o.id === ctx.params.id)
      if (typeof body.provider !== 'string' || body.provider === '') return ctx.fail(422, 'validation_failed', '参数不合法', { provider: '必填' })
      if (!order) return ctx.fail(404, 'not_found', '订单不存在')
      const channel = PAY_METHODS.find((m) => m.provider === body.provider && (body.method === undefined || m.method === body.method))
      if (!channel) return ctx.fail(404, 'not_found', '未知的支付渠道')
      if (order.status !== 'pending_payment') return ctx.fail(409, 'conflict', '该订单当前状态不可支付')
      if (order.payable_amount <= 0) return ctx.fail(409, 'conflict', '该订单无需外部支付')
      const origin = `http://${ctx.req.headers.host}`
      const returnUrl = typeof body.return_url === 'string' && body.return_url.startsWith(`${origin}/`) ? body.return_url : `${origin}/#orders`
      const existing = [...intents.entries()].find(([, v]) => v.orderId === order.id && v.provider === channel.provider && v.method === channel.method)
      const intentId = existing?.[0] ?? randomUUID()
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
