import { describe, expect, it } from 'vitest'
import type { PlanDetail, PlanRow, PriceRow, VersionRow } from './schemas'
import {
  ARCHIVE_BODY,
  GiB,
  KIND_LABELS,
  STATUS_FILTER_OPTIONS,
  TIERS,
  activePrices,
  activeTierPrice,
  autoCode,
  basicFacts,
  basicBody,
  basicForm,
  basicProblems,
  bytesToGb,
  codeHint,
  createBody,
  createFieldKey,
  createProblems,
  createdToast,
  currentVersion,
  detailTabs,
  draftVersion,
  emptyCreate,
  filterPlans,
  formatTraffic,
  isDetailTab,
  isLive,
  isKindFilter,
  isStatusFilter,
  kbpsToMbps,
  kindCounts,
  onceBody,
  onceForm,
  onceProblems,
  overviewGroups,
  overviewNotes,
  parseAmount,
  parseOptionalInt,
  periodLabel,
  perMonthNote,
  planListView,
  planSubtitle,
  poolBindingWarning,
  poolMeta,
  poolsBody,
  priceListRows,
  publishCopy,
  resetBody,
  resetForm,
  resetProblems,
  rowVersionBody,
  salesBody,
  salesForm,
  salesProblems,
  throttleLabel,
  tierHint,
  tierOf,
  tierPriceBody,
  togglePool,
  usageBody,
  usageForm,
  usageProblems,
  versionListRows,
} from './model'

// ---------------------------------------------------------------------------
// 夹具：虚构数据，给齐 PlanRow / PlanDetail 全部字段
// ---------------------------------------------------------------------------
const price = (over: Partial<PriceRow> = {}): PriceRow => ({
  id: 'p1',
  billing_interval: 'month',
  interval_count: 1,
  unit_amount: 2500,
  validity_days: null,
  status: 'active',
  created_at: '2026-01-02T12:00:00',
  ...over,
})

const version = (over: Partial<VersionRow> = {}): VersionRow => ({
  id: 'v1',
  version: 1,
  status: 'published',
  published_at: '2026-01-03T12:00:00',
  traffic_bytes: 10 * GiB,
  max_devices: 3,
  throttle_kbps: null,
  created_by_email: 'admin@example.com',
  created_at: '2026-01-02T12:00:00',
  ...over,
})

const planRow = (over: Partial<PlanRow> = {}): PlanRow => ({
  id: 'plan-1',
  row_version: 7,
  code: 'standard',
  name: '标准套餐',
  description: null,
  kind: 'subscription',
  status: 'active',
  visibility: 'public',
  recommended: false,
  prices: [price()],
  traffic_bytes: 10 * GiB,
  max_devices: 3,
  throttle_kbps: null,
  version: 1,
  draft_version: null,
  draft_version_id: null,
  pool_ids: ['pool-a'],
  reset: null,
  active_subscriptions: 12,
  node_count: 4,
  ...over,
})

const planDetail = (over: Partial<PlanDetail> = {}): PlanDetail => ({
  ...planRow(),
  visible_group_ids: [],
  allow_new_purchase: true,
  allow_renewal: true,
  allow_upgrade: true,
  versions: [version()],
  ...over,
})

// ---------------------------------------------------------------------------
// 常量与文案
// ---------------------------------------------------------------------------
describe('constants', () => {
  it('exposes kind labels', () => {
    expect(KIND_LABELS.subscription).toBe('订阅套餐')
    expect(KIND_LABELS.one_time).toBe('一次性套餐')
  })

  it('lists status filter options', () => {
    expect(STATUS_FILTER_OPTIONS.map((o) => o.value)).toEqual(['all', 'active', 'draft', 'archived'])
  })

  it('keeps the four tiers in display order', () => {
    expect(TIERS.map((t) => t.key)).toEqual(['month:1', 'month:3', 'month:6', 'year:1'])
    expect(tierOf('month', 3)).toBe('month:3')
    expect(tierOf('year', 1)).toBe('year:1')
    expect(tierOf('one_time', 1)).toBeNull()
  })

  it('labels periods', () => {
    expect(periodLabel('one_time', 1)).toBe('一次性')
    expect(periodLabel('month', 1)).toBe('每月')
    expect(periodLabel('month', 3)).toBe('每季度')
    expect(periodLabel('month', 6)).toBe('每半年')
    expect(periodLabel('year', 1)).toBe('每年')
    expect(periodLabel('day', 1)).toBe('每天')
    expect(periodLabel('week', 2)).toBe('每 2 周')
    expect(periodLabel('month', 5)).toBe('每 5 个月')
    expect(periodLabel('quarter', 1)).toBe('每个季度')
  })
})

