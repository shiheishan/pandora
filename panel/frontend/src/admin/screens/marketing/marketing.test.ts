/**
 * [INPUT]: 依赖 vitest，依赖 ./logic 的全部纯函数，依赖 ./schemas 的 schema（核对契约形状的边界：nil 切片、可选的待补字段）
 * [OUTPUT]: 对外提供营销页纯逻辑的单元测试
 * [POS]: admin/screens/marketing 的单元测试：输入换算、优惠文案、券 / 卡码 / 提现的状态映射、面额与兑换内容、批次名、三张表单到请求体的构建与校验、待补·后端字段缺失时的降级；界面交互在浏览器里对 dev/mock/admin/marketing.ts 验收
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  GB,
  basisPointsToPercent,
  batchFileName,
  batchLabel,
  buildCommissionConfig,
  buildCouponRequest,
  buildGenerateRequest,
  buildTemplateRequest,
  commissionStats,
  commissionToForm,
  couponBatchLabel,
  couponState,
  discountLabel,
  emptyCouponForm,
  formatGB,
  grantedLabel,
  minorToYuan,
  parseCount,
  percentToBasisPoints,
  planScopeLabel,
  redeemRate,
  templateFace,
  templateToForm,
  usage,
  validUntilLabel,
  withdrawalState,
  yuanToMinor,
} from './logic'
import { couponSchema, overviewSchema, templatesResponse, type CommissionOverview, type GiftTemplate, type Plan } from './schemas'

const PLANS: Plan[] = [
  { id: 'p1', name: '基础版', status: 'active', prices: [] },
  { id: 'p2', name: '专业版', status: 'active', prices: [] },
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
  it('labels discounts: basis points as percent, fixed in its currency (blank means CNY)', () => {
    expect(discountLabel({ discount_type: 'percent', discount_value: 1500, currency: '' })).toBe('15% 折扣')
    expect(discountLabel({ discount_type: 'fixed', discount_value: 1000, currency: '' })).toBe('立减 ¥10.00')
    expect(discountLabel({ discount_type: 'fixed', discount_value: 500, currency: 'USD' })).toBe('立减 $5.00')
  })

  it('shows usage against the cap, unlimited when null', () => {
    expect(usage({ redeemed_count: 412, max_redemptions: 1000 })).toEqual({ label: '412 / 1000', ratio: 0.412 })
    expect(usage({ redeemed_count: 5, max_redemptions: null })).toEqual({ label: '5 / 不限', ratio: 0 })
    expect(usage({ redeemed_count: 120, max_redemptions: 100 }).ratio).toBe(1)
    expect(validUntilLabel(null)).toBe('长期')
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
    const fixed = buildCouponRequest({ ...emptyCouponForm(false), code: 'X', kind: 'fixed', value: '10', currency: 'USD', minOrder: '50', planIds: ['p1'] }, false)
    expect(fixed.ok && fixed.body).toMatchObject({ discount_type: 'fixed', discount_value: 1000, currency: 'USD', max_redemptions: null, min_order_amount: 5000, applicable_plan_ids: ['p1'] })
    // fixed 不带封顶优惠，percent 不带币种
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

  describe('valid_until', () => {
    beforeEach(() => vi.useFakeTimers({ now: new Date('2026-09-24T10:00:00Z') }))
    afterEach(() => vi.useRealTimers())
    it('sends RFC3339 and rejects the past', () => {
      const future = buildCouponRequest({ ...emptyCouponForm(false), code: 'A', validUntil: '2026-10-07T23:59' }, false)
      expect(future.ok && future.body.valid_until).toMatch(/^2026-10-0\dT\d\d:59:00\.000Z$/)
      const past = buildCouponRequest({ ...emptyCouponForm(false), code: 'A', validUntil: '2026-01-01T00:00' }, false)
      expect(!past.ok && past.errors.valid_until).toBe('结束时间要晚于现在')
    })
  })

  it('accepts the Go nil slice for applicable_plan_ids', () => {
    const row = couponSchema.parse({
      id: 'c',
      code: 'A',
      name: 'A',
      discount_type: 'percent',
      discount_value: 1,
      currency: '',
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

describe('gift cards', () => {
  const tpl = (t: Partial<GiftTemplate>): GiftTemplate => ({
    id: 't',
    name: '卡',
    description: '',
    type: 'general',
    status: 'active',
    rewards: {},
    conditions: {},
    limits: {},
    theme_color: '',
    code_total: 0,
    code_used: 0,
    created_at: '2026-09-24T00:00:00Z',
    ...t,
  })

  it('derives the face, kind and after-redeem text per card type', () => {
    expect(templateFace(tpl({ rewards: { balance: 10000 } }), PLANS)).toEqual({ face: '¥100.00', kind: '余额', after: '直接入账' })
    expect(templateFace(tpl({ rewards: { traffic_bytes: 100 * GB } }), PLANS)).toEqual({ face: '100 GB', kind: '流量', after: '当期有效' })
    expect(templateFace(tpl({ rewards: { expire_days: 30 } }), PLANS).face).toBe('30 天')
    expect(templateFace(tpl({ type: 'plan', rewards: { plan_id: 'p2' } }), PLANS)).toEqual({ face: '专业版', kind: '套餐', after: '立即开通' })
    expect(templateFace(tpl({ type: 'plan', rewards: { plan_id: 'p2' } }), undefined).face).toBe('套餐')
    expect(templateFace(tpl({ type: 'mystery', rewards: { pool: [{ label: 'a', weight: 1 }, { label: 'b', weight: 1 }] } }), PLANS)).toMatchObject({ face: '盲盒', kind: '2 个奖品' })
  })

  it('describes what a redemption granted', () => {
    expect(grantedLabel({ granted: { balance: 10000 }, template_name: '100 元卡' })).toBe('余额 +¥100.00')
    expect(grantedLabel({ granted: { traffic_bytes: 100 * GB, expire_days: 30 }, template_name: 'x' })).toBe('流量 +100 GB · +30 天')
    expect(grantedLabel({ granted: {}, template_name: '专业版月卡' })).toBe('专业版月卡')
    expect(grantedLabel({ granted: { balance: 500 }, template_name: '盲盒', prize_label: '¥5 余额' })).toBe('¥5 余额（余额 +¥5.00）')
  })

  it('names batches and export files from the id', () => {
    const b = { id: '3f9a1c2b-0000-4000-8000-000000000000', created_at: new Date(2026, 8, 23, 12).toISOString() }
    expect(batchLabel(b)).toBe('GB-0923-3F9A')
    expect(batchFileName(b.id)).toBe('gift-codes-3f9a1c2b.csv')
    expect(redeemRate(1287, 2040)).toBe('63.1%')
    expect(redeemRate(0, 0)).toBe('—')
  })

  it('round-trips a template through the form and sends only known reward keys', () => {
    const t = tpl({ id: 'x', rewards: { balance: 10050, traffic_bytes: 50 * GB, reset_quota: true }, conditions: { require_invite: true }, limits: { max_use_per_user: 2 }, theme_color: '#123456' })
    const built = buildTemplateRequest(templateToForm(t))
    expect(built).toEqual({
      ok: true,
      body: {
        id: 'x',
        name: '卡',
        description: '',
        type: 'general',
        status: 'active',
        rewards: { balance: 10050, traffic_bytes: 50 * GB, reset_quota: true },
        conditions: { require_invite: true },
        limits: { max_use_per_user: 2 },
        theme_color: '#123456',
      },
    })
  })

  it('mirrors the backend reward and condition rules', () => {
    const empty = buildTemplateRequest({ ...templateToForm(null), name: '空卡' })
    expect(!empty.ok && empty.errors.rewards).toBe('通用卡至少要送一样东西：余额、流量、延长到期或重置流量')
    const both = buildTemplateRequest({ ...templateToForm(null), name: 'x', balance: '1', newUserOnly: true, paidUserOnly: true })
    expect(!both.ok && Object.keys(both.errors)).toEqual(['conditions'])
    const plan = buildTemplateRequest({ ...templateToForm(null), name: 'x', type: 'plan', planId: 'p1' })
    expect(!plan.ok && plan.errors.rewards).toBe('套餐卡要选定套餐和价格')
    const mystery = buildTemplateRequest({
      ...templateToForm(null),
      name: 'x',
      type: 'mystery',
      pool: [
        { label: '¥5', weight: '80', balance: '5', trafficGb: '', days: '' },
        { label: '空奖', weight: '20', balance: '', trafficGb: '', days: '' },
      ],
    })
    expect(!mystery.ok && mystery.errors.rewards).toBe('第 2 个奖品什么都不送')
    const pool = buildTemplateRequest({
      ...templateToForm(null),
      name: 'x',
      type: 'mystery',
      pool: [
        { label: '¥5', weight: '80', balance: '5', trafficGb: '', days: '' },
        { label: '50G', weight: '20', balance: '', trafficGb: '50', days: '' },
      ],
    })
    expect(pool.ok && pool.body.rewards).toEqual({
      pool: [
        { label: '¥5', weight: 80, balance: 500 },
        { label: '50G', weight: 20, traffic_bytes: 50 * GB },
      ],
    })
  })

  describe('code generation', () => {
    beforeEach(() => vi.useFakeTimers({ now: new Date('2026-09-24T10:00:00Z') }))
    afterEach(() => vi.useRealTimers())
    it('limits count and prefix and sends only what was filled', () => {
      expect(buildGenerateRequest({ count: '100', prefix: '', expiresAt: '' })).toEqual({ ok: true, body: { count: 100 } })
      expect(buildGenerateRequest({ count: '10', prefix: 'gc', expiresAt: '2026-12-31T23:59' })).toMatchObject({ ok: true, body: { count: 10, prefix: 'GC' } })
      const bad = buildGenerateRequest({ count: '5001', prefix: 'A-B', expiresAt: '2026-01-01T00:00' })
      expect(!bad.ok && bad.errors).toEqual({ count: '一次生成 1 到 5000 个', prefix: '只能用大写字母和数字，最多 8 位', expires_at: '有效期不能设在过去' })
    })
  })

  it('tolerates a null template list', () => {
    expect(templatesResponse.parse({ templates: null }).templates).toEqual([])
  })
})

describe('commission', () => {
  const overview: CommissionOverview = overviewSchema.parse({
    pending: 390400,
    available: 1,
    paid_out: 3166000,
    this_month: 0,
    entries: 0,
    need_review: 0,
    waiting_withdrawals: 0,
    rate_percent: 20,
    freeze_days: 7,
    min_withdraw: 10050,
  })

  it('maps withdrawal statuses; only approved can be paid', () => {
    expect(withdrawalState('requested')).toMatchObject({ label: '待审核', canReview: true, canPay: false })
    expect(withdrawalState('reviewing').canReview).toBe(true)
    expect(withdrawalState('approved')).toMatchObject({ label: '待打款', canPay: true })
    expect(withdrawalState('processing')).toMatchObject({ canReview: false, canPay: false })
    expect(withdrawalState('paid').label).toBe('已打款')
    expect(withdrawalState('failed').label).toBe('打款失败')
    expect(withdrawalState('returned').label).toBe('已退回')
  })

  it('shows 「—」 for the backend-pending totals and fills them once present', () => {
    expect(commissionStats(overview).map((s) => s.value)).toEqual(['—', '¥3,904.00', '¥31,660.00', '—'])
    expect(commissionStats({ ...overview, total_earned: 4821000, invited_users: 2318 }).map((s) => s.value)).toEqual(['¥48,210.00', '¥3,904.00', '¥31,660.00', '2,318'])
  })

  it('omits scope until the backend reports it (DisallowUnknownFields would reject the whole save)', () => {
    const form = commissionToForm(overview)
    expect(form).toEqual({ rate: 20, scope: 'every_order', freezeDays: '7', minWithdraw: '100.50' })
    expect(buildCommissionConfig(form, false)).toEqual({ ok: true, body: { rate_percent: 20, freeze_days: 7, min_withdraw: 10050 } })
    expect(buildCommissionConfig({ ...form, scope: 'first_order' }, true)).toEqual({ ok: true, body: { rate_percent: 20, freeze_days: 7, min_withdraw: 10050, scope: 'first_order' } })
    const bad = buildCommissionConfig({ rate: 51, scope: 'every_order', freezeDays: '91', minWithdraw: 'x' }, false)
    expect(!bad.ok && Object.keys(bad.errors).sort()).toEqual(['freeze_days', 'min_withdraw', 'rate_percent'])
  })
})
