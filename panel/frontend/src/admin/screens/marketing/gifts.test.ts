import { describe, expect, it } from 'vitest'
import { GB } from './logic'
import {
  batchFileName,
  batchLabel,
  buildGenerateRequest,
  buildTemplateRequest,
  codeFilterQuery,
  codesReportFileName,
  emptyTemplateForm,
  grantedLabel,
  priceLabel,
  redeemRate,
  templateFace,
  templateStatusBody,
} from './giftModel'
import { templatesResponse, type GiftTemplate, type Plan } from './schemas'

const PLANS: Plan[] = [
  {
    id: 'p1',
    name: '基础版',
    kind: 'subscription',
    status: 'active',
    prices: [
      { id: 'pr-m1', unit_amount: 1500, billing_interval: 'month', interval_count: 1, status: 'active' },
      { id: 'pr-m3', unit_amount: 4000, billing_interval: 'month', interval_count: 3, status: 'active' },
      { id: 'pr-m6', unit_amount: 7500, billing_interval: 'month', interval_count: 6, status: 'active' },
      { id: 'pr-y1', unit_amount: 14000, billing_interval: 'year', interval_count: 1, status: 'active' },
      { id: 'pr-archived', unit_amount: 1000, billing_interval: 'month', interval_count: 1, status: 'archived' },
    ],
  },
  {
    id: 'p2',
    name: '专业版',
    kind: 'subscription',
    status: 'active',
    prices: [{ id: 'pr-p2', unit_amount: 3000, billing_interval: 'month', interval_count: 1, status: 'active' }],
  },
  {
    id: 'p-onetime',
    name: '一次性流量包',
    kind: 'one_time',
    status: 'active',
    prices: [{ id: 'pr-ot', unit_amount: 5000, billing_interval: 'one_time', interval_count: 1, status: 'active' }],
  },
  {
    id: 'p-ot-noprice',
    name: '一次性无在售价格',
    kind: 'one_time',
    status: 'active',
    prices: [{ id: 'pr-ot-arch', unit_amount: 5000, billing_interval: 'one_time', interval_count: 1, status: 'archived' }],
  },
]

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
    price_archived: false,
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

  it('formats priceLabel for four subscription tiers and one_time', () => {
    const sub = PLANS[0]!.prices
    expect(priceLabel(sub[0]!)).toBe('月付 · ¥15.00')
    expect(priceLabel(sub[1]!)).toBe('季付 · ¥40.00')
    expect(priceLabel(sub[2]!)).toBe('半年付 · ¥75.00')
    expect(priceLabel(sub[3]!)).toBe('年付 · ¥140.00')
    const ot = PLANS[2]!.prices[0]!
    expect(priceLabel(ot)).toBe('一次性 · ¥50.00')
    const custom = { id: 'c', unit_amount: 2000, billing_interval: 'month', interval_count: 2, status: 'active' as const }
    expect(priceLabel(custom)).toBe('每 2 月 · ¥20.00')
  })

  it('describes what a redemption granted', () => {
    expect(grantedLabel({ granted: { balance: 10000 }, template_name: '100 元卡' })).toBe('余额 +¥100.00')
    expect(grantedLabel({ granted: { traffic_bytes: 100 * GB, expire_days: 30 }, template_name: 'x' })).toBe('流量 +100 GB · +30 天')
    expect(grantedLabel({ granted: {}, template_name: '专业版月卡' })).toBe('专业版月卡')
    expect(grantedLabel({ granted: { balance: 500 }, template_name: '盲盒', prize_label: '¥5 余额' })).toBe('¥5 余额（余额 +¥5.00）')
  })

  it('shares one code filter between the list and the masked report export', () => {
    expect(codeFilterQuery('b1', '')).toEqual({ batch_id: 'b1' })
    expect(codeFilterQuery('b1', 'used')).toEqual({ batch_id: 'b1', status: 'used' })
  })

  it('names batches and export files from the id', () => {
    const b = { id: '3f9a1c2b-0000-4000-8000-000000000000', created_at: new Date(2026, 8, 23, 12).toISOString() }
    expect(batchLabel(b)).toBe('GB-0923-3F9A')
    expect(batchFileName(b.id)).toBe('gift-codes-3f9a1c2b.csv')
    expect(codesReportFileName(b.id)).toBe('gift-codes-report-3f9a1c2b.csv')
    expect(redeemRate(1287, 2040)).toBe('63.1%')
    expect(redeemRate(0, 0)).toBe('—')
  })

  it('builds template create request with only Go-known fields and without id', () => {
    const form = {
      ...emptyTemplateForm(),
      name: '通用卡',
      balance: '100.5',
      trafficGb: '50',
      days: '30',
      resetQuota: true,
      newUserOnly: true,
      maxUsePerUser: '2',
    }
    const built = buildTemplateRequest(form, PLANS)
    expect(built).toEqual({
      ok: true,
      body: {
        name: '通用卡',
        description: '',
        type: 'general',
        status: 'active',
        rewards: { balance: 10050, traffic_bytes: 50 * GB, expire_days: 30, reset_quota: true },
        conditions: { new_user_only: true },
        limits: { max_use_per_user: 2 },
        theme_color: '',
      },
    })
  })

  it('validates general card rewards and conditions', () => {
    const empty = buildTemplateRequest({ ...emptyTemplateForm(), name: '空卡' }, PLANS)
    expect(!empty.ok && empty.errors.rewards).toBe('通用卡至少要送一样东西：余额、流量、延长到期或重置流量')

    const both = buildTemplateRequest({ ...emptyTemplateForm(), name: 'x', balance: '1', newUserOnly: true, paidUserOnly: true }, PLANS)
    expect(!both.ok && Object.keys(both.errors)).toEqual(['conditions'])
  })

  it('validates plan card rules for subscription and one-time plans', () => {
    // 没选套餐
    const noPlan = buildTemplateRequest({ ...emptyTemplateForm(), name: 'x', type: 'plan', planId: '' }, PLANS)
    expect(!noPlan.ok && noPlan.errors.rewards).toBe('请选择套餐')

    // 订阅套餐未选价格
    const subNoPrice = buildTemplateRequest({ ...emptyTemplateForm(), name: 'x', type: 'plan', planId: 'p1', priceId: '' }, PLANS)
    expect(!subNoPrice.ok && subNoPrice.errors.rewards).toBe('订阅套餐要选一档周期价')

    // 订阅套餐选了已下架价格
    const subArchivedPrice = buildTemplateRequest({ ...emptyTemplateForm(), name: 'x', type: 'plan', planId: 'p1', priceId: 'pr-archived' }, PLANS)
    expect(!subArchivedPrice.ok && subArchivedPrice.errors.rewards).toBe('订阅套餐要选一档周期价')

    // 订阅套餐选在售价格正常构建
    const subOk = buildTemplateRequest({ ...emptyTemplateForm(), name: 'x', type: 'plan', planId: 'p1', priceId: 'pr-m1' }, PLANS)
    expect(subOk.ok && subOk.body.rewards).toEqual({ plan_id: 'p1', price_id: 'pr-m1' })

    // 一次性套餐自动取唯一在售价格
    const otOk = buildTemplateRequest({ ...emptyTemplateForm(), name: 'x', type: 'plan', planId: 'p-onetime', priceId: '' }, PLANS)
    expect(otOk.ok && otOk.body.rewards).toEqual({ plan_id: 'p-onetime', price_id: 'pr-ot' })

    // 一次性套餐没有在售价格
    const otNoPrice = buildTemplateRequest({ ...emptyTemplateForm(), name: 'x', type: 'plan', planId: 'p-ot-noprice' }, PLANS)
    expect(!otNoPrice.ok && otNoPrice.errors.rewards).toBe('这个一次性套餐没有在售价格')
  })

  it('validates mystery box pool rules', () => {
    const tooFew = buildTemplateRequest({ ...emptyTemplateForm(), name: 'x', type: 'mystery', pool: [{ label: 'a', weight: '1', balance: '1', trafficGb: '', days: '' }] }, PLANS)
    expect(!tooFew.ok && tooFew.errors.rewards).toBe('盲盒至少要有 2 个奖品')

    const emptyPrizeErr = buildTemplateRequest(
      {
        ...emptyTemplateForm(),
        name: 'x',
        type: 'mystery',
        pool: [
          { label: '¥5', weight: '80', balance: '5', trafficGb: '', days: '' },
          { label: '空奖', weight: '20', balance: '', trafficGb: '', days: '' },
        ],
      },
      PLANS,
    )
    expect(!emptyPrizeErr.ok && emptyPrizeErr.errors.rewards).toBe('第 2 个奖品什么都不送')

    const poolOk = buildTemplateRequest(
      {
        ...emptyTemplateForm(),
        name: 'x',
        type: 'mystery',
        pool: [
          { label: '¥5', weight: '80', balance: '5', trafficGb: '', days: '' },
          { label: '50G', weight: '20', balance: '', trafficGb: '50', days: '' },
        ],
      },
      PLANS,
    )
    expect(poolOk.ok && poolOk.body.rewards).toEqual({
      pool: [
        { label: '¥5', weight: 80, balance: 500 },
        { label: '50G', weight: 20, traffic_bytes: 50 * GB },
      ],
    })
  })

  it('templateStatusBody carries back existing rewards, conditions, and limits, only modifying status', () => {
    const t = tpl({
      id: 't-123',
      name: '原模板',
      description: '原说明',
      type: 'general',
      status: 'active',
      rewards: { balance: 2000, traffic_bytes: 10 * GB },
      conditions: { new_user_only: true, require_invite: true },
      limits: { max_use_per_user: 3, cooldown_hours: 24 },
      theme_color: '#ff0000',
    })
    const pausedBody = templateStatusBody(t, 'paused')
    expect(pausedBody).toEqual({
      id: 't-123',
      name: '原模板',
      description: '原说明',
      type: 'general',
      status: 'paused',
      rewards: { balance: 2000, traffic_bytes: 10 * GB },
      conditions: { new_user_only: true, require_invite: true },
      limits: { max_use_per_user: 3, cooldown_hours: 24 },
      theme_color: '#ff0000',
    })
  })

  it('limits count and prefix in code generation request', () => {
    expect(buildGenerateRequest({ count: '100', prefix: '', expiresAt: '' })).toEqual({ ok: true, body: { count: 100 } })
    expect(buildGenerateRequest({ count: '10', prefix: 'gc', expiresAt: '' })).toMatchObject({ ok: true, body: { count: 10, prefix: 'GC' } })
    const bad = buildGenerateRequest({ count: '5001', prefix: 'A-B', expiresAt: '2020-01-01T00:00' })
    expect(!bad.ok && bad.errors).toEqual({ count: '一次生成 1 到 5000 个', prefix: '只能用大写字母和数字，最多 8 位', expires_at: '有效期不能设在过去' })
  })

  it('tolerates a null template list', () => {
    expect(templatesResponse.parse({ templates: null }).templates).toEqual([])
  })
})
