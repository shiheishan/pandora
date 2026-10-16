import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { GB, basisPointsToPercent, buildCouponRequest, couponBatchLabel, couponPeriodLabel, couponState, discountLabel, emptyCouponForm, formatGB, minorToYuan, parseCount, percentToBasisPoints, planScopeLabel, usage, yuanToMinor } from './logic'
import { couponSchema, type Plan } from './schemas'

const PLANS: Plan[] = [
  { id: 'p1', name: '基础版', kind: 'subscription', status: 'active', prices: [] },
  { id: 'p2', name: '专业版', kind: 'subscription', status: 'active', prices: [] },
]

describe('input conversion', () => {
  it('turns yuan and percent into minor units and back, rejecting a third decimal', () => {
    expect(yuanToMinor('10')).toBe(1000)
    expect(yuanToMinor('10.5')).toBe(1050)
    expect(yuanToMinor(' 0.05 ')).toBe(5)
    expect(yuanToMinor('9.999')).toBeNull()
    expect(yuanToMinor('-1')).toBeNull()
    expect(yuanToMinor('')).toBeNull()
    expect(percentToBasisPoints('15')).toBe(1500)
    expect(percentToBasisPoints('15.5')).toBe(1550)
    expect(basisPointsToPercent(1550)).toBe('15.5')
    expect(minorToYuan(1050)).toBe('10.50')
    expect(minorToYuan(10000)).toBe('100')
  })

  it('parses counts: blank is unset, anything but digits is invalid', () => {
    expect(parseCount('')).toBeNull()
    expect(parseCount('12')).toBe(12)
    expect(parseCount('1.5')).toBeNaN()
    expect(parseCount('-1')).toBeNaN()
  })

  it('formats bytes as GB', () => {
    expect(formatGB(100 * GB)).toBe('100 GB')
    expect(formatGB(1.5 * GB)).toBe('1.5 GB')
  })
})