// ---------------------------------------------------------------------------
// 输入解析
// ---------------------------------------------------------------------------
describe('parsing', () => {
  it('parses amounts into cents', () => {
    expect(parseAmount('¥25.5')).toBe(2550)
    expect(parseAmount('0')).toBe(0)
    expect(parseAmount('-1')).toBeNull()
    expect(parseAmount('1.234')).toBeNull()
    expect(parseAmount('')).toBeNull()
  })

  it('parses optional ints', () => {
    expect(parseOptionalInt('  ', 100)).toBeNull()
    expect(parseOptionalInt('5', 100)).toBe(5)
    expect(parseOptionalInt('0', 100)).toBe('bad')
    expect(parseOptionalInt('101', 100)).toBe('bad')
    expect(parseOptionalInt('x', 100)).toBe('bad')
  })

  it('formats traffic and throttle', () => {
    expect(formatTraffic(null)).toBe('不限')
    expect(formatTraffic(2 * GiB)).toBe('2.00 GB')
    expect(kbpsToMbps(null)).toBe('')
    expect(kbpsToMbps(1500)).toBe('1.5')
    expect(throttleLabel(null)).toBe('不限速')
    expect(throttleLabel(2000)).toBe('2 Mbps')
  })

  it('converts bytes to gb text', () => {
    expect(bytesToGb(null)).toBe('')
    expect(bytesToGb(5 * GiB)).toBe('5')
    expect(bytesToGb(1.5 * GiB)).toBe('1.5')
  })
})

// ---------------------------------------------------------------------------
// 列表
// ---------------------------------------------------------------------------
describe('list filters', () => {
  it('narrows filter values', () => {
    expect(isKindFilter('one_time')).toBe(true)
    expect(isKindFilter('nope')).toBe(false)
    expect(isStatusFilter('draft')).toBe(true)
    expect(isStatusFilter(null)).toBe(false)
  })

  it('filters by kind, status and query', () => {
    const rows = [
      planRow({ id: 'a', name: '标准 Standard', kind: 'subscription', status: 'active' }),
      planRow({ id: 'b', code: 'once-30', kind: 'one_time', status: 'draft' }),
    ]
    expect(filterPlans(rows, { kind: 'all', status: 'all', q: '' })).toHaveLength(2)
    expect(filterPlans(rows, { kind: 'one_time', status: 'all', q: '' }).map((r) => r.id)).toEqual(['b'])
    expect(filterPlans(rows, { kind: 'all', status: 'active', q: '' }).map((r) => r.id)).toEqual(['a'])
    expect(filterPlans(rows, { kind: 'all', status: 'all', q: 'standard' }).map((r) => r.id)).toEqual(['a'])
    expect(filterPlans(rows, { kind: 'all', status: 'all', q: 'ONCE' }).map((r) => r.id)).toEqual(['b'])
  })

  it('counts kinds by status only', () => {
    const rows = [
      planRow({ id: 'a', kind: 'subscription', status: 'active' }),
      planRow({ id: 'b', kind: 'subscription', status: 'draft' }),
      planRow({ id: 'c', kind: 'one_time', status: 'active' }),
    ]
    expect(kindCounts(rows, 'active')).toEqual({ all: 2, subscription: 1, one_time: 1 })
    expect(kindCounts(rows, 'all')).toEqual({ all: 3, subscription: 2, one_time: 1 })
  })
})

describe('activePrices', () => {
  it('sorts live prices by tier with one_time last, archived dropped', () => {
    const live = activePrices([
      price({ id: 'once', billing_interval: 'one_time', interval_count: 1 }),
      price({ id: 'y', billing_interval: 'year', interval_count: 1 }),
      price({ id: 'arch', status: 'archived' }),
      price({ id: 'm1' }),
    ])
    expect(live.map((p) => p.id)).toEqual(['m1', 'y', 'once'])
  })
})

