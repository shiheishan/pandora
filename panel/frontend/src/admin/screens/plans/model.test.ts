/**
 * [INPUT]: 依赖 vitest，依赖 ./api 的 schema，依赖 ./model 的纯函数
 * [OUTPUT]: 无（测试文件）
 * [POS]: admin/screens/plans 的单元测试：schema 归一与封闭枚举、周期与金额、版本表单与 quotas 同步、向导两种提交体与校验、销售设置、新增价格、流量包；界面交互在浏览器里对 dev/mock-api 验收
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it } from 'vitest'
import { packSchema, planCreatedSchema, planDetailSchema, planRowSchema, versionRowSchema, type PlanDetail, type PriceRow, type VersionRow } from './api'
import {
  createBody,
  emptyPriceForm,
  emptyWizard,
  GiB,
  packBody,
  packForm,
  packProblems,
  parseAmount,
  periodLabel,
  planFacts,
  priceBody,
  priceFrom,
  priceProblems,
  publishBlockers,
  quotaLabel,
  removedCurrencies,
  salesBody,
  salesForm,
  salesProblems,
  stepOfField,
  updateBody,
  versionBody,
  versionForm,
  versionNote,
  versionProblems,
  versionSummary,
  versionView,
  wizardFromPlan,
  wizardProblems,
} from './model'

const price = (over: Partial<PriceRow> = {}): PriceRow => ({
  id: 'pr1',
  currency: 'CNY',
  unit_amount: 2900,
  billing_interval: 'month',
  interval_count: 1,
  trial_days: 0,
  status: 'active',
  user_group_id: null,
  valid_from: null,
  valid_until: null,
  row_version: 1,
  ...over,
})

const version = (over: Partial<VersionRow> = {}): VersionRow => ({
  id: 'v3',
  version: 3,
  status: 'published',
  frozen_at: '2026-08-01T12:00:00Z',
  row_version: 4,
  quota_reset_strategy: 'billing_cycle',
  quota_reset_day: null,
  grace_period_hours: 24,
  grace_keeps_service: true,
  renewal_extends_period: true,
  renewal_resets_quota: true,
  renewal_keeps_addons: false,
  max_devices: 3,
  max_concurrent: null,
  device_release_hours: 0,
  overage_policy: 'suspend',
  throttle_kbps: null,
  notes: null,
  entitlements: [{ code: 'streaming', value: true }],
  quotas: [
    { metric: 'traffic.bytes', limit: 200 * GiB, unit: 'bytes', period: 'cycle' },
    { metric: 'devices.active', limit: 3, unit: 'count', period: 'cycle' },
    { metric: 'api.calls', limit: 1000, unit: 'count', period: 'day' },
  ],
  pool_ids: ['pool-a'],
  created_by_email: 'ops@pandora.dev',
  created_at: '2026-07-30T08:00:00Z',
  ...over,
})

const plan = (over: Partial<PlanDetail> = {}): PlanDetail => ({
  id: 'plan1',
  product_id: 'prod1',
  current_version_id: 'v3',
  row_version: 7,
  code: 'std',
  name: '标准版',
  description: '日常浏览',
  status: 'active',
  visibility: 'public',
  visible_group_ids: [],
  visible_from: null,
  visible_until: null,
  allow_new_purchase: true,
  allow_renewal: true,
  allow_upgrade: true,
  purchase_limit_per_user: null,
  stock_total: null,
  stock_reserved: 2,
  sort_order: 10,
  versions: [version()],
  prices: [price(), price({ id: 'pr2', currency: 'USD', unit_amount: 450 }), price({ id: 'pr3', user_group_id: 'g1', unit_amount: 1900 }), price({ id: 'pr4', status: 'archived', unit_amount: 100 })],
  ...over,
})

describe('schema', () => {
  it('Go 的 nil 切片归一成 []；封闭枚举外的值判为不符约定', () => {
    const raw = { ...version(), entitlements: null, quotas: null, pool_ids: null }
    const v = versionRowSchema.parse(raw)
    expect([v.entitlements, v.quotas, v.pool_ids]).toEqual([[], [], []])
    expect(versionRowSchema.safeParse({ ...raw, overage_policy: 'block' }).success).toBe(false)
    expect(planDetailSchema.parse({ ...plan(), visible_group_ids: null, versions: null, prices: null }).versions).toEqual([])
    expect(planDetailSchema.safeParse({ ...plan(), visibility: 'friends' }).success).toBe(false)
  })

  it('列表行与向导响应（R65 plan 为完整详情）、流量包行', () => {
    const row = {
      id: 'p',
      product_id: 'x',
      row_version: 1,
      code: 'c',
      name: 'n',
      description: null,
      status: 'active',
      visibility: 'public',
      sort_order: 0,
      current_version_id: null,
      draft_version_id: null,
      version: null,
      max_devices: null,
      traffic_limit: null,
      prices: null,
      active_subscriptions: 0,
      node_count: 0,
    }
    expect(planRowSchema.parse(row).prices).toEqual([])
    expect(planCreatedSchema.parse({ plan: plan(), version_id: 'v', price_ids: null, published: true }).price_ids).toEqual([])
    const pack = {
      id: 'k',
      name: '100G',
      traffic_bytes: 100 * GiB,
      currency: 'CNY',
      unit_amount: 1500,
      recommended: true,
      status: 'active',
      sort_order: 0,
      sold_count: 3,
      created_at: 't',
      updated_at: 't',
    }
    expect(packSchema.safeParse(pack).success).toBe(true)
    expect(packSchema.safeParse({ ...pack, status: 'hidden' }).success).toBe(false)
  })
})

describe('文案', () => {
  it('周期：五档预设、季付 quarter×1、自定义', () => {
    expect(periodLabel('month', 1)).toBe('每月')
    expect(periodLabel('month', 3)).toBe('每季度')
    expect(periodLabel('quarter', 1)).toBe('每季度')
    expect(periodLabel('year', 1)).toBe('每年')
    expect(periodLabel('one_time', 1)).toBe('一次性')
    expect(periodLabel('week', 1)).toBe('每周')
    expect(periodLabel('day', 7)).toBe('每 7 天')
    expect(periodLabel('month', 12)).toBe('每 12 个月')
  })

  it('金额：元转分，最多两位小数', () => {
    expect(parseAmount('29')).toBe(2900)
    expect(parseAmount('¥4.5')).toBe(450)
    expect(parseAmount('0')).toBe(0)
    expect(parseAmount('1.234')).toBeNull()
    expect(parseAmount('-1')).toBeNull()
  })

  it('起价：在售、优先公开价与 CNY；额度文案', () => {
    expect(priceFrom(plan().prices)).toBe('¥29.00 起')
    expect(priceFrom([price({ currency: 'USD', unit_amount: 450 })])).toBe('$4.50 起')
    expect(priceFrom([price({ status: 'archived' })])).toBe('无价格')
    expect(quotaLabel(200 * GiB, 3)).toBe('200 GB · 3 设备')
    expect(quotaLabel(null, null)).toBe('不限流量 · 不限设备')
    expect(planFacts({ traffic_limit: null, max_devices: null, prices: [], version: null })).toEqual({ price: '无价格', quota: '未发布' })
  })

  it('版本：当前发布 / 历史 / 草稿与创建人', () => {
    expect(versionView(version(), 'v3').label).toBe('当前发布')
    expect(versionView(version(), 'v4').label).toBe('历史')
    expect(versionView(version({ status: 'draft' }), 'v3')).toEqual({ label: '草稿', tone: 'warn' })
    expect(versionNote(version({ status: 'draft', created_by_email: null }))).toMatch(/^草稿 · \d\d-\d\d$/)
    expect(versionNote(version())).toMatch(/^2026-08-01 发布$/)
    expect(versionSummary(version({ throttle_kbps: 50000, overage_policy: 'throttle' }))).toBe('200 GB / 周期 · 3 台设备 · 限速 50 Mbps')
  })

  it('发布前的提示：无价格、无节点池、仅邀请', () => {
    expect(publishBlockers(plan(), version())).toEqual([])
    expect(publishBlockers(plan({ prices: [], visibility: 'invite_only' }), version({ pool_ids: [] }))).toHaveLength(3)
  })
})

describe('版本表单', () => {
  it('改流量与设备只动 traffic.bytes / devices.active，其它 quotas 与 entitlements 原样回填', () => {
    const v = version()
    const f = { ...versionForm(v), gb: '600', devices: '5' }
    const body = versionBody(f, v, v.row_version)
    expect(body.quotas).toEqual([
      { metric: 'api.calls', limit: 1000, unit: 'count', period: 'day' },
      { metric: 'traffic.bytes', limit: 600 * GiB, unit: 'bytes', period: 'cycle' },
      { metric: 'devices.active', limit: 5, unit: 'count', period: 'cycle' },
    ])
    expect(body).toMatchObject({ expected_row_version: 4, max_devices: 5, entitlements: [{ code: 'streaming', value: true }], quota_reset_day: null, throttle_kbps: null })
    expect(body).not.toHaveProperty('pool_ids')
  })

  it('清空流量与设备 = 不限：去掉两条 quota；另存为新版本时乐观锁取目标草稿', () => {
    const v = version()
    const body = versionBody({ ...versionForm(v), gb: '', devices: '' }, v, 1)
    expect(body.quotas.map((q) => q.metric)).toEqual(['api.calls'])
    expect(body.max_devices).toBeNull()
    expect(body.expected_row_version).toBe(1)
  })

  it('D-C-5 第 ② 步（R99）改之前按后端校验：限速只配「用完限速」，该策略必须有速率', () => {
    const f = versionForm(version())
    expect(versionProblems({ ...f, mbps: '50' })).toHaveProperty('throttle_kbps')
    expect(versionProblems({ ...f, overage: 'throttle' })).toHaveProperty('throttle_kbps')
    expect(versionProblems({ ...f, overage: 'throttle', mbps: '50' })).toEqual({})
    expect(versionBody({ ...f, overage: 'throttle', mbps: '2.5' }, version(), 4).throttle_kbps).toBe(2500)
    expect(versionProblems({ ...f, strategy: 'fixed_day', resetDay: '31' })).toHaveProperty('quota_reset_day')
    expect(versionProblems({ ...f, devices: '0', gb: 'x' })).toMatchObject({ max_devices: expect.any(String), traffic: expect.any(String) })
  })
})

describe('向导', () => {
  it('新建：键名与后端一致，publish 时要价格与节点池', () => {
    const f = { ...emptyWizard(), name: '专业版', code: 'Pro' }
    const p = wizardProblems(f, 'new')
    expect(Object.keys(p).sort()).toEqual(['code', 'pool_ids', 'prices.0.unit_amount'])
    const ok = { ...f, code: 'pro', poolIds: ['a'], prices: [{ ...f.prices[0]!, amount: '59' }] }
    expect(wizardProblems(ok, 'new')).toEqual({})
    expect(wizardProblems({ ...ok, publish: false, poolIds: [], prices: [] }, 'new')).toEqual({})
    expect(wizardProblems({ ...ok, visibility: 'invite_only' }, 'new')).toHaveProperty('visibility')
    const dup = { ...ok, prices: [...ok.prices, { ...ok.prices[0]!, key: 'x' }] }
    expect(wizardProblems(dup, 'new')).toHaveProperty(['prices.1.billing_interval'])
  })

  it('新建提交体：留空 = 不限发 null（设备 0 会被版本校验拒绝）；固定日才带 quota_reset_day', () => {
    const f = { ...emptyWizard(), name: '专业版', code: 'pro', poolIds: ['a'], prices: [{ ...emptyWizard().prices[0]!, amount: '59.9', period: 'month:3', trialDays: '3' }] }
    const body = createBody(f)
    expect(body).toMatchObject({ traffic_gb: null, max_devices: null, visible_group_ids: [], publish: true, description: null })
    expect(body.prices).toEqual([{ billing_interval: 'month', interval_count: 3, unit_amount: 5990, currency: 'CNY', trial_days: 3 }])
    expect(body).not.toHaveProperty('quota_reset_day')
    expect(createBody({ ...f, strategy: 'fixed_day', resetDay: '15', gb: '200', devices: '3' })).toMatchObject({ quota_reset_day: 15, traffic_gb: 200, max_devices: 3 })
  })

  it('编辑：初值取当前版本与全部在售公开价；没改的额度 / 价格 / 线路发 null', () => {
    const p = plan()
    const orig = wizardFromPlan(p)
    expect(orig).toMatchObject({ gb: '200', devices: '3', poolIds: ['pool-a'], publish: true })
    expect(orig.prices.map((x) => [x.currency, x.amount, x.period])).toEqual([
      ['CNY', '29', 'month:1'],
      ['USD', '4.50', 'month:1'],
    ])
    const same = updateBody(orig, orig, p.row_version)
    expect(same).toMatchObject({ expected_row_version: 7, traffic_gb: null, max_devices: null, prices: null, pool_ids: null, code: 'std', sort_order: 10 })
    const changed = updateBody({ ...orig, gb: '', poolIds: ['pool-a', 'pool-b'], prices: orig.prices.slice(0, 1) }, orig, 7)
    expect(changed.traffic_gb).toBe(0)
    expect(changed.pool_ids).toEqual(['pool-a', 'pool-b'])
    expect(changed.prices).toHaveLength(1)
  })

  it('编辑：设备数不能改回不限；删光一个币种要提示去价格卡归档', () => {
    const orig = wizardFromPlan(plan())
    expect(wizardProblems({ ...orig, devices: '' }, 'edit', orig)).toHaveProperty('max_devices')
    expect(wizardProblems(orig, 'edit', orig)).toEqual({})
    expect(removedCurrencies({ ...orig, prices: orig.prices.filter((x) => x.currency === 'CNY') }, orig)).toEqual(['USD'])
  })

  it('fields 键落到哪一步', () => {
    expect(stepOfField('code')).toBe(1)
    expect(stepOfField('visible_group_ids')).toBe(1)
    expect(stepOfField('traffic_gb')).toBe(2)
    expect(stepOfField('prices.2.unit_amount')).toBe(3)
    expect(stepOfField('prices')).toBe(3)
    expect(stepOfField('pool_ids')).toBe(4)
    expect(stepOfField('stock_total')).toBe(5)
    expect(stepOfField('row_version')).toBeNull()
  })
})

describe('销售设置与新增价格', () => {
  it('整体覆盖：回填 code / name / description，非分组可见清空用户组；库存不低于已预留', () => {
    const p = plan({ visibility: 'group', visible_group_ids: ['g1'] })
    const f = salesForm(p)
    expect(salesProblems({ ...f, stockTotal: '1' }, p.stock_reserved)).toHaveProperty('stock_total')
    expect(salesProblems({ ...f, groupIds: [] }, 0)).toHaveProperty('visible_group_ids')
    expect(salesProblems({ ...f, from: '2026-10-02T00:00', until: '2026-10-01T00:00' }, 0)).toHaveProperty('visible_until')
    const body = salesBody({ ...f, visibility: 'public', allowNew: false }, p)
    expect(body).toMatchObject({ expected_row_version: 7, code: 'std', name: '标准版', description: '日常浏览', visible_group_ids: [], allow_new_purchase: false, visible_from: null })
  })

  it('新增价格：预设周期、自定义周期与用户组价', () => {
    const f = { ...emptyPriceForm(), amount: '8.9', currency: 'USD' as const }
    expect(priceBody(f)).toMatchObject({ currency: 'USD', unit_amount: 890, billing_interval: 'month', interval_count: 1, user_group_id: null })
    const custom = { ...f, period: 'custom', customCount: '7', customUnit: 'day' as const, groupId: 'g1', trialDays: '3' }
    expect(priceBody(custom)).toMatchObject({ billing_interval: 'day', interval_count: 7, user_group_id: 'g1', trial_days: 3 })
    expect(priceProblems({ ...custom, customCount: '0', amount: '' })).toMatchObject({ interval_count: expect.any(String), unit_amount: expect.any(String) })
  })
})

describe('流量包', () => {
  it('GB ↔ 字节、元 ↔ 分往返；与后端同规则校验', () => {
    const pack = {
      id: 'k',
      name: '加油包',
      traffic_bytes: 100 * GiB,
      currency: 'CNY' as const,
      unit_amount: 1500,
      recommended: true,
      status: 'active' as const,
      sort_order: 2,
      sold_count: 0,
      created_at: 't',
      updated_at: 't',
    }
    const f = packForm(pack)
    expect(f).toMatchObject({ gb: '100', amount: '15', recommended: true, sortOrder: '2' })
    expect(packBody(f)).toEqual({ name: '加油包', traffic_bytes: 100 * GiB, currency: 'CNY', unit_amount: 1500, recommended: true, sort_order: 2 })
    expect(packBody({ ...f, gb: '0.5' }).traffic_bytes).toBe(GiB / 2)
    expect(Object.keys(packProblems({ ...packForm(), sortOrder: '2000000' })).sort()).toEqual(['name', 'sort_order', 'traffic_bytes', 'unit_amount'])
    expect(packProblems(f)).toEqual({})
  })
})
