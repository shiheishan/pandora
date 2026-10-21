import { describe, expect, it } from 'vitest'
import { groupByMonth, ORDER_FILTERS, orderDetailSchema, orderReceipt, orderResult, statusBadge, type OrderDetail } from '../common/orders'
import { giftCardSchema, redeemResultSchema, type GiftCard } from './api'
import { giftFace, giftKind, giftNote, ledgerByMonth, ledgerLabel, ledgerView, normalizeGiftCode, parseTopupAmount, redemptionGain } from './model'

const GIB = 1024 ** 3

function card(over: Partial<GiftCard>): GiftCard {
  return giftCardSchema.parse({ id: 'c', name: '卡', description: '', type: 'general', status: 'active', rewards: {}, conditions: {}, limits: {}, theme_color: '#000', created_at: 'x', placement: null, ...over })
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
    expect(ledgerLabel('balance_hold')).toBe('下单用余额')
    expect(ledgerLabel('late_payment_suspense')).toBe('待处理到账转入')
    expect(ledgerLabel('plan_change_refund')).toBe('换套餐退回')
    expect(ledgerLabel('something_new')).toBeNull()
  })

  it('副行只写时间与按类型的固定短语，后端 memo 不露出来；只有人工调整写 memo（管理员原因）', () => {
    const at = '2026-09-16T09:03:00'
    const refund = ledgerView({ kind: 'plan_change_refund', delta: 380, memo: '换套餐剩余价值退回余额', at })
    expect(refund).toEqual({ title: '换套餐退回', sub: '09-16 09:03 · 差额退回', icon: 'swap', out: false, amount: '+¥3.80' })
    expect(ledgerView({ kind: 'balance_release', delta: 1000, memo: 'user_cancelled', at }).sub).toBe('09-16 09:03 · 订单没付成，余额退回')
    expect(ledgerView({ kind: 'balance_topup', delta: 10000, memo: '余额充值', at }).sub).toBe('09-16 09:03')
    const hold = ledgerView({ kind: 'balance_hold', delta: -2000, memo: '', at })
    expect([hold.icon, hold.out, hold.amount]).toEqual(['out', true, '−¥20.00'])
    expect(ledgerView({ kind: 'balance_adjusted', delta: -8880, memo: ' 重复充值退回 ', at }).sub).toBe('09-16 09:03 · 重复充值退回')
    // 认不出的类型不拿 memo 当标题
    expect(ledgerView({ kind: 'something_new', delta: 100, memo: 'raw memo', at }).title).toBe('余额入账')
  })

  it('按月分组，保持顺序，不算合计', () => {
    const groups = ledgerByMonth([{ at: '2026-10-12T10:00:00' }, { at: '2026-10-01T10:00:00' }, { at: '2026-09-24T10:00:00' }])
    expect(groups.map((g) => [g.label, g.rows.length])).toEqual([
      ['2026 年 10 月', 2],
      ['2026 年 9 月', 1],
    ])
  })
})

describe('礼品卡', () => {
  it('卡面：余额 / 流量 / 延期组合、套餐、盲盒', () => {
    expect(giftFace(card({ rewards: { balance: 10000 } }))).toBe('¥100.00 余额')
    expect(giftFace(card({ rewards: { traffic_bytes: 200 * GIB, expire_days: 7 } }))).toBe('流量 +200 GB + 延长 7 天')
    expect(giftFace(card({ type: 'plan', plan_name: '专业版', interval: 'month', interval_count: 1 }))).toBe('专业版 · 1 个月')
    expect(giftFace(card({ type: 'plan', plan_name: '大包', plan_kind: 'one_time', validity_days: 30 }))).toBe('大包 · 一次性 · 有效 30 天')
    expect(giftFace(card({ type: 'plan', plan_name: '永久包', plan_kind: 'one_time', validity_days: undefined }))).toBe('永久包 · 一次性 · 永久有效')
    expect(giftFace(card({ type: 'mystery', rewards: { pool: [{ label: 'A', weight: 0 }, { label: 'B', weight: 0 }] } }))).toBe('盲盒：可能抽到 A / B')
    expect(giftFace(card({ name: '空卡' }))).toBe('空卡')
  })

  it('卡面小字：只有一种奖励叫那种卡，混合叫礼品卡', () => {
    expect(giftKind(card({ type: 'plan', plan_name: '专业版' }))).toBe('套餐卡')
    expect(giftKind(card({ type: 'mystery' }))).toBe('盲盒')
    expect(giftKind(card({ rewards: { balance: 1000 } }))).toBe('余额卡')
    expect(giftKind(card({ rewards: { reset_quota: true } }))).toBe('流量重置卡')
    expect(giftKind(card({ rewards: { balance: 1000, expire_days: 7 } }))).toBe('礼品卡')
  })

  it('说明：后台说明优先，缺省按类型', () => {
    expect(giftNote(card({ description: '限国庆' }))).toBe('限国庆')
    expect(giftNote(card({ rewards: { traffic_bytes: GIB } }))).toBe('送的流量：按时间的套餐只在本期有效，一次性套餐加到总流量上')
    expect(giftNote(card({ type: 'mystery' }))).toBe('兑换时随机抽取其中一项')
  })

  it('兑换记录「获得」列与卡码归一', () => {
    expect(redemptionGain({ template_name: 'T', type: 'mystery', code_hint: 'GC-1…', prize_label: '50 GB 流量', redeemed_at: 'x' })).toBe('50 GB 流量')
    expect(redemptionGain({ template_name: 'T', type: 'general', code_hint: 'GC-2…', balance: 1000, redeemed_at: 'x' })).toBe('¥10.00 余额')
    expect(redemptionGain({ template_name: '专业版月卡', type: 'plan', code_hint: 'GC-3…', redeemed_at: 'x' })).toBe('专业版月卡')
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
      total_amount: 4720,
      discount_amount: 1180,
      balance_applied: 0,
      payable_amount: 4720,
      paid_amount: 4720,
      plan_name: '专业版',
      item_name: '专业版',
      cancellable: false,
      has_payment_intent: true,
      created_at: '2026-09-21T10:00:00Z',
      items: [{ name: '专业版', quantity: 1, unit_amount: 5900, line_amount: 5900, plan_id: 'plan-pro', price_id: 'price-pro-m' }],
      payments: [{ status: 'succeeded', amount: 4720, created_at: '2026-09-21T10:01:00Z', method: 'alipay', provider_name: '易支付' }],
      coupon_code: 'AUTUMN26',
      subscription_period_end: '2026-11-05T10:00:00Z',
      subscription_id: 's1',
      ...over,
    },
  }).order
}