describe('planListView', () => {
  it('shows the first tier plus a hint for multi-tier subscription', () => {
    const v = planListView(planRow({ prices: [price({ id: 'm1' }), price({ id: 'm3', interval_count: 3, unit_amount: 7000 })] }))
    expect(v.kind).toBe('订阅套餐')
    expect(v.price).toBe('¥25.00 / 月')
    expect(v.priceSub).toBe('另有 1 个周期')
    expect(v.quota).toBe('10.0 GB / 周期')
    expect(v.quotaSub).toBe('3 台设备')
    expect(v.lines).toBe('1 个节点池')
    expect(v.linesWarn).toBe(false)
    expect(v.subs).toBe('12')
  })

  it('shows one-time price with validity', () => {
    const forever = planListView(
      planRow({ kind: 'one_time', prices: [price({ id: 'o', billing_interval: 'one_time', unit_amount: 5000 })] }),
    )
    expect(forever.price).toBe('¥50.00')
    expect(forever.priceSub).toBe('一次性 · 永久有效')
    expect(forever.quota).toBe('10.0 GB 总量')
    const days = planListView(
      planRow({ kind: 'one_time', prices: [price({ id: 'o', billing_interval: 'one_time', unit_amount: 5000, validity_days: 30 })] }),
    )
    expect(days.priceSub).toBe('一次性 · 有效 30 天')
  })

  it('falls back to 未定价 and 不限流量', () => {
    const v = planListView(planRow({ prices: [], traffic_bytes: null, max_devices: null }))
    expect(v.price).toBe('未定价')
    expect(v.priceSub).toBe('')
    expect(v.quota).toBe('不限流量')
    expect(v.quotaSub).toBe('设备不限')
  })

  it('marks plans that sell traffic resets', () => {
    expect(planListView(planRow({ reset: { price: 1000, limit: 2 } })).quotaSub).toBe('3 台设备 · 可重置')
  })

  it('does not warn on archived plans', () => {
    const v = planListView(planRow({ status: 'archived', pool_ids: [], node_count: 0 }))
    expect(v.lines).toBe('未绑定')
    expect(v.linesWarn).toBe(false)
  })

  it('warns when nothing is bound or no node is deliverable', () => {
    expect(planListView(planRow({ pool_ids: [] })).linesWarn).toBe(true)
    expect(planListView(planRow({ node_count: 0 })).linesWarn).toBe(true)
  })
})

// ---------------------------------------------------------------------------
// 详情
// ---------------------------------------------------------------------------
describe('detail shell', () => {
  it('narrows tab values and omits prices tab for one-time plans', () => {
    expect(isDetailTab('prices')).toBe(true)
    expect(isDetailTab('nope')).toBe(false)
    expect(isDetailTab(undefined)).toBe(false)
    expect(detailTabs(planDetail()).map((t) => t.value)).toEqual(['overview', 'prices', 'pools', 'versions'])
    const once = detailTabs(planDetail({ kind: 'one_time', versions: [version(), version({ id: 'v2', version: 2, status: 'draft' })] }))
    expect(once.map((t) => t.value)).toEqual(['overview', 'pools', 'versions'])
  })

  it('builds subtitle and liveness', () => {
    expect(planSubtitle(planDetail())).toBe('订阅套餐 · 12 个有效订阅')
    expect(isLive(planDetail())).toBe(true)
    expect(isLive(planDetail({ status: 'archived' }))).toBe(false)
    expect(draftVersion(planDetail())).toBeUndefined()
    const p = planDetail({ versions: [version({ id: 'v1' }), version({ id: 'v2', version: 2, status: 'draft', published_at: null })] })
    expect(draftVersion(p)?.id).toBe('v2')
    expect(currentVersion(p)?.id).toBe('v1')
    expect(currentVersion(planDetail({ version: 2, versions: p.versions }))).toBeUndefined()
  })
})

