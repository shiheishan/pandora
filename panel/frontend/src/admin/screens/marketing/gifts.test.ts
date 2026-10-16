import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { GB } from './logic'
import { batchFileName, batchLabel, buildGenerateRequest, buildTemplateRequest, codeFilterQuery, codesReportFileName, grantedLabel, redeemRate, templateFace, templateToForm } from './giftModel'
import { templatesResponse, type GiftTemplate, type Plan } from './schemas'

const PLANS: Plan[] = [
  { id: 'p1', name: '基础版', status: 'active', prices: [] },
  { id: 'p2', name: '专业版', status: 'active', prices: [] },
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
