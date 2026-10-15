import type { MockModule } from '../types.ts'
import { BillingError, createdView, isUuid, placeOrder, readStrict } from './billing.ts'
import { findPlan } from './catalog.ts'
import { isLiveSub, portalState, type PortalState } from './fixtures.ts'
import { quoteRows, quoteTime, settle } from './quote.ts'

/** 目录里前两个套餐（标准版、专业版）各配一个 CNY 重置价（分） */
export const RESET_PRICES: Record<string, number> = {
  '6f1c2a10-0000-4000-8000-000000000001': 1500,
  '6f1c2a10-0000-4000-8000-000000000002': 2500,
}

const memberPercents = new WeakMap<PortalState, number>()

export function memberPercentOf(state: PortalState): number {
  return memberPercents.get(state) ?? 0
}

export function setMemberPercent(state: PortalState, pct: number): void {
  memberPercents.set(state, pct)
}

export function assertNoPendingReset(state: PortalState, subId: string) {
  const open = state.orders.find((o) => o.kind === 'traffic_reset' && o.status === 'pending_payment' && o.subscription_id === subId)
  if (!open) return
  const lapsed = open.expires_at !== undefined && new Date(open.expires_at).getTime() <= Date.now()
  const msg = lapsed ? '你有一张已超过付款期限的流量重置订单，取消后再买' : '你有一张还没付款的流量重置订单，继续付款或取消后再买'
  throw new BillingError(409, 'order_pending', msg, lapsed ? { order_id: open.id, lapsed: 'true' } : { order_id: open.id })
}

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
          if (!(sub.plan_id in RESET_PRICES) || !sub.limitBytes || sub.limitBytes <= 0) {
            throw new BillingError(409, 'conflict', '这个套餐不提供流量重置')
          }
          assertNoPendingReset(state, sub.id)
          const plan = findPlan(sub.plan_id)
          const planName = plan?.name ?? sub.plan_name
          const rows = quoteRows(state, { action: 'reset', subscription_id: sub.id, coupon_code: body.coupon_code }, quoteTime(body), true)
          const { row, balance } = settle(state, rows, null, body)
          // 重置单永远按原价：不收券、不打会员折扣
          const order = placeOrder(state, {
            kind: 'traffic_reset',
            subtotal: row.subtotal,
            discount: 0,
            memberDiscount: 0,
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