describe('overviewNotes', () => {
  it('describes archived plans', () => {
    const notes = overviewNotes(planDetail({ status: 'archived', pool_ids: [], allow_new_purchase: false }))
    expect(notes).toEqual([{ text: '已归档：用户端不再展示、不能新购，老用户不能续费，已有订阅到期后结束。' }])
  })

  it('warns about empty lines and no deliverable nodes', () => {
    expect(overviewNotes(planDetail({ pool_ids: [] }))[0]!.text).toContain('还没有绑定线路')
    expect(overviewNotes(planDetail({ pool_ids: [] }))[0]!.action).toEqual({ label: '去线路', tab: 'pools' })
    expect(overviewNotes(planDetail({ node_count: 0 }))[0]!.text).toContain('没有可下发的节点')
    expect(overviewNotes(planDetail({ node_count: 0 }))[0]!.action).toEqual({ label: '去线路', tab: 'pools' })
  })

  it('explains closed new purchase per kind', () => {
    expect(overviewNotes(planDetail({ allow_new_purchase: false }))[0]!.text).toContain('已有订阅照常续费')
    expect(overviewNotes(planDetail({ kind: 'one_time', allow_new_purchase: false }))[0]!.text).toContain('已买的照常用到期')
  })

  it('explains draft versions', () => {
    const draft = version({ id: 'v2', version: 2, status: 'draft', published_at: null })
    const p = planDetail({ status: 'draft', versions: [draft], version: null })
    expect(overviewNotes(p).map((n) => n.text)).toContain('还没上架：发布草稿 v2 后开始售卖。')
    expect(overviewNotes(p)[0]!.action).toEqual({ label: '去发布', tab: 'versions' })
    const live = planDetail({ versions: [version(), draft], draft_version: 2 })
    expect(overviewNotes(live).map((n) => n.text)).toContain('有一个未发布的草稿 v2：发布后新购使用它，老用户下个周期切换。')
    expect(overviewNotes(live).find((n) => n.text.includes('未发布的草稿'))!.action).toEqual({ label: '去发布', tab: 'versions' })
    expect(overviewNotes(planDetail({ kind: 'one_time', versions: [version(), draft], draft_version: 2 })).map((n) => n.text)).toContain(
      '有一个未发布的草稿 v2：发布后新购使用它，已买的不受影响。',
    )
  })
})

describe('overviewGroups', () => {
  it('renders usage, reset off, sales for a subscription', () => {
    const groups = overviewGroups(planDetail({ description: null, reset: null }))
    expect(groups.map((g) => g.key)).toEqual(['usage', 'reset', 'sales'])
    expect(groups[0]!.rows[0]).toEqual({ k: '每周期流量', v: '10.0 GB' })
    expect(groups[0]!.rows[3]).toEqual({ k: '版本', v: 'v1' })
    expect(groups[1]!.rows).toEqual([{ k: '状态', v: '未开启', muted: true }])
    expect(groups[2]!.rows).toEqual([
      { k: '可见范围', v: '所有人可见' },
      { k: '新购', v: '允许' },
      { k: '续费', v: '允许' },
      { k: '从其他套餐变更', v: '允许' },
      { k: '推荐标记', v: '无' },
    ])
  })

  it('renders reset on, group visibility and draft version note', () => {
    const p = planDetail({
      reset: { price: 300, limit: 2 },
      visibility: 'group',
      visible_group_ids: ['g1', 'g2'],
      draft_version: 2,
      versions: [version(), version({ id: 'v2', version: 2, status: 'draft', published_at: null })],
    })
    const groups = overviewGroups(p)
    const reset = groups.find((g) => g.key === 'reset')!
    expect(reset.rows).toEqual([
      { k: '状态', v: '允许用户付费重置' },
      { k: '每次价格', v: '¥3.00', mono: true },
      { k: '次数限制', v: '每周期最多 2 次' },
    ])
    const sales = groups.find((g) => g.key === 'sales')!
    expect(sales.rows[0]).toEqual({ k: '可见范围', v: '指定用户组可见 · 2 个用户组' })
    expect(groups[0]!.rows[3]).toEqual({ k: '版本', v: 'v1 · 草稿 v2 待发布' })
  })

  it('renders the one-time price group last without renewal row in sales', () => {
    const p = planDetail({ kind: 'one_time', allow_renewal: false, prices: [price({ id: 'o', billing_interval: 'one_time', unit_amount: 9900, validity_days: 90 })] })
    const groups = overviewGroups(p)
    expect(groups.find((g) => g.key === 'sales')!.rows.map((r) => r.k)).toEqual(['可见范围', '新购', '从其他套餐变更', '推荐标记'])
    expect(groups[groups.length - 1]).toEqual({
      key: 'once',
      title: '价格与有效期',
      rows: [
        { k: '价格', v: '¥99.00', mono: true },
        { k: '有效期', v: '90 天' },
        { k: '到期后', v: '不能续费，订阅结束' },
      ],
    })
    const forever = overviewGroups(planDetail({ kind: 'one_time', prices: [price({ id: 'o', billing_interval: 'one_time', unit_amount: 9900 })] }))
    expect(forever[forever.length - 1]!.rows[1]).toEqual({ k: '有效期', v: '永久' })
  })
})

describe('basicFacts', () => {
  it('subscription with description', () => {
    expect(basicFacts(planDetail({ description: '说明文字' }))).toEqual([
      { k: '名称', v: '标准套餐' },
      { k: '代码', v: 'standard', mono: true },
      { k: '说明', v: '说明文字' },
      { k: '类型', v: '订阅套餐', hint: '按时间算，每周期清零' },
    ])
  })
  it('one-time without description', () => {
    expect(basicFacts(planDetail({ kind: 'one_time', description: null }))).toEqual([
      { k: '名称', v: '标准套餐' },
      { k: '代码', v: 'standard', mono: true },
      { k: '说明', v: '未填写', muted: true },
      { k: '类型', v: '一次性套餐', hint: '按流量算，用完即止' },
    ])
  })
})

