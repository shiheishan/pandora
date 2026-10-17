import type { MockModule } from '../types.ts'
import { BillingError, couponCheck, couponFace, isUuid, readStrict } from './billing.ts'
import { findPlan, findPrice, planView, plansNow } from './catalog.ts'
import { gate } from './fixtures.ts'

export const plans: MockModule = {
  anonymous: {
    'GET /v1/plans': async (ctx) => {
      if (!(await gate(ctx))) return
      const plans = plansNow()
        .filter((p) => p.prices.length > 0)
        .map(planView)
        .sort((a, b) => (b.recommended ? 1 : 0) - (a.recommended ? 1 : 0))
      ctx.send(200, { plans })
    },
  },
  routes: {
    'POST /v1/coupons/preview': async (ctx) => {
      const body = await readStrict(ctx, ['plan_id', 'price_id', 'coupon_code'])
      if (!body) return
      try {
        if (!isUuid(body.plan_id)) throw new BillingError(422, 'validation_failed', '参数不合法', { plan_id: '必填' })
        if (!isUuid(body.price_id)) throw new BillingError(422, 'validation_failed', '参数不合法', { price_id: '必填' })
        const plan = findPlan(body.plan_id)
        const price = plan && findPrice(plan, body.price_id)
        if (!price) throw new BillingError(404, 'not_found', '价格不存在或已下架')
        const discount = couponCheck(body.coupon_code, price.unit_amount, price.id)
        ctx.send(200, { subtotal: price.unit_amount, discount, payable: price.unit_amount - discount, coupon: couponFace(body.coupon_code) })
      } catch (e) {
        if (!(e instanceof BillingError)) throw e
        ctx.fail(e.status, e.code, e.message, e.fields)
      }
    },
  },
}
