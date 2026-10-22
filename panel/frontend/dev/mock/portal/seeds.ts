import { randomUUID } from 'node:crypto'
import { GIB, PLANS } from './catalog.ts'
import type { LedgerEntry, OrderFixture, RedemptionFixture, SubFixture } from './fixtures.ts'

const DAY_MS = 86_400_000

let seq = 3000

function order(now: number, daysAgo: number, init: Partial<OrderFixture> & Pick<OrderFixture, 'kind' | 'status' | 'subtotal'>): OrderFixture {
  const created = new Date(now - daysAgo * DAY_MS).toISOString()
  const discount = init.discount_amount ?? 0
  const total = init.subtotal - discount
  const balanceApplied = init.balance_applied ?? 0
  const paid = ['paid', 'fulfilled'].includes(init.status)
  return {
    id: randomUUID(),
    order_no: `PD-2${String(++seq).padStart(3, '0')}`,
    total_amount: total,
    discount_amount: discount,
    balance_applied: balanceApplied,
    payable_amount: total - balanceApplied,
    paid_amount: paid ? total - balanceApplied : 0,
    created_at: created,
    ...(paid ? { paid_at: created, payProvider: 'epay', payMethod: 'alipay' } : {}),
    effect: { type: 'none' },
    ...init,
  }
}

/** 近四个月的订单：12 条已结束 + 1 条已发起支付的待支付单 */
export function pastOrders(now: number, sub: SubFixture): OrderFixture[] {
  const [std, pro] = [PLANS[0]!, PLANS[1]!]
  const traffic = PLANS[3]!
  const trafficPrice = traffic.prices[0]!
  const month = pro.prices[0]!
  const renew = { kind: 'renewal' as const, plan_name: pro.name, item_name: pro.name, interval: month.billing_interval, interval_count: 1, subscription_id: sub.id, subtotal: month.unit_amount }
  return [
    order(now, 0.02, { kind: 'topup', status: 'pending_payment', subtotal: 5000, expires_at: new Date(now + 25 * 60_000).toISOString(), hasIntent: true }),
    order(now, 3, { ...renew, status: 'fulfilled', coupon_code: 'AUTUMN26', discount_amount: 1180 }),
    // 买过一份一次性的流量版（那份已用完停用，不在 subs 里；叠加只能落在一次性那份上，所以这里是新购）
    order(now, 6, { kind: 'new', status: 'fulfilled', plan_name: traffic.name, item_name: traffic.name, interval: 'one_time', interval_count: 1, subtotal: trafficPrice.unit_amount, balance_applied: 2000 }),
    order(now, 9, { kind: 'topup', status: 'fulfilled', subtotal: 10000 }),
    order(now, 12, {
      kind: 'traffic_reset',
      status: 'expired',
      plan_name: pro.name,
      item_name: '重置本期流量',
      subtotal: 1000,
      subscription_id: sub.id,
      cancelled_at: new Date(now - 12 * DAY_MS + 30 * 60_000).toISOString(),
      effect: { type: 'reset', subId: sub.id },
    }),
    order(now, 20, { kind: 'new', status: 'cancelled', plan_name: std.name, item_name: std.name, interval: 'year', interval_count: 1, subtotal: std.prices[3]!.unit_amount, cancel_reason: 'user_cancelled', cancelled_at: new Date(now - 20 * DAY_MS).toISOString() }),
    order(now, 34, { ...renew, status: 'fulfilled' }),
    order(now, 40, { kind: 'topup', status: 'fulfilled', subtotal: 3000 }),
    order(now, 64, { ...renew, status: 'fulfilled' }),
    order(now, 75, { kind: 'new', status: 'cancelled', plan_name: pro.name, item_name: pro.name, interval: 'month', interval_count: 1, subtotal: month.unit_amount, cancel_reason: '支付渠道维护，系统取消' }),
    order(now, 94, { kind: 'new', status: 'fulfilled', plan_name: pro.name, item_name: pro.name, interval: 'month', interval_count: 1, subscription_id: sub.id, subtotal: month.unit_amount }),
    order(now, 96, { kind: 'topup', status: 'fulfilled', subtotal: 2000 }),
  ]
}

export function seedLedger(now: number): LedgerEntry[] {
  const at = (d: number) => new Date(now - d * DAY_MS).toISOString()
  return [
    { kind: 'gift_card_balance', delta: 1000, memo: '礼品卡 GC-0915-…', at: at(4) },
    { kind: 'balance_hold', delta: -2000, memo: '订单 PD-2003', at: at(6) },
    { kind: 'balance_topup', delta: 10000, memo: '充值 PD-2004', at: at(9) },
    { kind: 'commission_to_balance', delta: 1650, memo: '佣金转入', at: at(15) },
    { kind: 'late_payment_applied', delta: 500, memo: '待处理到账转入余额', at: at(22) },
    { kind: 'plan_change_refund', delta: 380, memo: '变更套餐差额退回', at: at(30) },
    { kind: 'balance_adjusted', delta: -8880, memo: '客服调整：重复充值退回', at: at(33) },
    { kind: 'balance_topup', delta: 3000, memo: '充值 PD-2008', at: at(40) },
    { kind: 'balance_hold', delta: -1000, memo: '订单 PD-2011', at: at(75) },
    { kind: 'balance_release', delta: 1000, memo: '订单 PD-2011 取消', at: at(75) },
  ]
}

export function seedRedemptions(now: number): RedemptionFixture[] {
  return [
    { template_name: '新人余额卡', type: 'general', code_hint: 'GC-0915-K2PQ…', balance: 1000, redeemed_at: new Date(now - 4 * DAY_MS).toISOString() },
    { template_name: '中秋流量礼包', type: 'general', code_hint: 'GC-0830-B4NC…', traffic_bytes: 100 * GIB, redeemed_at: new Date(now - 25 * DAY_MS).toISOString() },
  ]
}