describe('price lists', () => {
  it('lists live prices first by tier then archived by created_at desc', () => {
    const p = planDetail({
      prices: [
        price({ id: 'a1', status: 'archived', created_at: '2026-01-01T00:00:00' }),
        price({ id: 'y', billing_interval: 'year', interval_count: 1, unit_amount: 25800 }),
        price({ id: 'a2', status: 'archived', created_at: '2026-02-01T00:00:00' }),
      ],
    })
    expect(priceListRows(p).map((r) => r.id)).toEqual(['y', 'a2', 'a1'])
    expect(priceListRows(p)[0]).toEqual({ id: 'y', label: '每年', amount: '¥258.00', active: true, tier: 'year:1', note: '折合每月 ¥21.50' })
  })

  it('computes perMonthNote', () => {
    expect(perMonthNote(45000, 12, 4500)).toBe('折合每月 ¥37.50，省 17%')
    expect(perMonthNote(25800, 12, null)).toBe('折合每月 ¥21.50')
    expect(perMonthNote(4500, 1, 4500)).toBe('')
    expect(perMonthNote(30000, 12, 2500)).toBe('折合每月 ¥25.00')
    expect(perMonthNote(0, 12, 2500)).toBe('')
  })

  it('finds the active tier price', () => {
    const p = planDetail({ prices: [price({ id: 'y', billing_interval: 'year', interval_count: 1, unit_amount: 25800 })] })
    expect(activeTierPrice(p, 'year:1')?.id).toBe('y')
    expect(activeTierPrice(p, 'month:1')).toBeUndefined()
  })

  it('computes the per-month hint', () => {
    expect(tierHint('258', 'year:1', '25')).toBe('折合 ¥21.50 / 月，省 14%')
    expect(tierHint('258', 'year:1', '258')).toBe('折合 ¥21.50 / 月，省 92%')
    expect(tierHint('258', 'year:1', '')).toBe('折合 ¥21.50 / 月')
    expect(tierHint('300', 'year:1', '25')).toBe('折合 ¥25.00 / 月')
    expect(tierHint('', 'year:1', '25')).toBe('')
    expect(tierHint('25', 'month:1', '25')).toBe('')
  })
})

describe('pools', () => {
  it('toggles pool ids', () => {
    expect(togglePool(['a', 'b'], 'c')).toEqual(['a', 'b', 'c'])
    expect(togglePool(['a', 'b'], 'a')).toEqual(['b'])
  })

  it('describes pool capacity', () => {
    expect(poolMeta({ active_nodes: 10, deliverable_nodes: 4 })).toBe('4 / 10 个节点可下发')
  })

  it('warns on bindings without deliverable nodes', () => {
    const pools = [
      { id: 'a', deliverable_nodes: 0 },
      { id: 'b', deliverable_nodes: 2 },
    ]
    expect(poolBindingWarning(pools, [])).toContain('0 个节点')
    expect(poolBindingWarning(pools, ['a'])).toContain('没有可下发的节点')
    expect(poolBindingWarning(pools, ['a', 'b'])).toBeNull()
  })
})

describe('version list and copies', () => {
  it('labels draft, current and archived versions', () => {
    const p = planDetail({
      version: 2,
      versions: [
        version({ id: 'v3', version: 3, status: 'draft', published_at: null, created_at: '2026-03-01T08:00:00', traffic_bytes: null, max_devices: null, throttle_kbps: 2000 }),
        version({ id: 'v2', version: 2, status: 'published', published_at: '2026-02-05T10:00:00' }),
        version({ id: 'v1', version: 1, status: 'published', published_at: null, created_at: '2026-01-02T12:00:00' }),
      ],
    })
    const rows = versionListRows(p)
    expect(rows.map((r) => r.label)).toEqual(['草稿', '当前', '历史'])
    expect(rows.map((r) => r.tone)).toEqual(['warn', 'ok', 'neutral'])
    expect(rows[0]).toMatchObject({ at: '2026-03-01', summary: '不限流量 · 设备不限 · 限速 2 Mbps', isDraft: true, by: 'admin@example.com' })
    expect(rows[2]!.at).toBe('2026-01-02')
    expect(rows[1]!.summary).toBe('10.0 GB / 周期 · 3 台设备')
  })

  it('builds publish copy', () => {
    expect(publishCopy(planDetail(), 2)).toEqual({ title: '发布 v2？', body: '发布后新购使用这个版本，订阅老用户在下个周期切换。已发布的版本不能再改。' })
    const once = publishCopy(planDetail({ kind: 'one_time' }), 1)
    expect(once.body).toBe('发布后新购使用这个版本，已买的老用户不受影响。已发布的版本不能再改。')
    const first = publishCopy(planDetail({ status: 'draft', version: null }), 1)
    expect(first.body.startsWith('首个版本发布后套餐开始售卖。')).toBe(true)
    expect(ARCHIVE_BODY).toContain('归档后不能恢复')
  })
})

