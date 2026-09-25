/**
 * [INPUT]: 依赖 vitest，依赖 ./model 的结账纯逻辑，依赖 ../common/catalog 的周期与价格文案，依赖 ../plans/labels 的选购页入口，依赖 ../common/PayFlow 的回跳地址与成功文案，依赖 ../../queries 的 subscriptionSchema
 * [OUTPUT]: 无（测试文件）
 * [POS]: 第 ② 步选购与结账的单元测试：模式判定（新购 / 续费 / 变更 / 流量包）、续费遇改价、订单预览（优惠、余额抵扣、变更折算与退余额）、四种下单请求体、优惠码试算请求体与文案、周期归档与省幅、选购页入口、支付回跳地址与成功文案
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it } from 'vitest'
import { subscriptionSchema, type Subscription } from '../../queries'
import { monthlyNote, periodName, periodOf, periodUnit, perGbNote, quotaPeriodNote, resetNote, savingAmount, savingPercent, type Pack, type Plan, type Price } from '../common/catalog'
import { payReturnUrl, paySuccessText } from '../common/PayFlow'
import { availablePeriods, fromPrice, planAction } from '../plans/labels'
import { buildQuote, couponNote, couponPreviewBody, defaultPriceId, isRepriced, normalizeCoupon, orderRequest, periodOptions, resolveMode, type CheckoutMode } from './model'

const GIB = 1024 ** 3
const price = (id: string, unit_amount: number, billing_interval: Price['billing_interval'], interval_count = 1, currency = 'CNY'): Price => ({ id, currency, unit_amount, billing_interval, interval_count, trial_days: 0 })

const STD: Plan = {
  id: 'std',
  code: 'std',
  name: '标准版',
  description: null,
  version: 1,
  max_devices: 3,
  quotas: [{ metric: 'traffic.bytes', limit: 200 * GIB, unit: 'bytes', period: 'cycle' }],
  prices: [price('s1', 2900, 'month'), price('s3', 7900, 'quarter'), price('s12', 29900, 'year'), price('su', 499, 'month', 1, 'USD')],
  quota_reset_strategy: 'natural_month',
  allow_renewal: true,
  allow_upgrade: true,
  throttle_kbps: null,
  highlights: [],
  recommended: false,
}
const PRO: Plan = { ...STD, id: 'pro', code: 'pro', name: '专业版', prices: [price('p1', 5900, 'month'), price('p3', 15900, 'month', 3), price('p12', 59900, 'month', 12)] }
const FAM: Plan = { ...STD, id: 'fam', code: 'fam', name: '家庭版', allow_upgrade: false, prices: [price('f1', 4900, 'month')] }
const PACK: Pack = { id: 'k', name: '500 GB', traffic_bytes: 500 * GIB, currency: 'CNY', unit_amount: 9900, recommended: true }

function sub(over: Partial<Subscription> = {}): Subscription {
  return subscriptionSchema.parse({
    id: 'sub1',
    plan_id: 'pro',
    price_id: 'p1',
    plan_name: '专业版',
    plan_version: 1,
    status: 'active',
    current_period_start: '2026-09-01T00:00:00Z',
    current_period_end: '2026-11-01T00:00:00Z',
    currency: 'CNY',
    amount: 5900,
    quotas: [],
    ...over,
  })
}

const q = (s: string) => new URLSearchParams(s)

describe('结账模式', () => {
  it('没有订阅走新购，同套餐走续费，换套餐走变更（契约门户-03）', () => {
    expect(resolveMode(q('plan=std&price=s1'), [STD, PRO], [], [])).toMatchObject({ mode: { kind: 'new' } })
    expect(resolveMode(q('plan=pro'), [STD, PRO], [], [sub()])).toMatchObject({ mode: { kind: 'renew', sub: { id: 'sub1' } } })
    expect(resolveMode(q('plan=std'), [STD, PRO], [], [sub()])).toMatchObject({ mode: { kind: 'change', plan: { id: 'std' } } })
    // 过期订阅不算
    expect(resolveMode(q('plan=std'), [STD], [], [sub({ status: 'expired' })])).toMatchObject({ mode: { kind: 'new' } })
  })

  it('续费、流量包与异常地址', () => {
    expect(resolveMode(q('renew=sub1'), [PRO], [], [sub()])).toMatchObject({ mode: { kind: 'renew', plan: { id: 'pro' } } })
    expect(resolveMode(q('renew=sub1'), [STD], [], [sub()])).toMatchObject({ mode: { kind: 'renew', plan: null } })
    expect(resolveMode(q('renew=sub1'), [PRO], [], [sub({ status: 'cancelled' })])).toEqual({ problem: 'sub_gone' })
    expect(resolveMode(q('pack=k'), [], [PACK], [])).toMatchObject({ mode: { kind: 'pack' } })
    expect(resolveMode(q('pack=x'), [], [PACK], [])).toEqual({ problem: 'pack_gone' })
    expect(resolveMode(q('plan=x'), [STD], [], [])).toEqual({ problem: 'plan_gone' })
    expect(resolveMode(q(''), [STD], [], [])).toEqual({ problem: 'missing' })
  })

  it('周期选项只列 CNY，按月 / 季 / 年排序', () => {
    expect(periodOptions({ kind: 'new', plan: STD }).map((p) => p.id)).toEqual(['s1', 's3', 's12'])
  })

  it('续费沿用原价格；原价格失效（改价）时选同周期的当前价格并标记', () => {
    const renew = (rp: Subscription['renewal_price']): CheckoutMode => ({ kind: 'renew', sub: sub({ plan_id: 'std', renewal_price: rp }), plan: STD })
    const same = renew({ id: 's3', currency: 'CNY', unit_amount: 7900, billing_interval: 'quarter', interval_count: 1, available: true })
    expect(defaultPriceId(same, periodOptions(same), null)).toBe('s3')
    expect(isRepriced(same)).toBe(false)
    const repriced = renew({ id: 'old', currency: 'CNY', unit_amount: 2500, billing_interval: 'month', interval_count: 1, available: false })
    expect(periodOptions(repriced).map((p) => p.id)).toEqual(['s1', 's3', 's12'])
    expect(defaultPriceId(repriced, periodOptions(repriced), null)).toBe('s1')
    expect(isRepriced(repriced)).toBe(true)
    // 仍有效但不在目录里的原价格（组专属价）补进选项
    const hidden = renew({ id: 'grp', currency: 'CNY', unit_amount: 2000, billing_interval: 'month', interval_count: 1, available: true })
    expect(periodOptions(hidden).map((p) => p.id)).toContain('grp')
    // 地址点名的价格优先
    expect(defaultPriceId(same, periodOptions(same), 's12')).toBe('s12')
  })
})

describe('订单预览', () => {
  it('优惠后再按开关抵扣余额，余额不够只抵一部分', () => {
    expect(buildQuote({ subtotal: 2900, currency: 'CNY', discount: 580, balance: 2650, useBalance: true })).toMatchObject({ due: 2320, balanceApplied: 2320, payable: 0 })
    expect(buildQuote({ subtotal: 2900, currency: 'CNY', discount: 0, balance: 1000, useBalance: true })).toMatchObject({ balanceApplied: 1000, payable: 1900 })
    expect(buildQuote({ subtotal: 2900, currency: 'CNY', discount: 0, balance: 1000, useBalance: false })).toMatchObject({ balanceApplied: 0, payable: 2900 })
    expect(buildQuote({ subtotal: 100, currency: 'CNY', discount: 500, balance: 0, useBalance: false }).payable).toBe(0)
  })

  it('变更套餐用服务端试算：折算、优惠与退余额', () => {
    const change = { direction: 'downgrade' as const, currency: 'CNY', subtotal: 2900, proration_credit: 4000, discount: 0, total: 0, balance_refund: 1100, current_period_end: 'a', new_period_start: 'b', new_period_end: 'c' }
    expect(buildQuote({ subtotal: 0, currency: 'CNY', discount: 0, change, balance: 5000, useBalance: true })).toEqual({ currency: 'CNY', subtotal: 2900, discount: 0, credit: 4000, due: 0, balanceApplied: 0, payable: 0, refund: 1100 })
  })
})

describe('请求体（后端拒绝多余字段）', () => {
  it('可选字段不用时不传', () => {
    expect(orderRequest({ kind: 'new', plan: STD }, 's1', 0, null)).toEqual({ path: 'v1/orders', body: { plan_id: 'std', price_id: 's1' } })
    expect(orderRequest({ kind: 'new', plan: STD }, 's1', 500, 'AUTUMN26')).toEqual({ path: 'v1/orders', body: { plan_id: 'std', price_id: 's1', use_balance: 500, coupon_code: 'AUTUMN26' } })
    expect(orderRequest({ kind: 'renew', sub: sub(), plan: PRO }, 'p3', 0, null)).toEqual({ path: 'v1/me/subscriptions/sub1/renew', body: { price_id: 'p3' } })
    expect(orderRequest({ kind: 'change', sub: sub(), plan: STD }, 's1', 0, null)).toEqual({ path: 'v1/me/subscriptions/sub1/change-plan', body: { plan_id: 'std', price_id: 's1' } })
    expect(orderRequest({ kind: 'pack', pack: PACK }, null, 100, null)).toEqual({ path: 'v1/me/traffic-pack-orders', body: { pack_id: 'k', use_balance: 100 } })
  })

  it('优惠码试算：流量包用 pack_id 形态，续费用订阅的套餐', () => {
    expect(couponPreviewBody({ kind: 'pack', pack: PACK }, null, 'X')).toEqual({ pack_id: 'k', coupon_code: 'X' })
    expect(couponPreviewBody({ kind: 'renew', sub: sub(), plan: null }, 'p1', 'X')).toEqual({ plan_id: 'pro', price_id: 'p1', coupon_code: 'X' })
    expect(couponPreviewBody({ kind: 'new', plan: STD }, null, 'X')).toBeNull()
    expect(normalizeCoupon('  autumn26 ')).toBe('AUTUMN26')
  })

  it('优惠码文案：percent 为万分比', () => {
    const fmt = (m: number) => `¥${(m / 100).toFixed(2)}`
    expect(couponNote('A', { discount_type: 'percent', discount_value: 2000 }, 580, fmt)).toBe('已使用 A：20% 折扣')
    expect(couponNote('B', { discount_type: 'fixed', discount_value: 10000 }, 10000, fmt)).toBe('已使用 B：立减 ¥100.00')
    expect(couponNote('C', null, 300, fmt)).toBe('已使用 C：优惠 ¥3.00')
  })
})

describe('目录文案', () => {
  it('周期归档与文案', () => {
    expect([periodOf(price('a', 1, 'quarter')), periodOf(price('b', 1, 'month', 12)), periodOf(price('c', 1, 'week', 2))]).toEqual(['3m', '12m', 'week:2'])
    expect([periodName('12m'), periodName('week:2'), periodName('one_time:1')]).toEqual(['年付', '每 2 周', '一次性'])
    expect([periodUnit('3m'), periodUnit('month:2'), periodUnit('day:1')]).toEqual(['/ 季', '/ 2 个月', '/ 天'])
  })

  it('折合月价、省额与最小省幅', () => {
    expect(monthlyNote(price('m', 2900, 'month'))).toBe('按月付费')
    expect(monthlyNote(price('y', 29900, 'year'))).toBe('折合 ¥24.92 / 月')
    expect(savingAmount(STD, STD.prices[2]!)).toBe(2900 * 12 - 29900)
    // 标准版年付省 14%，专业版省 15% → 取 14
    expect(savingPercent([STD, PRO], '12m')).toBe(14)
    expect(savingPercent([STD], '1m')).toBe(0)
    expect(perGbNote(PACK)).toBe('约 ¥0.20 / GB')
  })

  it('重置与额度周期文案（R69 缺席时按到期日重置）', () => {
    expect(resetNote({ quota_reset_strategy: 'fixed_day', quota_reset_day: 15 })).toBe('每月 15 日重置')
    expect(resetNote({})).toBe('到期日自动重置')
    expect(quotaPeriodNote({ quota_reset_strategy: 'billing_cycle' }, 'cycle', '12m')).toBe('/ 年')
    expect(quotaPeriodNote({ quota_reset_strategy: 'natural_month' }, 'cycle', '12m')).toBe('/ 月')
    expect(quotaPeriodNote({}, 'total', '1m')).toBe('总量')
  })
})

describe('选购页', () => {
  it('周期分段与起价', () => {
    expect(availablePeriods([STD, PRO])).toEqual([
      { key: '1m', label: '月付' },
      { key: '3m', label: '季付 · 省 9%' },
      { key: '12m', label: '年付 · 省 14%' },
    ])
    expect(fromPrice([STD, PRO])).toBe(' · ¥29.00 起 / 月')
    expect(fromPrice([])).toBe('')
  })

  it('套餐卡入口：续费 / 变更 / 新购 / 不可用', () => {
    const cur = sub()
    expect(planAction(PRO, PRO.prices[0], cur, '1m', true)).toEqual({ label: '续费', href: '#/checkout?renew=sub1&price=p1' })
    expect(planAction({ ...PRO, allow_renewal: false }, PRO.prices[0], cur, '1m', true).href).toBeNull()
    expect(planAction(STD, STD.prices[0], cur, '1m', true)).toEqual({ label: '换成标准版', href: '#/checkout?plan=std&price=s1' })
    expect(planAction(FAM, FAM.prices[0], cur, '1m', true)).toEqual({ label: '不支持变更', href: null })
    expect(planAction(FAM, FAM.prices[0], null, '1m', false)).toEqual({ label: '选择家庭版', href: '#/checkout?plan=fam&price=f1' })
    expect(planAction(FAM, undefined, null, '12m', false)).toEqual({ label: '暂无年付', href: null })
  })
})

describe('支付', () => {
  it('回跳地址相对入口页解析、带 paid=1', () => {
    expect(payReturnUrl('o1', 'https://panel.example.com/#/checkout?plan=x')).toBe('https://panel.example.com/#/orders/o1?paid=1')
    expect(payReturnUrl('o 1', 'https://h.example/app/index.html#/x')).toBe('https://h.example/app/#/orders/o%201?paid=1')
  })

  it('成功文案按订单种类', () => {
    const base = { paid_amount: 0, total_amount: 5000, currency: 'CNY' }
    expect(paySuccessText({ ...base, kind: 'addon' })).toBe('流量包已到账')
    expect(paySuccessText({ ...base, kind: 'upgrade' })).toBe('已开通，订阅地址不变')
    expect(paySuccessText({ ...base, kind: 'topup' })).toBe('余额 +¥50.00')
    expect(paySuccessText({ ...base, kind: 'new', subscription_period_end: '2027-09-19T10:00:00Z' })).toBe('已开通，有效期至 2027-09-19')
    expect(paySuccessText({ ...base, kind: 'renewal' })).toBe('已开通')
  })
})
