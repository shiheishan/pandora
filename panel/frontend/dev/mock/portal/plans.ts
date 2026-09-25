/**
 * [INPUT]: 依赖 node:crypto 的 randomUUID，依赖 ../types 的 MockModule，依赖 ./catalog 的目录，依赖 ./fixtures 的 portalState / gate / scenario，依赖 ./billing 的校验、优惠码与下单
 * [OUTPUT]: 对外提供 plans 模块的假接口 MockModule
 * [POS]: dev/mock/portal 的「选购套餐（门户-03）」假接口，归门户前端；形状照 api-contract.md（含修订 R30、R69）：套餐目录与流量包目录（匿名）、我的流量包余量、优惠码试算（套餐 + 价格或 pack_id 两种形态）、购买流量包（幂等 scope order_create）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomUUID } from 'node:crypto'
import type { MockModule } from '../types.ts'
import { BillingError, couponCheck, couponView, createdView, isUuid, placeOrder, readStrict } from './billing.ts'
import { findPack, findPlan, findPrice, GIB, PACKS, PLANS, planView } from './catalog.ts'
import { gate, portalState, scenario } from './fixtures.ts'

export const plans: MockModule = {
  anonymous: {
    'GET /v1/plans': async (ctx) => {
      if (!(await gate(ctx))) return
      ctx.send(200, { plans: PLANS.map((p) => planView(p, scenario() === 'legacy')) })
    },
    'GET /v1/traffic-packs': async (ctx) => {
      if (!(await gate(ctx))) return
      ctx.send(200, { packs: PACKS })
    },
  },
  routes: {
    'GET /v1/me/traffic-packs': async (ctx) => {
      if (!(await gate(ctx))) return
      const remaining = portalState(ctx.user.userId).packBytes
      const packs =
        remaining > 0
          ? [{ id: randomUUID(), source: 'gift_card', order_id: null, granted_bytes: Math.max(remaining, 50 * GIB), consumed_bytes: Math.max(0, 50 * GIB - remaining), remaining_bytes: remaining, created_at: new Date(Date.now() - 20 * 86_400_000).toISOString() }]
          : []
      ctx.send(200, { remaining_bytes_total: remaining, packs })
    },

    // 不落库；pack_id 与 plan_id / price_id 互斥（修订 R69）
    'POST /v1/coupons/preview': async (ctx) => {
      const body = await readStrict(ctx, ['plan_id', 'price_id', 'pack_id', 'coupon_code'])
      if (!body) return
      try {
        if (body.pack_id !== undefined) {
          if (body.plan_id !== undefined || body.price_id !== undefined) throw new BillingError(422, 'validation_failed', '参数不合法', { pack_id: '不能与 plan_id / price_id 同时传' })
          const pack = findPack(body.pack_id)
          if (!pack) throw new BillingError(404, 'not_found', '流量包不存在')
          const discount = couponCheck(body.coupon_code, pack.unit_amount, null)
          return ctx.send(200, { subtotal: pack.unit_amount, discount, payable: pack.unit_amount - discount, currency: 'CNY', coupon: couponView(body.coupon_code) })
        }
        if (!isUuid(body.plan_id)) throw new BillingError(422, 'validation_failed', '参数不合法', { plan_id: '必填' })
        if (!isUuid(body.price_id)) throw new BillingError(422, 'validation_failed', '参数不合法', { price_id: '必填' })
        const plan = findPlan(body.plan_id)
        const price = plan && findPrice(plan, body.price_id)
        if (!price) throw new BillingError(404, 'not_found', '价格不存在或已下架')
        const discount = couponCheck(body.coupon_code, price.unit_amount, price.id)
        ctx.send(200, { subtotal: price.unit_amount, discount, payable: price.unit_amount - discount, currency: price.currency, coupon: couponView(body.coupon_code) })
      } catch (e) {
        if (!(e instanceof BillingError)) throw e
        ctx.fail(e.status, e.code, e.message, e.fields)
      }
    },

    // 修订 R30：挂用户、没有订阅也能买；与新购共用幂等 scope order_create
    'POST /v1/me/traffic-pack-orders': async (ctx) => {
      const body = await readStrict(ctx, ['pack_id', 'use_balance', 'coupon_code'])
      if (!body) return
      await ctx.idempotent('order_create', () => {
        try {
          const pack = findPack(body.pack_id)
          if (!pack) throw new BillingError(404, 'not_found', '流量包不存在或已下架')
          const order = placeOrder(portalState(ctx.user.userId), {
            kind: 'addon',
            subtotal: pack.unit_amount,
            coupon: body.coupon_code,
            priceId: null,
            useBalance: body.use_balance,
            planName: pack.name,
            itemName: pack.name,
            effect: { type: 'addon', bytes: pack.traffic_bytes },
          })
          return { status: 201, body: createdView(order) }
        } catch (e) {
          if (e instanceof BillingError) return e.result()
          throw e
        }
      })
    },
  },
}