// ---------------------------------------------------------------------------
// 表单 + 校验 + 请求体
// ---------------------------------------------------------------------------
describe('basic', () => {
  it('round-trips the form', () => {
    const p = planDetail({ description: '说明' })
    expect(basicForm(p)).toEqual({ name: '标准套餐', description: '说明' })
    expect(basicProblems(basicForm(p))).toEqual({})
    expect(basicBody({ name: '  新名  ', description: '  ' }, 7)).toEqual({ expected_row_version: 7, name: '新名', description: null })
  })

  it('validates name and description lengths', () => {
    expect(basicProblems({ name: '', description: '' })).toEqual({ name: '请填写名称，最多 120 个字' })
    expect(basicProblems({ name: 'x'.repeat(121), description: '' })).toEqual({ name: '请填写名称，最多 120 个字' })
    expect(basicProblems({ name: 'ok', description: 'x'.repeat(2001) })).toEqual({ description: '最多 2000 个字' })
  })
})

describe('usage', () => {
  it('takes draft values when a draft exists', () => {
    const p = planDetail({ versions: [version({ id: 'v2', version: 2, status: 'draft', published_at: null, traffic_bytes: 5 * GiB, max_devices: 9, throttle_kbps: 1024 })] })
    expect(usageForm(p)).toEqual({ gb: '5', devices: '9', mbps: '1.024' })
    expect(usageForm(planDetail())).toEqual({ gb: '10', devices: '3', mbps: '' })
  })

  it('validates with backend field keys', () => {
    expect(usageProblems({ gb: 'abc', devices: '0', mbps: '' })).toEqual({
      traffic_bytes: '填正整数，或留空为不限',
      max_devices: '填正整数，或留空为不限',
    })
    expect(usageProblems({ gb: '', devices: '', mbps: 'x' })).toEqual({ throttle_kbps: '填正整数，或留空为不限速' })
    expect(usageProblems({ gb: '1048577', devices: '', mbps: '' })).toEqual({ traffic_bytes: '填正整数，或留空为不限' })
    expect(usageProblems({ gb: '100', devices: '5', mbps: '50' })).toEqual({})
  })

  it('builds the body with nulls for unlimited', () => {
    expect(usageBody({ gb: '5', devices: '3', mbps: '20' }, 9)).toEqual({
      expected_row_version: 9,
      traffic_bytes: 5 * GiB,
      max_devices: 3,
      throttle_kbps: 20000,
    })
    expect(usageBody({ gb: '', devices: '', mbps: '' }, 9)).toEqual({
      expected_row_version: 9,
      traffic_bytes: null,
      max_devices: null,
      throttle_kbps: null,
    })
  })
})

describe('sales', () => {
  it('requires a group when visibility is group', () => {
    const f = salesForm(planDetail({ visibility: 'group', visible_group_ids: [] }))
    expect(salesProblems(f)).toEqual({ visible_group_ids: '指定用户组可见时至少选一个用户组' })
    expect(salesProblems({ ...f, groupIds: ['g1'] })).toEqual({})
  })

  it('sends allow_renewal false for one-time plans', () => {
    const f = { visibility: 'public' as const, groupIds: [], allowNew: true, allowRenewal: true, allowUpgrade: true, recommended: false }
    expect(salesBody(f, 'subscription', 3)).toMatchObject({ expected_row_version: 3, allow_renewal: true, visible_group_ids: [] })
    expect(salesBody(f, 'one_time', 3)).toMatchObject({ expected_row_version: 3, allow_renewal: false })
    const g = { ...f, visibility: 'group' as const, groupIds: ['g1'] }
    expect(salesBody(g, 'subscription', 3)).toMatchObject({ visible_group_ids: ['g1'] })
  })
})

