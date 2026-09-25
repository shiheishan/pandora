/**
 * [INPUT]: 依赖 vitest，依赖 ./model 的钱包纯映射与 ./api 的 schema，依赖 ../common/orders 的订单页纯映射与 schema
 * [OUTPUT]: 无（测试文件）
 * [POS]: 第 ③ 步订单与钱包的单元测试：充值金额元转分与上下限、流水类型名（挂账按保留规则 6）、礼品卡卡面 / 说明 / 获得列、兑换响应 summary 必为数组；订单状态徽标、按月分组与已支付合计、展开区「结果」与事实行、筛选的状态集合
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it } from 'vitest'
import { groupByMonth, ORDER_FILTERS, orderDetailSchema, orderFacts, orderResult, statusBadge, type OrderDetail } from '../common/orders'
import { giftCardSchema, redeemResultSchema, type GiftCard } from './api'
import { giftFace, giftNote, ledgerLabel, normalizeGiftCode, parseTopupAmount, redemptionGain } from './model'

const GIB = 1024 ** 3

function card(over: Partial<GiftCard>): GiftCard {
  return giftCardSchema.parse({ id: 'c', name: '卡', description: '', type: 'general', status: 'active', rewards: {}, conditions: {}, limits: {}, theme_color: '#000', created_at: 'x', ...over })
}

describe('充值金额', () => {
  it('元转分，最多两位小数，¥1–¥50000', () => {
    expect(parseTopupAmount('88.8')).toEqual({ cents: 8880 })
    expect(parseTopupAmount(' 100 ')).toEqual({ cents: 10000 })
    expect(parseTopupAmount('0.99')).toEqual({ error: '最少充值 ¥1' })
    expect(parseTopupAmount('50000.01')).toEqual({ error: '单次最多充值 ¥50,000' })
    expect(parseTopupAmount('1.234')).toEqual({ error: '金额最多两位小数' })
    expect(parseTopupAmount('')).toEqual({ error: '请输入充值金额' })
    // 浮点：19.99 × 100 不能变成 1998
    expect(parseTopupAmount('19.99')).toEqual({ cents: 1999 })
  })
})

describe('余额流水', () => {
  it('账本类型映射，挂账两类都叫「挂账转入」，未知类型返回 null', () => {
    expect(ledgerLabel('balance_hold')).toBe('下单抵扣（冻结）')
    expect(ledgerLabel('late_payment_suspense')).toBe('挂账转入')
    expect(ledgerLabel('plan_change_refund')).toBeNull()
  })
})

describe('礼品卡', () => {
  it('卡面：余额 / 流量 / 延期组合、套餐、盲盒', () => {
    expect(giftFace(card({ rewards: { balance: 10000 } }))).toBe('¥100.00 余额')
    expect(giftFace(card({ rewards: { traffic_bytes: 200 * GIB, expire_days: 7 } }))).toBe('流量 +200 GB + 延长 7 天')
    expect(giftFace(card({ type: 'plan', plan_name: '专业版', interval: 'month', interval_count: 1 }))).toBe('专业版 月付')
    expect(giftFace(card({ type: 'mystery', rewards: { pool: [{ label: 'A', weight: 0 }, { label: 'B', weight: 0 }] } }))).toBe('盲盒：可能抽到 A / B')
    expect(giftFace(card({ name: '空卡' }))).toBe('空卡')
  })

  it('说明：后台说明优先，缺省按类型', () => {
    expect(giftNote(card({ description: '限国庆' }))).toBe('限国庆')
    expect(giftNote(card({ rewards: { traffic_bytes: GIB } }))).toBe('流量进流量包余额，不过期，用完为止')
    expect(giftNote(card({ type: 'mystery' }))).toBe('兑换时随机抽取其中一项')
  })

  it('兑换记录「获得」列与卡码归一', () => {
    expect(redemptionGain({ template_name: 'T', type: 'mystery', prize_label: '50 GB 流量', redeemed_at: 'x' })).toBe('50 GB 流量')
    expect(redemptionGain({ template_name: 'T', type: 'general', balance: 1000, redeemed_at: 'x' })).toBe('¥10.00 余额')
    expect(redemptionGain({ template_name: '专业版月卡', type: 'plan', redeemed_at: 'x' })).toBe('专业版月卡')
    expect(normalizeGiftCode(' gc-1024 myst ')).toBe('GC-1024MYST')
  })

  it('兑换响应的 summary 是非空数组，null 不放行（Redeem 空摘要即报错）', () => {
    expect(redeemResultSchema.parse({ template_name: 'T', type: 'general', summary: ['余额 +¥10.00'] }).summary).toEqual(['余额 +¥10.00'])
    expect(redeemResultSchema.safeParse({ template_name: 'T', type: 'general', summary: null }).success).toBe(false)
  })
})

// ---------------------------------------------------------------------------
// 订单页
// ---------------------------------------------------------------------------
function detail(over: Partial<OrderDetail> = {}): OrderDetail {
  return orderDetailSchema.parse({
    order: {
      id: 'o',
      order_no: 'PD-1',
      kind: 'new',
      status: 'fulfilled',
      currency: 'CNY',
      total_amount: 4720,
      discount_amount: 1180,
      balance_applied: 0,
      payable_amount: 4720,
      paid_amount: 4720,
      refunded_amount: 0,
      plan_name: '专业版',
      cancellable: false,
      created_at: '2026-09-21T10:00:00Z',
      items: [{ name: '专业版', quantity: 1, unit_amount: 5900, line_amount: 5900 }],
      payments: [{ status: 'succeeded', amount: 4720, currency: 'CNY', created_at: '2026-09-21T10:01:00Z', method: 'alipay', provider_name: '易支付' }],
      coupon_code: 'AUTUMN26',
      subscription_period_end: '2026-11-05T10:00:00Z',
      ...over,
    },
  }).order
}

describe('订单页', () => {
  it('状态徽标：已退款两类为 info，processing 为「处理中」', () => {
    expect(statusBadge('fulfilled')).toEqual({ label: '已支付', tone: 'ok' })
    expect(statusBadge('expired')).toEqual({ label: '已取消', tone: 'neutral' })
    expect(statusBadge('partially_refunded')).toEqual({ label: '部分退款', tone: 'info' })
    expect(statusBadge('processing').label).toBe('处理中')
  })

  it('筛选的状态集合：「全部」不含待支付', () => {
    expect(ORDER_FILTERS.all).not.toContain('pending_payment')
    expect(ORDER_FILTERS.cancelled).toEqual(['cancelled', 'expired'])
  })

  it('按月分组，只对已支付求和', () => {
    const rows = [
      { created_at: '2026-09-21T10:00:00Z', status: 'fulfilled' as const, total_amount: 100 },
      { created_at: '2026-09-12T10:00:00Z', status: 'cancelled' as const, total_amount: 900 },
      { created_at: '2026-08-15T10:00:00Z', status: 'paid' as const, total_amount: 300 },
    ]
    const groups = groupByMonth(rows)
    expect(groups.map((g) => [g.label, g.rows.length, g.paid])).toEqual([
      ['2026 年 9 月', 2, 100],
      ['2026 年 8 月', 1, 300],
    ])
  })

  it('结果：按状态与种类（契约门户-04 映射）', () => {
    expect(orderResult(detail())).toBe('易支付 · 有效期至 2026-11-05')
    expect(orderResult(detail({ kind: 'topup' }))).toBe('余额已到账')
    expect(orderResult(detail({ status: 'expired' }))).toBe('超时未支付，已自动取消')
    expect(orderResult(detail({ status: 'cancelled', cancel_reason: 'user_cancelled' }))).toBe('已由您取消')
    expect(orderResult(detail({ status: 'cancelled', cancel_reason: '渠道维护' }))).toBe('渠道维护')
    expect(orderResult(detail({ status: 'refunded', refunded_amount: 4720 }))).toBe('已退款 ¥47.20')
  })

  it('事实行：原价、优惠带券码、实付、支付记录；待支付显示过期时间', () => {
    const facts = Object.fromEntries(orderFacts(detail()).map((f) => [f.k, f.v]))
    expect(facts['原价']).toBe('¥59.00')
    expect(facts['优惠']).toBe('−¥11.80（AUTUMN26）')
    expect(facts['实付']).toBe('¥47.20')
    expect(facts['支付记录']).toMatch(/^易支付 · ¥47\.20 · 成功 · 2026-09-21 /)
    const open = orderFacts(detail({ status: 'pending_payment', payments: [], expires_at: '2026-09-24T10:30:00Z' })).map((f) => f.k)
    expect(open).toContain('过期时间')
    expect(open).not.toContain('实付')
  })
})
