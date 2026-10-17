import type { MockModule } from '../types.ts'
import { assertNoPendingReset, BillingError, createdView, isUuid, placeOrder, readStrict } from './billing.ts'
import { findPlan } from './catalog.ts'
import { isLiveSub, portalState } from './fixtures.ts'
import { quoteRows, quoteTime, settle } from './quote.ts'

export const reset: MockModule = {
  routes: {
    'POST /v1/me/subscriptions/:id/traffic-reset': async (ctx) => {
      const body = await readStrict(ctx, ['use_balance', 'coupon_code', 'as_of', 'expect'])
      if (!body) return
      await ctx.idempotent('order_create', () => {
        try {
          const state = portalState(ctx.user.userId)
          const subId = ctx.params.id
          if (!isUuid(subId)) throw new BillingError(404, 'not_found', '订阅不存在')
          const sub = state.subs.find((s) => s.id === subId)
          if (!sub) throw new BillingError(404, 'not_found', '订阅不存在')
          if (!isLiveSub(sub)) throw new BillingError(422, 'validation_failed', '这一份不在生效中，不能重置流量')
          const plan = findPlan(sub.plan_id)
          if (!plan || plan.reset === null || sub.limitBytes === null || sub.limitBytes <= 0) {
            throw new BillingError(409, 'conflict', '这个套餐不提供流量重置')
          }
          if (plan.reset.limit !== null && sub.resetsUsed >= plan.reset.limit) {
            throw new BillingError(409, 'conflict', '本期的流量重置次数已用完')
          }
          assertNoPendingReset(state, sub.id)
          const planName = plan.name ?? sub.plan_name
          const rows = quoteRows(state, { action: 'reset', subscription_id: sub.id, coupon_code: body.coupon_code }, quoteTime(body), true)
          const { row, balance } = settle(state, rows, null, body)
          const order = placeOrder(state, {
            kind: 'traffic_reset',
            subtotal: row.subtotal,
            discount: 0,
            total: row.total,
            applied: balance.applied,
            waived: balance.waived,
            planName,
            itemName: `流量重置 · ${planName}`,
            subscriptionId: sub.id,
            effect: { type: 'reset', subId: sub.id },
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