describe('reset', () => {
  it('initializes from the plan', () => {
    expect(resetForm(planDetail({ reset: null }))).toEqual({ on: false, price: '', limit: '' })
    expect(resetForm(planDetail({ reset: { price: 2500, limit: null } }))).toEqual({ on: true, price: '25', limit: '' })
    expect(resetForm(planDetail({ reset: { price: 2550, limit: 3 } }))).toEqual({ on: true, price: '25.5', limit: '3' })
  })

  it('validates only when on', () => {
    expect(resetProblems({ on: false, price: '', limit: 'x' })).toEqual({})
    // 次数留空为不限
    expect(resetProblems({ on: true, price: '', limit: '' })).toEqual({
      'reset.price': '填每次价格，大于 0、最多两位小数',
    })
    expect(resetProblems({ on: true, price: '0', limit: '1001' })).toEqual({
      'reset.price': '填每次价格，大于 0、最多两位小数',
      'reset.limit': '填 1–1000 的整数，或留空为不限',
    })
    expect(resetProblems({ on: true, price: '2.5', limit: '10' })).toEqual({})
  })

  it('builds the body', () => {
    expect(resetBody({ on: true, price: '2.5', limit: '' }, 4)).toEqual({ expected_row_version: 4, enabled: true, price: 250, limit: null })
    expect(resetBody({ on: false, price: '', limit: '' }, 4)).toEqual({ expected_row_version: 4, enabled: false, price: 0, limit: null })
  })
})

describe('once price', () => {
  it('initializes from the active one-time price', () => {
    expect(onceForm(planDetail({ prices: [] }))).toEqual({ amount: '', days: '' })
    expect(onceForm(planDetail({ prices: [price({ id: 'o', billing_interval: 'one_time', unit_amount: 9900, validity_days: 90 })] }))).toEqual({ amount: '99', days: '90' })
  })

  it('validates amount and days', () => {
    expect(onceProblems({ amount: 'x', days: '40000' })).toEqual({
      unit_amount: '填价格，最多两位小数',
      validity_days: '填 1–36500 的天数，或留空为永久',
    })
    expect(onceProblems({ amount: '9.9', days: '' })).toEqual({})
  })

  it('builds the body', () => {
    expect(onceBody({ amount: '9.9', days: '' }, 2)).toEqual({ expected_row_version: 2, unit_amount: 990, validity_days: null })
    expect(onceBody({ amount: '9.9', days: '30' }, 2)).toEqual({ expected_row_version: 2, unit_amount: 990, validity_days: 30 })
  })
})

describe('tier price and simple bodies', () => {
  it('sends one tier change', () => {
    expect(tierPriceBody(5, 'month:3', 7000)).toEqual({
      expected_row_version: 5,
      prices: [{ billing_interval: 'month', interval_count: 3, unit_amount: 7000 }],
    })
    expect(tierPriceBody(5, 'year:1', null)).toEqual({
      expected_row_version: 5,
      prices: [{ billing_interval: 'year', interval_count: 1, unit_amount: null }],
    })
  })

  it('sends pools and row version only', () => {
    expect(poolsBody(6, ['a', 'b'])).toEqual({ expected_row_version: 6, pool_ids: ['a', 'b'] })
    expect(rowVersionBody(8)).toEqual({ expected_row_version: 8 })
  })
})

