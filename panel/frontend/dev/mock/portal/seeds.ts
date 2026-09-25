/**
 * [INPUT]: 依赖 node:crypto 的 randomUUID，依赖 ./catalog 的目录，依赖 ./fixtures 的夹具类型（仅类型）
 * [OUTPUT]: 对外提供 pastOrders、seedLedger、seedRedemptions
 * [POS]: dev/mock/portal 的历史数据种子（不是模块，不进登记表）：订单页要跨月分组、「显示更早」分页（多于 6 条）、已支付 / 已取消 / 超时 / 已退款 / 处理中各种状态，钱包要余额流水与礼品卡兑换记录；与 fixtures 分开放，免得夹具文件越写越长
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomUUID } from 'node:crypto'
import { GIB, PACKS, PLANS } from './catalog.ts'
import type { LedgerEntry, OrderFixture, RedemptionFixture, SubFixture } from './fixtures.ts'

const DAY_MS = 86_400_000

let seq = 3000

function order(now: number, daysAgo: number, init: Partial<OrderFixture> & Pick<OrderFixture, 'kind' | 'status' | 'subtotal'>): OrderFixture {
  const created = new Date(now - daysAgo * DAY_MS).toISOString()
  const discount = init.discount_amount ?? 0
  const total = init.subtotal - discount
  const balanceApplied = init.balance_applied ?? 0
  const paid = ['paid', 'fulfilled', 'partially_refunded', 'refunded'].includes(init.status)
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

/** 近四个月的订单：12 条已结束 + 1 条处理中 */
export function pastOrders(now: number, sub: SubFixture): OrderFixture[] {
  const [std, pro] = [PLANS[0]!, PLANS[1]!]
  const month = pro.prices[0]!
  const pack = PACKS[1]!
  const renew = { kind: 'renewal' as const, plan_name: pro.name, item_name: pro.name, interval: month.billing_interval, interval_count: 1, subscription_id: sub.id, subtotal: month.unit_amount }
  return [
    order(now, 0.02, { kind: 'topup', status: 'processing', subtotal: 5000, expires_at: new Date(now + 25 * 60_000).toISOString() }),
    order(now, 3, { ...renew, status: 'fulfilled', coupon_code: 'AUTUMN26', discount_amount: 1180 }),
    order(now, 6, { kind: 'addon', status: 'fulfilled', plan_name: pack.name, item_name: pack.name, subtotal: pack.unit_amount, balance_applied: 2000 }),
    order(now, 9, { kind: 'topup', status: 'fulfilled', subtotal: 10000 }),
    order(now, 12, { kind: 'addon', status: 'expired', plan_name: pack.name, item_name: pack.name, subtotal: pack.unit_amount, cancelled_at: new Date(now - 12 * DAY_MS + 30 * 60_000).toISOString() }),
    order(now, 20, { kind: 'new', status: 'cancelled', plan_name: std.name, item_name: std.name, interval: 'year', interval_count: 1, subtotal: std.prices[2]!.unit_amount, cancel_reason: 'user_cancelled', cancelled_at: new Date(now - 20 * DAY_MS).toISOString() }),
    order(now, 34, { ...renew, status: 'fulfilled' }),
    order(now, 40, { kind: 'topup', status: 'fulfilled', subtotal: 3000 }),
    order(now, 52, { kind: 'addon', status: 'refunded', plan_name: pack.name, item_name: pack.name, subtotal: pack.unit_amount, refunded_amount: pack.unit_amount }),
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
    { kind: 'late_payment_applied', delta: 500, memo: '挂账转入余额', at: at(22) },
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
