import { randomUUID } from 'node:crypto'
import type { MockModule } from '../types.ts'
import { BillingError, couponCheck, couponFace, createdView, isUuid, placeOrder, readStrict } from './billing.ts'
import { findPack, findPlan, findPrice, packsNow, planView, plansNow } from './catalog.ts'
import { gate, isDead, isLiveSub, isRevivable, portalState } from './fixtures.ts'
import { quoteRows, quoteTime, settle } from './quote.ts'

const DAY_MS = 86_400_000

export const plans: MockModule = {
  anonymous: {
    'GET /v1/plans': async (ctx) => {
      if (!(await gate(ctx))) return
      ctx.send(200, { plans: plansNow().map(planView) })
    },
    'GET /v1/traffic-packs': async (ctx) => {
      if (!(await gate(ctx))) return
      ctx.send(200, { packs: packsNow() })
    },
  },
  routes: {
    // 每笔余额带上它挂在哪一份（subscription_id，未分配为 null）；假后端每份合成一笔
    'GET /v1/me/traffic-packs': async (ctx) => {
      if (!(await gate(ctx))) return
      const state = portalState(ctx.user.userId)
      const grant = (sub: string | null, remaining: number) => ({
        id: randomUUID(),
        subscription_id: sub,
        source: 'order',
        order_id: null,
        granted_bytes: remaining,
        consumed_bytes: 0,
        remaining_bytes: remaining,
        created_at: new Date(Date.now() - 20 * DAY_MS).toISOString(),
      })
      const packs = [...state.subs.filter((s) => s.packBytes > 0).map((s) => grant(s.id, s.packBytes)), ...(state.unattachedBytes > 0 ? [grant(null, state.unattachedBytes)] : [])]
      ctx.send(200, { remaining_bytes_total: packs.reduce((n, p) => n + p.remaining_bytes, 0), packs })
    },

    // 设计稿 2.7：来源只能是未分配（null）或彻底停用的那份，目标须生效中或可救回；不幂等，重复调用 moved_bytes=0
    'POST /v1/me/traffic-packs/transfer': async (ctx) => {
      const body = await readStrict(ctx, ['from_subscription_id', 'to_subscription_id'])
      if (!body) return
      const state = portalState(ctx.user.userId)
      // 与 billing.TransferTrafficPacks 入口同一句：来源与目标是同一份，先于一切状态判断回 422
      if (body.from_subscription_id === body.to_subscription_id) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { to_subscription_id: '要转到另一份上' })
      const to = state.subs.find((s) => s.id === body.to_subscription_id)
      if (!to) return ctx.fail(404, 'not_found', '订阅不存在')
      if (!isLiveSub(to) && !isRevivable(to)) return ctx.fail(409, 'conflict', '只能转到在用的套餐上')
      let moved: number
      if (body.from_subscription_id === null || body.from_subscription_id === undefined) {
        moved = state.unattachedBytes
        state.unattachedBytes = 0
      } else {
        const from = state.subs.find((s) => s.id === body.from_subscription_id)
        if (!from) return ctx.fail(404, 'not_found', '订阅不存在')
        // 与 billing.errTransferSource 同一句：来源还在用（生效中或可救回）一律 409
        if (!isDead(from)) return ctx.fail(409, 'conflict', '这份还在用，流量包不能转走；等它停用后再转')
        moved = from.packBytes
        from.packBytes = 0
      }
      to.packBytes += moved
      if (moved > 0) state.transfers.push({ from: (body.from_subscription_id as string | null | undefined) ?? null, to: to.id, bytes: moved, at: new Date().toISOString() })
      ctx.send(200, { moved_bytes: moved })
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
          return ctx.send(200, { subtotal: pack.unit_amount, discount, payable: pack.unit_amount - discount, currency: 'CNY', coupon: couponFace(body.coupon_code) })
        }
        if (!isUuid(body.plan_id)) throw new BillingError(422, 'validation_failed', '参数不合法', { plan_id: '必填' })
        if (!isUuid(body.price_id)) throw new BillingError(422, 'validation_failed', '参数不合法', { price_id: '必填' })
        const plan = findPlan(body.plan_id)
        const price = plan && findPrice(plan, body.price_id)
        if (!price) throw new BillingError(404, 'not_found', '价格不存在或已下架')
        const discount = couponCheck(body.coupon_code, price.unit_amount, price.id)
        ctx.send(200, { subtotal: price.unit_amount, discount, payable: price.unit_amount - discount, currency: price.currency, coupon: couponFace(body.coupon_code) })
      } catch (e) {
        if (!(e instanceof BillingError)) throw e
        ctx.fail(e.status, e.code, e.message, e.fields)
      }
    },

    // 设计稿 2.4：必须带 subscription_id，且那份生效中、属于本人；与新购共用幂等 scope order_create
    'POST /v1/me/traffic-pack-orders': async (ctx) => {
      const body = await readStrict(ctx, ['pack_id', 'subscription_id', 'use_balance', 'coupon_code', 'as_of', 'expect'])
      if (!body) return
      await ctx.idempotent('order_create', () => {
        try {
          const state = portalState(ctx.user.userId)
          const pack = findPack(body.pack_id)
          if (!pack) throw new BillingError(404, 'not_found', '流量包不存在或已下架')
          if (!isUuid(body.subscription_id)) throw new BillingError(422, 'validation_failed', '参数不合法', { subscription_id: '必填' })
          const sub = state.subs.find((s) => s.id === body.subscription_id)
          if (!sub) throw new BillingError(404, 'not_found', '订阅不存在')
          if (!isLiveSub(sub)) throw new BillingError(409, 'conflict', '流量包要加到一份在用的套餐上，先续费或买个套餐')
          const rows = quoteRows(state, { action: 'pack', pack_id: pack.id, subscription_id: sub.id, coupon_code: body.coupon_code }, quoteTime(body), true)
          const { row, balance } = settle(state, rows, null, body)
          const order = placeOrder(state, {
            kind: 'addon',
            subtotal: row.subtotal,
            discount: row.discount,
            total: row.total,
            applied: balance.applied,
            waived: balance.waived,
            couponCode: row.coupon?.code,
            planName: pack.name,
            itemName: pack.name,
            subscriptionId: sub.id,
            effect: { type: 'addon', bytes: pack.traffic_bytes, subId: sub.id },
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