// ---------------------------------------------------------------------------
// 新建
// ---------------------------------------------------------------------------
describe('create', () => {
  it('starts empty per kind', () => {
    expect(emptyCreate()).toEqual(expect.objectContaining({ kind: 'subscription', publish: true }))
    expect(emptyCreate('one_time').kind).toBe('one_time')
  })

  it('validates subscription tiers', () => {
    const f = { ...emptyCreate(), name: '' }
    expect(createProblems(f)['tier.month:1']).toBe('至少填一个周期的价格')
    const f2 = { ...f, name: '标准', tiers: { ...f.tiers, 'month:1': '25', 'year:1': 'x' } }
    expect(createProblems(f2)).toEqual({ 'tier.year:1': '填数字，最多两位小数' })
  })

  it('validates one-time and usage fields', () => {
    const f = { ...emptyCreate('one_time'), name: '一次', amount: '', days: '0', gb: 'x', devices: '-1' }
    expect(createProblems(f)).toEqual({
      unit_amount: '填价格，最多两位小数',
      validity_days: '填 1–36500 的天数，或留空为永久',
      traffic_bytes: '填正整数，或留空为不限',
      max_devices: '填正整数，或留空为不限',
    })
  })

  it('accepts a valid one-time validity', () => {
    const f = { ...emptyCreate('one_time'), name: '年度买断', amount: '199', days: '365' }
    expect(createProblems(f)).toEqual({})
  })

  it('validates reset only when on', () => {
    const off = { ...emptyCreate(), name: '标准', tiers: { ...emptyCreate().tiers, 'month:1': '25' } }
    expect(createProblems(off)).toEqual({})
    const on = { ...off, resetOn: true, resetPrice: '', resetLimit: '0' }
    expect(createProblems(on)).toEqual({
      'reset.price': '填每次价格，大于 0、最多两位小数',
      'reset.limit': '填 1–1000 的整数，或留空为不限',
    })
  })

  it('builds a subscription body without one-time keys', () => {
    const f = {
      ...emptyCreate(),
      name: ' 标准套餐 ',
      description: '  ',
      tiers: { 'month:1': '25', 'month:3': '', 'month:6': '50', 'year:1': '' },
      gb: '10',
      devices: '3',
      poolIds: ['pool-a'],
      resetOn: true,
      resetPrice: '3',
      resetLimit: '5',
      publish: false,
    }
    expect(createBody(f)).toEqual({
      kind: 'subscription',
      name: '标准套餐',
      description: null,
      traffic_bytes: 10 * GiB,
      max_devices: 3,
      throttle_kbps: null,
      pool_ids: ['pool-a'],
      reset: { price: 300, limit: 5 },
      visibility: 'public',
      visible_group_ids: [],
      allow_new_purchase: true,
      allow_renewal: true,
      allow_upgrade: true,
      recommended: false,
      publish: false,
      prices: [
        { billing_interval: 'month', interval_count: 1, unit_amount: 2500 },
        { billing_interval: 'month', interval_count: 6, unit_amount: 5000 },
      ],
    })
    expect(createBody(f)).not.toHaveProperty('unit_amount')
    expect(createBody(f)).not.toHaveProperty('validity_days')
  })

  it('builds a one-time body without prices keys', () => {
    const f = { ...emptyCreate('one_time'), name: '加油包', amount: '30', days: '90' }
    const body = createBody(f)
    expect(body).toMatchObject({ kind: 'one_time', unit_amount: 3000, validity_days: 90, allow_renewal: false })
    expect(body).not.toHaveProperty('prices')
    expect(createBody({ ...f, days: '' })).toMatchObject({ validity_days: null })
  })

  it('maps server price keys back to tier keys', () => {
    const f = { ...emptyCreate(), tiers: { 'month:1': '', 'month:3': '30', 'month:6': '', 'year:1': '258' } }
    expect(createFieldKey('prices.0.unit_amount', f)).toBe('tier.month:3')
    expect(createFieldKey('prices.1', f)).toBe('tier.year:1')
    expect(createFieldKey('prices.2.unit_amount', f)).toBe('prices.2.unit_amount')
    expect(createFieldKey('name', f)).toBe('name')
  })
})

// ---------------------------------------------------------------------------
// 套餐代码
// ---------------------------------------------------------------------------
describe('autoCode', () => {
  it('follows the Go code.go rules', () => {
    expect(autoCode('标准版', [])).toBe('standard')
    expect(autoCode('专业 年付', [])).toBe('pro-yearly')
    expect(autoCode('Pro Max!!', [])).toBe('pro-max')
    expect(autoCode('年', [])).toBe('yearly')
    expect(autoCode('!!', [])).toBe('plan')
    expect(autoCode('a', [])).toBe('plan-a')
  })

  it('skips taken codes', () => {
    expect(autoCode('标准', ['standard', 'standard-2'])).toBe('standard-3')
  })

  it('truncates to 32 and trims trailing dashes', () => {
    const long = autoCode('abcdefghijklmnopqrstuvwxyz0123456789--', [])
    expect(long.length).toBeLessThanOrEqual(32)
    expect(long.endsWith('-')).toBe(false)
    expect(autoCode('abcdefghij'.repeat(4) + '-k', [])).toBe('abcdefghijabcdefghijabcdefghijabcdefghij'.slice(0, 32))
  })

  it('previews via codeHint', () => {
    expect(codeHint('  ', [])).toBe('')
    expect(codeHint('标准', [])).toBe('套餐代码将自动生成为 standard')
  })

  it('tells created toasts', () => {
    expect(createdToast(true)).toBe('已创建并上架')
    expect(createdToast(false)).toBe('已保存为草稿')
  })
})