describe('coupons', () => {
  it('labels discounts: basis points as percent, fixed in CNY', () => {
    expect(discountLabel({ discount_type: 'percent', discount_value: 1500 })).toBe('15% 折扣')
    expect(discountLabel({ discount_type: 'fixed', discount_value: 1000 })).toBe('立减 ¥10.00')
    expect(discountLabel({ discount_type: 'fixed', discount_value: 500 })).toBe('立减 ¥5.00')
  })

  it('shows usage against the cap, unlimited when null', () => {
    expect(usage({ redeemed_count: 412, max_redemptions: 1000 })).toEqual({ label: '412 / 1000', ratio: 0.412 })
    expect(usage({ redeemed_count: 5, max_redemptions: null })).toEqual({ label: '5 / 不限', ratio: 0 })
    expect(usage({ redeemed_count: 120, max_redemptions: 100 }).ratio).toBe(1)
  })

  it('formats coupon period label for future start, until, or perpetual', () => {
    const now = new Date('2026-10-16T12:00:00Z')
    // 未来开始 + 结束
    expect(couponPeriodLabel({ valid_from: '2026-11-01T00:00:00Z', valid_until: '2026-12-01T00:00:00Z' }, now)).toEqual({
      short: '11-01 起 · 至 12-01',
      full: '2026-11-01 → 2026-12-01',
    })
    // 过去开始 + 长期（无结束）
    expect(couponPeriodLabel({ valid_from: '2026-09-01T00:00:00Z', valid_until: null }, now)).toEqual({
      short: '长期',
      full: '2026-09-01 → 长期',
    })
    // 都空
    expect(couponPeriodLabel({ valid_from: null, valid_until: null }, now)).toEqual({
      short: '长期',
      full: '创建即生效 → 长期',
    })
    // 过去开始 + 有结束
    expect(couponPeriodLabel({ valid_from: '2026-10-01T00:00:00Z', valid_until: '2026-11-01T00:00:00Z' }, now)).toEqual({
      short: '至 11-01',
      full: '2026-10-01 → 2026-11-01',
    })
  })

  it('maps plan scope to names, falling back to a count without the catalog', () => {
    expect(planScopeLabel([], PLANS)).toBe('全部套餐')
    expect(planScopeLabel(['p2', 'p1'], PLANS)).toBe('专业版、基础版')
    expect(planScopeLabel(['p2', 'gone'], PLANS)).toBe('专业版、已删除的套餐')
    expect(planScopeLabel(['p1', 'p2'], undefined)).toBe('2 个套餐')
  })

  it('only active and paused coupons get a switch; expired and exhausted become tags', () => {
    expect(couponState('active')).toMatchObject({ switchable: true, on: true })
    expect(couponState('paused')).toMatchObject({ switchable: true, on: false })
    expect(couponState('expired').tag).toEqual({ label: '已过期', tone: 'warn' })
    expect(couponState('exhausted').tag).toEqual({ label: '已用完', tone: 'neutral' })
    expect(couponBatchLabel({ code: 'KOLA7Q2', name: 'KOL 渠道' })).toBe('KOL 渠道')
    expect(couponBatchLabel({ code: 'AUTUMN26', name: 'AUTUMN26' })).toBe('')
  })

  it('builds a single-coupon body with contract units and optional fields only when filled', () => {
    const built = buildCouponRequest({ ...emptyCouponForm(false), code: 'autumn26', value: '15', maxRedemptions: '1000', maxDiscount: '30' }, false)
    expect(built).toEqual({
      ok: true,
      body: { code: 'AUTUMN26', discount_type: 'percent', discount_value: 1500, max_redemptions: 1000, max_redemptions_per_user: 1, max_discount: 3000 },
    })
    const fixed = buildCouponRequest({ ...emptyCouponForm(false), code: 'X', kind: 'fixed', value: '10', minOrder: '50', planIds: ['p1'] }, false)
    expect(fixed.ok && fixed.body).toMatchObject({ discount_type: 'fixed', discount_value: 1000, max_redemptions: null, min_order_amount: 5000, applicable_plan_ids: ['p1'] })
    // 没有 currency、applicable_intervals、max_discount
    expect(fixed.ok && 'currency' in fixed.body).toBe(false)
    expect(fixed.ok && 'applicable_intervals' in fixed.body).toBe(false)
    expect(fixed.ok && 'max_discount' in fixed.body).toBe(false)
  })

  it('validates with the same field keys the backend uses', () => {
    const bad = buildCouponRequest({ ...emptyCouponForm(false), value: '100.5', perUser: '0', maxRedemptions: '0' }, false)
    expect(bad.ok).toBe(false)
    expect(!bad.ok && Object.keys(bad.errors).sort()).toEqual(['code', 'discount_value', 'max_redemptions', 'max_redemptions_per_user'])
  })

  it('batch generation needs a name, a safe prefix and at most 500 codes', () => {
    const ok = buildCouponRequest({ ...emptyCouponForm(true), name: ' KOL 10 月 ', prefix: 'kol', count: '50' }, true)
    expect(ok.ok && ok.body).toMatchObject({ name: 'KOL 10 月', prefix: 'KOL', count: 50, max_redemptions: 1, discount_value: 1500 })
    expect(ok.ok && 'code' in ok.body).toBe(false)
    const bad = buildCouponRequest({ ...emptyCouponForm(true), prefix: 'TOO-LONG1', count: '501' }, true)
    expect(!bad.ok && Object.keys(bad.errors).sort()).toEqual(['count', 'name', 'prefix'])
  })

  describe('valid_from and valid_until', () => {
    beforeEach(() => vi.useFakeTimers({ now: new Date('2026-09-24T10:00:00Z') }))
    afterEach(() => vi.useRealTimers())
    it('sends RFC3339, converts valid_from and rejects past or start after end', () => {
      const future = buildCouponRequest({ ...emptyCouponForm(false), code: 'A', validFrom: '2026-10-01T00:00', validUntil: '2026-10-07T23:59' }, false)
      expect(future.ok && future.body.valid_from).toMatch(/^2026-10-01T\d\d:00:00\.000Z$/)
      expect(future.ok && future.body.valid_until).toMatch(/^2026-10-0\dT\d\d:59:00\.000Z$/)

      const past = buildCouponRequest({ ...emptyCouponForm(false), code: 'A', validUntil: '2026-01-01T00:00' }, false)
      expect(!past.ok && past.errors.valid_until).toBe('结束时间要晚于现在')

      const inverted = buildCouponRequest({ ...emptyCouponForm(false), code: 'A', validFrom: '2026-10-10T00:00', validUntil: '2026-10-05T00:00' }, false)
      expect(!inverted.ok && inverted.errors.valid_until).toBe('结束时间要晚于开始时间')
    })
  })

  it('accepts the Go nil slice for applicable_plan_ids', () => {
    const row = couponSchema.parse({
      id: 'c',
      code: 'A',
      name: 'A',
      discount_type: 'percent',
      discount_value: 1,
      max_discount: null,
      min_order_amount: 0,
      max_redemptions: null,
      max_redemptions_per_user: 1,
      redeemed_count: 0,
      applicable_plan_ids: null,
      valid_from: null,
      valid_until: null,
      status: 'active',
      created_at: '2026-09-24T00:00:00Z',
      discounted_total: 0,
    })
    expect(row.applicable_plan_ids).toEqual([])
  })
})
})