describe('订单页', () => {
  it('状态徽标：pending_payment 为「待支付」', () => {
    expect(statusBadge('fulfilled')).toEqual({ label: '已支付', tone: 'ok' })
    expect(statusBadge('expired')).toEqual({ label: '已取消', tone: 'neutral' })
    expect(statusBadge('pending_payment').label).toBe('待支付')
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
    expect(orderResult(detail())).toBe('已开通，有效期至 2026-11-05')
    expect(orderResult(detail({ kind: 'renewal' }))).toBe('已续上，有效期至 2026-11-05')
    expect(orderResult(detail({ kind: 'topup' }))).toBe('余额已到账')
    expect(orderResult(detail({ kind: 'stack' }))).toBe('已叠加，总流量加上了')
    expect(orderResult(detail({ status: 'expired' }))).toBe('超时未支付，已自动取消')
    expect(orderResult(detail({ status: 'cancelled', cancel_reason: 'user_cancelled' }))).toBe('已由您取消')
    expect(orderResult(detail({ status: 'cancelled', cancel_reason: '渠道维护' }))).toBe('渠道维护')
    // 认不出的英文原因码不露出来
    expect(orderResult(detail({ status: 'cancelled', cancel_reason: 'admin_void' }))).toBe('已取消')
    // 套餐归档后才付款：不开通，钱转余额（10-16）
    expect(orderResult(detail({ status: 'cancelled', cancel_reason: 'plan_archived', surcharge_kept: 0 }))).toBe('套餐已停售，没有开通；付的钱已转入钱包余额')
    // 手续费不退（10-16 改口）：扣了手续费写「应付金额」；字段缺了用统一说法
    expect(orderResult(detail({ status: 'cancelled', cancel_reason: 'plan_archived', surcharge_kept: 300 }))).toBe('套餐已停售，没有开通；应付金额已转入钱包余额（支付手续费不退）')
    expect(orderResult(detail({ status: 'cancelled', cancel_reason: 'plan_archived' }))).toBe('套餐已停售，没有开通；应付金额已转入钱包余额，支付手续费不退')
  })

  it('收据：实付是渠道实收（payments 合计），不是 paid_amount', () => {
    const r = orderReceipt(detail({ total_amount: 5400, balance_applied: 680, payable_amount: 4720, paid_amount: 5400, discount_amount: 500, coupon_code: 'HELLO5' }))
    expect(r.result).toEqual({ text: '已开通，有效期至 2026-11-05', ok: true })
    expect(r.lines).toEqual([
      { k: '原价', v: '¥59.00' },
      { k: '优惠', v: '¥5.00', minus: true, code: 'HELLO5' },
      { k: '用了余额', v: '¥6.80', minus: true },
      { k: '实付', v: '¥47.20', total: true },
    ])
    // 付款方式的叫法，不是渠道商户名（易支付）
    expect(r.payments).toHaveLength(1)
    expect(r.payments[0]).toMatchObject({ name: '支付宝', method: 'alipay', amount: '¥47.20' })
    expect(r.payments[0]!.meta).toMatch(/^09-21 \d\d:01 · 成功$/)
  })

  it('收据：全部用余额付写「全部用余额」；换套餐单写「订单金额」不写原价', () => {
    const allBalance = orderReceipt(detail({ discount_amount: 0, coupon_code: undefined, balance_applied: 4720, payable_amount: 0, payments: [] }))
    expect(allBalance.lines.at(-1)).toEqual({ k: '实付', v: '全部用余额', total: true })
    const up = orderReceipt(detail({ kind: 'upgrade', total_amount: 7540, discount_amount: 500, coupon_code: 'HELLO5', payments: [{ status: 'succeeded', amount: 7540, created_at: '2026-09-21T10:01:00Z', method: 'wxpay', provider_name: '易支付' }] }))
    expect(up.lines.map((l) => l.k)).toEqual(['订单金额', '其中优惠', '实付'])
    expect(up.lines[0]).toEqual({ k: '订单金额', v: '¥75.40' })
    expect(up.lines[1]!.minus).toBeUndefined()
  })

  it('收据：取消的单余额写「已退回」、不写实付；待支付写「还要付」', () => {
    const cancelled = orderReceipt(detail({ status: 'cancelled', cancel_reason: 'user_cancelled', balance_applied: 2000, payments: [] }))
    expect(cancelled.result.ok).toBe(false)
    expect(cancelled.lines.map((l) => l.k)).toEqual(['原价', '优惠', '用了余额（已退回）'])
    const open = orderReceipt(detail({ status: 'pending_payment', payments: [], payable_amount: 4720, expires_at: '2026-09-24T10:30:00Z' }))
    expect(open.lines.at(-1)).toEqual({ k: '还要付', v: '¥47.20', total: true })
  })
})
