import { formatBytes, formatCount, formatDateTime, formatMoney } from '../../../core/format'
import type { PlanDetail, PlanKind, PlanRow, PlanStatus, PriceRow, VersionRow, Visibility } from './schemas'

export type Tone = 'ok' | 'warn' | 'danger' | 'info' | 'neutral'
export type Fields = Record<string, string>

/** 契约：GB = 字节 ÷ 1024³（与后端一致） */
export const GiB = 1024 ** 3

// ===========================================================================
// 常量与文案
// ===========================================================================
export const PLAN_STATUS_VIEW: Record<PlanStatus, { label: string; tone: Tone }> = {
  draft: { label: '草稿', tone: 'warn' },
  active: { label: '在售', tone: 'ok' },
  archived: { label: '已归档', tone: 'neutral' },
}

export const KIND_LABELS: Record<PlanKind, string> = {
  subscription: '订阅套餐',
  one_time: '一次性套餐',
}

export const VISIBILITY_LABELS: Record<Visibility, string> = {
  public: '所有人可见',
  authenticated: '登录用户可见',
  group: '指定用户组可见',
  hidden: '隐藏',
}

export type TierKey = 'month:1' | 'month:3' | 'month:6' | 'year:1'

export interface Tier {
  key: TierKey
  interval: string
  count: number
  label: string
  short: string
  months: number
}

/** 订阅套餐的四档周期价，顺序即展示顺序 */
export const TIERS: readonly Tier[] = [
  { key: 'month:1', interval: 'month', count: 1, label: '每月', short: '月', months: 1 },
  { key: 'month:3', interval: 'month', count: 3, label: '每季度', short: '季', months: 3 },
  { key: 'month:6', interval: 'month', count: 6, label: '每半年', short: '半年', months: 6 },
  { key: 'year:1', interval: 'year', count: 1, label: '每年', short: '年', months: 12 },
]

/** (interval, count) 是四档之一时给出它的 key，否则 null */
export function tierOf(interval: string, count: number): TierKey | null {
  return TIERS.find((t) => t.interval === interval && t.count === count)?.key ?? null
}

const PERIOD_UNITS: Record<string, string> = { day: '天', week: '周', month: '个月', quarter: '个季度', year: '年' }

/** 周期文案：四档给设计稿的 label，其它（订单页会传旧周期）写「每 N 个单位」 */
export function periodLabel(interval: string, count: number): string {
  if (interval === 'one_time') return '一次性'
  const key = tierOf(interval, count)
  if (key) return TIERS.find((t) => t.key === key)!.label
  const unit = PERIOD_UNITS[interval] ?? interval
  if (count === 1 && interval !== 'month') return `每${unit}`
  return `每 ${count} ${unit}`
}

// ===========================================================================
// 输入解析
// ===========================================================================
/** 元 → 分：非负、最多两位小数，「¥」前缀可有可无；不合法为 null */
export function parseAmount(input: string): number | null {
  const text = input.trim().replace(/^[¥$]/, '')
  if (!/^\d+(\.\d{1,2})?$/.test(text)) return null
  const [whole = '0', frac = ''] = text.split('.')
  const cents = Number(whole) * 100 + Number(frac.padEnd(2, '0'))
  return Number.isSafeInteger(cents) ? cents : null
}

/** 去空白后：空串 → null（不限）；正整数且 ≤ max → 数字；否则 'bad' */
export function parseOptionalInt(input: string, max: number): number | null | 'bad' {
  const text = input.trim()
  if (text === '') return null
  if (!/^\d+$/.test(text)) return 'bad'
  const n = Number(text)
  if (!Number.isSafeInteger(n) || n < 1 || n > max) return 'bad'
  return n
}

/** 流量展示：null = 不限 */
export function formatTraffic(bytes: number | null): string {
  return bytes === null ? '不限' : formatBytes(bytes)
}

/** 表单初值：null → ''；整除 GiB 时整数文本，否则至多两位小数去尾零 */
export function bytesToGb(bytes: number | null): string {
  if (bytes === null) return ''
  if (bytes % GiB === 0) return String(bytes / GiB)
  const whole = Math.floor(bytes / GiB)
  const frac = Math.round(((bytes % GiB) * 100) / GiB)
  if (frac === 100) return String(whole + 1)
  return frac === 0 ? String(whole) : `${whole}.${String(frac).padStart(2, '0').replace(/0+$/, '')}`
}

/** kbps → Mbps 表单初值；null → '' */
export function kbpsToMbps(kbps: number | null): string {
  return kbps === null ? '' : String(kbps / 1000)
}

/** 限速文案：null = 不限速 */
export function throttleLabel(kbps: number | null): string {
  return kbps === null ? '不限速' : `${kbps / 1000} Mbps`
}

/** 分 → 元的文本，去掉多余的零（重置价、一次性价的表单初值） */
const yuanText = (cents: number): string => {
  const whole = Math.floor(cents / 100)
  const frac = String(cents % 100).padStart(2, '0').replace(/0+$/, '')
  return frac ? `${whole}.${frac}` : String(whole)
}

const strOrNull = (n: number | null): string => (n === null ? '' : String(n))

// ===========================================================================
// 列表
// ===========================================================================
export type KindFilter = 'all' | PlanKind
export type StatusFilter = 'all' | PlanStatus

export function isKindFilter(v: string | null): v is KindFilter {
  return v === 'all' || v === 'subscription' || v === 'one_time'
}

export function isStatusFilter(v: string | null): v is StatusFilter {
  return v === 'all' || v === 'draft' || v === 'active' || v === 'archived'
}

export function filterPlans(rows: readonly PlanRow[], f: { kind: KindFilter; status: StatusFilter; q: string }): PlanRow[] {
  const q = f.q.trim().toLowerCase()
  return rows.filter((r) => {
    if (f.kind !== 'all' && r.kind !== f.kind) return false
    if (f.status !== 'all' && r.status !== f.status) return false
    if (q && !r.name.toLowerCase().includes(q) && !r.code.toLowerCase().includes(q)) return false
    return true
  })
}

/** 类型页签计数：只按状态筛，不看搜索词（与设计稿一致） */
export function kindCounts(rows: readonly PlanRow[], status: StatusFilter): Record<KindFilter, number> {
  const out: Record<KindFilter, number> = { all: 0, subscription: 0, one_time: 0 }
  for (const r of rows) {
    if (status !== 'all' && r.status !== status) continue
    out.all++
    out[r.kind]++
  }
  return out
}

export const STATUS_FILTER_OPTIONS = [
  { value: 'all', label: '全部状态' },
  { value: 'active', label: '在售' },
  { value: 'draft', label: '草稿' },
  { value: 'archived', label: '已归档' },
] as const
/** 在售价：按 TIERS 顺序排（one_time 排最后） */
export function activePrices(prices: readonly PriceRow[]): PriceRow[] {
  return prices
    .filter((p) => p.status === 'active')
    .sort((a, b) => {
      const ia = TIERS.findIndex((t) => t.interval === a.billing_interval && t.count === a.interval_count)
      const ib = TIERS.findIndex((t) => t.interval === b.billing_interval && t.count === b.interval_count)
      return (ia === -1 ? TIERS.length : ia) - (ib === -1 ? TIERS.length : ib)
    })
}

/** 列表卡片的全部文案 */
export function planListView(row: PlanRow): {
  kind: string
  price: string
  priceSub: string
  quota: string
  quotaSub: string
  lines: string
  linesWarn: boolean
  subs: string
} {
  const live = activePrices(row.prices)
  let price = '未定价'
  let priceSub = ''
  if (row.kind === 'subscription') {
    const first = live[0]
    if (first) {
      const tier = TIERS.find((t) => t.interval === first.billing_interval && t.count === first.interval_count)
      price = `${formatMoney(first.unit_amount)} / ${tier?.short ?? periodLabel(first.billing_interval, first.interval_count)}`
      priceSub = live.length > 1 ? `另有 ${live.length - 1} 个周期` : ''
    }
  } else {
    const once = live.find((p) => p.billing_interval === 'one_time')
    if (once) {
      price = formatMoney(once.unit_amount)
      priceSub = once.validity_days === null ? '一次性 · 永久有效' : `一次性 · 有效 ${once.validity_days} 天`
    }
  }
  const quota =
    row.traffic_bytes === null
      ? '不限流量'
      : row.kind === 'subscription'
        ? `${formatBytes(row.traffic_bytes)} / 周期`
        : `${formatBytes(row.traffic_bytes)} 总量`
  const quotaSub = `${row.max_devices === null ? '设备不限' : `${row.max_devices} 台设备`}${row.reset ? ' · 可重置' : ''}`
  const lines = row.pool_ids.length === 0 ? '未绑定' : `${row.pool_ids.length} 个节点池`
  const linesWarn = row.status !== 'archived' && (row.pool_ids.length === 0 || row.node_count === 0)
  return { kind: KIND_LABELS[row.kind], price, priceSub, quota, quotaSub, lines, linesWarn, subs: formatCount(row.active_subscriptions) }
}

// ===========================================================================
// 详情
// ===========================================================================
export type DetailTab = 'overview' | 'prices' | 'pools' | 'versions'

export function isDetailTab(v: string | undefined): v is DetailTab {
  return v === 'overview' || v === 'prices' || v === 'pools' || v === 'versions'
}

/** 详情页签：一次性套餐没有「价格」页签（价格在概览的「价格与有效期」组里改） */
export function detailTabs(p: PlanDetail): Array<{ value: DetailTab; label: string; count?: number }> {
  const out: Array<{ value: DetailTab; label: string; count?: number }> = [{ value: 'overview', label: '概览' }]
  if (p.kind === 'subscription') out.push({ value: 'prices', label: '价格', count: activePrices(p.prices).length })
  out.push({ value: 'pools', label: '线路', count: p.pool_ids.length })
  out.push({ value: 'versions', label: '版本', count: p.versions.length })
  return out
}

export function planSubtitle(p: PlanDetail): string {
  return `${KIND_LABELS[p.kind]} · ${formatCount(p.active_subscriptions)} 个有效订阅`
}

export const isLive = (p: PlanDetail): boolean => p.status !== 'archived'

export function draftVersion(p: PlanDetail): VersionRow | undefined {
  return p.versions.find((v) => v.status === 'draft')
}

export type OverviewNote = { text: string; action?: { label: string; tab: DetailTab } }

/** 概览顶部的提示，按严重程度排序 */
export function overviewNotes(p: PlanDetail): OverviewNote[] {
  const out: OverviewNote[] = []
  if (p.status === 'archived') {
    out.push({ text: '已归档：用户端不再展示、不能新购，老用户不能续费，已有订阅到期后结束。' })
  } else {
    if (p.pool_ids.length === 0) {
      out.push({
        text: '还没有绑定线路：买了这个套餐的用户会拿到一份空订阅。到「线路」里选择节点池。',
        action: { label: '去线路', tab: 'pools' },
      })
    } else if (p.node_count === 0) {
      out.push({
        text: '绑定的节点池里没有可下发的节点：这个套餐的订阅里将是 0 个节点。',
        action: { label: '去线路', tab: 'pools' },
      })
    }
  }
  if (p.status !== 'archived' && !p.allow_new_purchase) {
    out.push({
      text:
        p.kind === 'subscription'
          ? '已关闭新购：用户端看得到但买不了，已有订阅照常续费。'
          : '已关闭新购：用户端看得到但买不了，已买的照常用到期。',
    })
  }
  const draft = draftVersion(p)
  if (draft) {
    const publish = { label: '去发布', tab: 'versions' as const }
    if (p.status === 'draft') {
      out.push({ text: `还没上架：发布草稿 v${draft.version} 后开始售卖。`, action: publish })
    } else if (p.kind === 'subscription') {
      out.push({ text: `有一个未发布的草稿 v${draft.version}：发布后新购使用它，老用户下个周期切换。`, action: publish })
    } else {
      out.push({ text: `有一个未发布的草稿 v${draft.version}：发布后新购使用它，已买的不受影响。`, action: publish })
    }
  }
  return out
}

export type OverviewGroupKey = 'basic' | 'usage' | 'reset' | 'sales' | 'once'

/** 右栏「基本资料」：名称、代码、说明、类型 */
export function basicFacts(p: PlanDetail): Array<{ k: string; v: string; mono?: boolean; muted?: boolean; hint?: string }> {
  return [
    { k: '名称', v: p.name },
    { k: '代码', v: p.code, mono: true },
    p.description ? { k: '说明', v: p.description } : { k: '说明', v: '未填写', muted: true },
    {
      k: '类型',
      v: KIND_LABELS[p.kind],
      hint: p.kind === 'subscription' ? '按时间算，每周期清零' : '按流量算，用完即止',
    },
  ]
}

/** 概览的分组只读展示 */
export function overviewGroups(p: PlanDetail): Array<{ key: OverviewGroupKey; title: string; rows: Array<{ k: string; v: string; mono?: boolean; muted?: boolean }> }> {
  const draft = draftVersion(p)
  const groups: Array<{ key: OverviewGroupKey; title: string; rows: Array<{ k: string; v: string; mono?: boolean; muted?: boolean }> }> = [
    {
      key: 'usage',
      title: '用量',
      rows: [
        { k: p.kind === 'subscription' ? '每周期流量' : '总流量', v: formatTraffic(p.traffic_bytes) },
        { k: '同时在线设备', v: p.max_devices === null ? '不限' : `${p.max_devices} 台` },
        { k: '限速', v: throttleLabel(p.throttle_kbps) },
        {
          k: '版本',
          v: p.version === null ? '未发布' : `v${p.version}${draft ? ` · 草稿 v${draft.version} 待发布` : ''}`,
        },
      ],
    },
  ]
  if (p.reset) {
    const limit =
      p.reset.limit === null ? '不限' : p.kind === 'subscription' ? `每周期最多 ${p.reset.limit} 次` : `有效期内最多 ${p.reset.limit} 次`
    groups.push({
      key: 'reset',
      title: '流量重置',
      rows: [
        { k: '状态', v: '允许用户付费重置' },
        { k: '每次价格', v: formatMoney(p.reset.price), mono: true },
        { k: '次数限制', v: limit },
      ],
    })
  } else {
    groups.push({ key: 'reset', title: '流量重置', rows: [{ k: '状态', v: '未开启', muted: true }] })
  }
  const salesRows: Array<{ k: string; v: string }> = [
    {
      k: '可见范围',
      v: p.visibility === 'group' ? `${VISIBILITY_LABELS.group} · ${p.visible_group_ids.length} 个用户组` : VISIBILITY_LABELS[p.visibility],
    },
    { k: '新购', v: p.allow_new_purchase ? '允许' : '关闭' },
    ...(p.kind === 'subscription' ? [{ k: '续费', v: p.allow_renewal ? '允许' : '关闭' }] : []),
    { k: '从其他套餐变更', v: p.allow_upgrade ? '允许' : '关闭' },
    { k: '推荐标记', v: p.recommended ? '已标为推荐' : '无' },
  ]
  groups.push({ key: 'sales', title: '销售', rows: salesRows })
  if (p.kind === 'one_time') {
    const once = activePrices(p.prices).find((pr) => pr.billing_interval === 'one_time')
    groups.push({
      key: 'once',
      title: '价格与有效期',
      rows: [
        { k: '价格', v: once ? formatMoney(once.unit_amount) : '未定价', mono: once !== undefined },
        { k: '有效期', v: once?.validity_days === undefined || once.validity_days === null ? '永久' : `${once.validity_days} 天` },
        { k: '到期后', v: '不能续费，订阅结束' },
      ],
    })
  }
  return groups
}

/** 价格行的「折合每月 ¥x，省 y%」：months ≤ 1 或 amount ≤ 0 时为空串；monthAmount 是当前在售月付价（没有为 null） */
export function perMonthNote(amount: number, months: number, monthAmount: number | null): string {
  if (months <= 1 || amount <= 0) return ''
  let out = `折合每月 ${formatMoney(Math.round(amount / months))}`
  if (monthAmount !== null && monthAmount > 0) {
    const pct = Math.round((1 - amount / (monthAmount * months)) * 100)
    if (pct >= 1) out += `，省 ${pct}%`
  }
  return out
}

export interface PriceListRow {
  id: string
  label: string
  amount: string
  unitAmount: number
  active: boolean
  tier: TierKey | null
  /** 状态标签 */
  status: { label: '在售' | '已停售'; tone: 'ok' | 'neutral' }
  /** 金额后的灰字：在售行是「折合每月…」（同现在）；已停售且同一档现在有在售价的行是「已换成新价」；其余空串 */
  note: string
  /** 这一档现在没有在售价、且这是该档最新的一条已停售价格、tier 不为 null 时为 true */
  restorable: boolean
}

/** 价格列表：在售的按 TIERS 顺序在前，已归档的按 created_at 降序在后 */
export function priceListRows(p: PlanDetail): PriceListRow[] {
  const monthAmount = activeTierPrice(p, 'month:1')?.unit_amount ?? null
  const live: PriceListRow[] = activePrices(p.prices).map((pr) => {
    const tier = tierOf(pr.billing_interval, pr.interval_count)
    const months = tier ? TIERS.find((t) => t.key === tier)!.months : 1
    const note = tier !== null ? perMonthNote(pr.unit_amount, months, monthAmount) : ''
    return {
      id: pr.id,
      label: periodLabel(pr.billing_interval, pr.interval_count),
      amount: formatMoney(pr.unit_amount),
      unitAmount: pr.unit_amount,
      active: true,
      tier,
      status: { label: '在售', tone: 'ok' },
      note,
      restorable: false,
    }
  })

  const activeTierSet = new Set(live.map((r) => r.tier).filter((t): t is TierKey => t !== null))
  const seenArchivedTiers = new Set<TierKey>()

  const archived: PriceListRow[] = p.prices
    .filter((pr) => pr.status !== 'active')
    .sort((a, b) => (a.created_at < b.created_at ? 1 : a.created_at > b.created_at ? -1 : 0))
    .map((pr) => {
      const tier = tierOf(pr.billing_interval, pr.interval_count)
      const hasActive = tier !== null && activeTierSet.has(tier)
      let restorable = false
      let note = ''
      if (hasActive) {
        note = '已换成新价'
      } else if (tier !== null && !seenArchivedTiers.has(tier)) {
        seenArchivedTiers.add(tier)
        restorable = true
      }
      return {
        id: pr.id,
        label: periodLabel(pr.billing_interval, pr.interval_count),
        amount: formatMoney(pr.unit_amount),
        unitAmount: pr.unit_amount,
        active: false,
        tier,
        status: { label: '已停售', tone: 'neutral' },
        note,
        restorable,
      }
    })
  return [...live, ...archived]
}

export function activeTierPrice(p: PlanDetail, tier: TierKey): PriceRow | undefined {
  const t = TIERS.find((x) => x.key === tier)!
  return activePrices(p.prices).find((pr) => pr.billing_interval === t.interval && pr.interval_count === t.count)
}

/** 折合每月价与相对月付的折扣提示（金额按元的文本输入） */
export function tierHint(amountText: string, tier: TierKey, monthText: string): string {
  const cents = parseAmount(amountText)
  if (cents === null || cents <= 0) return ''
  const amount = cents / 100
  const months = TIERS.find((t) => t.key === tier)?.months ?? 1
  if (months <= 1) return ''
  const parts = [`折合 ¥${(amount / months).toFixed(2)} / 月`]
  const monthCents = parseAmount(monthText)
  if (monthCents !== null && monthCents > 0) {
    const perMonths = (monthCents / 100) * months
    if (amount < perMonths) parts.push(`省 ${Math.round((1 - amount / perMonths) * 100)}%`)
  }
  return parts.join('，')
}

/**
 * 按所选节点池的可下发节点数给出「0 节点」提示，没问题时返回 null。
 * 可下发数与订阅下载同一口径（后端 subscription.DeliverableNodeSQL）。
 */
export function poolBindingWarning(pools: ReadonlyArray<{ id: string; deliverable_nodes: number }>, picked: readonly string[]): string | null {
  if (picked.length === 0) return '还没绑定节点池：此套餐的订阅里将是 0 个节点。'
  const total = pools.filter((p) => picked.includes(p.id)).reduce((sum, p) => sum + p.deliverable_nodes, 0)
  if (total > 0) return null
  return '所选节点池里没有可下发的节点：此套餐的订阅里将是 0 个节点。检查池里的节点是否在役、上报过心跳、服务器已就绪、协议与地址已配好。'
}

export function togglePool(ids: readonly string[], id: string): string[] {
  return ids.includes(id) ? ids.filter((x) => x !== id) : [...ids, id]
}

export function poolMeta(pool: { active_nodes: number; deliverable_nodes: number }): string {
  return `${pool.deliverable_nodes} / ${pool.active_nodes} 个节点可下发`
}

/** 版本行的展示：versions 原顺序（降序）；草稿 warn、当前 ok、历史 neutral */
export function versionListRows(p: PlanDetail): Array<{ id: string; n: number; label: string; tone: Tone; at: string; summary: string; by: string | null; isDraft: boolean }> {
  return p.versions.map((v) => {
    const isDraft = v.status === 'draft'
    const isCurrent = !isDraft && v.version === p.version
    const traffic = v.traffic_bytes === null ? '不限流量' : p.kind === 'subscription' ? `${formatBytes(v.traffic_bytes)} / 周期` : `${formatBytes(v.traffic_bytes)} 总量`
    const parts = [traffic, v.max_devices === null ? '设备不限' : `${v.max_devices} 台设备`]
    if (v.throttle_kbps !== null) parts.push(`限速 ${v.throttle_kbps / 1000} Mbps`)
    return {
      id: v.id,
      n: v.version,
      label: isDraft ? '草稿' : isCurrent ? '当前' : '历史',
      tone: isDraft ? 'warn' : isCurrent ? 'ok' : 'neutral',
      at: formatDateTime(v.published_at ?? v.created_at).slice(0, 10),
      summary: parts.join(' · '),
      by: v.created_by_email,
      isDraft,
    }
  })
}

export function publishCopy(p: PlanDetail, n: number): { title: string; body: string } {
  const body =
    p.kind === 'subscription'
      ? '发布后新购使用这个版本，订阅老用户在下个周期切换。已发布的版本不能再改。'
      : '发布后新购使用这个版本，已买的老用户不受影响。已发布的版本不能再改。'
  return { title: `发布 v${n}？`, body: p.status === 'draft' ? `首个版本发布后套餐开始售卖。${body}` : body }
}

export const ARCHIVE_BODY =
  '用户端不再展示、不能新购；老用户不能续费，已有订阅到期后结束。归档后不能恢复；只想暂停售卖，请在「销售」里关闭新购。'

// ===========================================================================
// 表单（字符串状态）+ 校验 + 请求体；校验键名 = 后端 422 的 fields 键
// ===========================================================================

// --- 基本资料：PUT v1/plans/:id/basic ---
export interface BasicForm {
  name: string
  description: string
}

export function basicForm(p: PlanDetail): BasicForm {
  return { name: p.name, description: p.description ?? '' }
}

export function basicProblems(f: BasicForm): Fields {
  const out: Fields = {}
  const name = [...f.name.trim()].length
  if (name < 1 || name > 120) out.name = '请填写名称，最多 120 个字'
  if ([...f.description.trim()].length > 2000) out.description = '最多 2000 个字'
  return out
}

export function basicBody(f: BasicForm, rowVersion: number) {
  return {
    expected_row_version: rowVersion,
    name: f.name.trim(),
    description: f.description.trim() || null,
  }
}

// --- 用量：PUT v1/plans/:id/usage（写进草稿版本，没有就新建） ---
export interface UsageForm {
  gb: string
  devices: string
  mbps: string
}

/** 有草稿取草稿版本的值，否则取套餐当前的三项 */
export function usageForm(p: PlanDetail): UsageForm {
  const draft = draftVersion(p)
  return {
    gb: bytesToGb(draft ? draft.traffic_bytes : p.traffic_bytes),
    devices: strOrNull(draft ? draft.max_devices : p.max_devices),
    mbps: kbpsToMbps(draft ? draft.throttle_kbps : p.throttle_kbps),
  }
}

export function usageProblems(f: UsageForm): Fields {
  const out: Fields = {}
  if (parseOptionalInt(f.gb, 1048576) === 'bad') out.traffic_bytes = '填正整数，或留空为不限'
  if (parseOptionalInt(f.devices, 2147483647) === 'bad') out.max_devices = '填正整数，或留空为不限'
  if (parseOptionalInt(f.mbps, 2147483) === 'bad') out.throttle_kbps = '填正整数，或留空为不限速'
  return out
}

export function usageBody(f: UsageForm, rowVersion: number) {
  const gb = parseOptionalInt(f.gb, 1048576)
  const devices = parseOptionalInt(f.devices, 2147483647)
  const mbps = parseOptionalInt(f.mbps, 2147483)
  return {
    expected_row_version: rowVersion,
    traffic_bytes: typeof gb === 'number' ? gb * GiB : null,
    max_devices: typeof devices === 'number' ? devices : null,
    throttle_kbps: typeof mbps === 'number' ? mbps * 1000 : null,
  }
}

// --- 销售：PUT v1/plans/:id/sales ---
export interface SalesForm {
  visibility: Visibility
  groupIds: string[]
  allowNew: boolean
  allowRenewal: boolean
  allowUpgrade: boolean
  recommended: boolean
}

export function salesForm(p: PlanDetail): SalesForm {
  return {
    visibility: p.visibility,
    groupIds: [...p.visible_group_ids],
    allowNew: p.allow_new_purchase,
    allowRenewal: p.allow_renewal,
    allowUpgrade: p.allow_upgrade,
    recommended: p.recommended,
  }
}

export function salesProblems(f: SalesForm): Fields {
  const out: Fields = {}
  if (f.visibility === 'group' && f.groupIds.length === 0) out.visible_group_ids = '指定用户组可见时至少选一个用户组'
  return out
}

export function salesBody(f: SalesForm, kind: PlanKind, rowVersion: number) {
  return {
    expected_row_version: rowVersion,
    visibility: f.visibility,
    visible_group_ids: f.visibility === 'group' ? f.groupIds : [],
    allow_new_purchase: f.allowNew,
    allow_renewal: kind === 'subscription' ? f.allowRenewal : false,
    allow_upgrade: f.allowUpgrade,
    recommended: f.recommended,
  }
}

// --- 流量重置：PUT v1/plans/:id/reset ---
export interface ResetForm {
  on: boolean
  price: string
  limit: string
}

export function resetForm(p: PlanDetail): ResetForm {
  if (!p.reset) return { on: false, price: '', limit: '' }
  return { on: true, price: yuanText(p.reset.price), limit: strOrNull(p.reset.limit) }
}

export function resetProblems(f: ResetForm): Fields {
  const out: Fields = {}
  if (!f.on) return out
  const price = parseAmount(f.price)
  if (price === null || price <= 0) out['reset.price'] = '填每次价格，大于 0、最多两位小数'
  const limit = parseOptionalInt(f.limit, 1000)
  if (limit === 'bad') out['reset.limit'] = '填 1–1000 的整数，或留空为不限'
  return out
}

export function resetBody(f: ResetForm, rowVersion: number) {
  return {
    expected_row_version: rowVersion,
    enabled: f.on,
    price: f.on ? (parseAmount(f.price) ?? 0) : 0,
    limit: f.on ? (parseOptionalInt(f.limit, 1000) ?? null) : null,
  }
}

// --- 一次性价格：PUT v1/plans/:id/prices（unit_amount + validity_days） ---
export interface OnceForm {
  amount: string
  days: string
}

export function onceForm(p: PlanDetail): OnceForm {
  const once = activePrices(p.prices).find((pr) => pr.billing_interval === 'one_time')
  return { amount: once ? yuanText(once.unit_amount) : '', days: once?.validity_days === undefined || once.validity_days === null ? '' : String(once.validity_days) }
}

export function onceProblems(f: OnceForm): Fields {
  const out: Fields = {}
  if (parseAmount(f.amount) === null) out.unit_amount = '填价格，最多两位小数'
  const days = parseOptionalInt(f.days, 36500)
  if (days === 'bad') out.validity_days = '填 1–36500 的天数，或留空为永久'
  return out
}

export function onceBody(f: OnceForm, rowVersion: number) {
  const days = parseOptionalInt(f.days, 36500)
  return {
    expected_row_version: rowVersion,
    unit_amount: parseAmount(f.amount) ?? 0,
    validity_days: typeof days === 'number' ? days : null,
  }
}

// --- 订阅单档改价：PUT v1/plans/:id/prices（prices，null = 停售这一档） ---
export function tierPriceBody(rowVersion: number, tier: TierKey, amount: number | null) {
  const t = TIERS.find((x) => x.key === tier)!
  return {
    expected_row_version: rowVersion,
    prices: [{ billing_interval: t.interval, interval_count: t.count, unit_amount: amount }],
  }
}

// --- 线路：PUT v1/plans/:id/pools（整体替换） ---
export function poolsBody(rowVersion: number, ids: readonly string[]) {
  return { expected_row_version: rowVersion, pool_ids: [...ids] }
}

// --- 发布、归档：只带乐观锁 ---
export function rowVersionBody(rowVersion: number) {
  return { expected_row_version: rowVersion }
}

// --- 新建：POST v1/plans ---
export interface CreateForm {
  kind: PlanKind
  name: string
  description: string
  tiers: Record<TierKey, string>
  amount: string
  days: string
  gb: string
  devices: string
  poolIds: string[]
  resetOn: boolean
  resetPrice: string
  resetLimit: string
  publish: boolean
}

export function emptyCreate(kind: PlanKind = 'subscription'): CreateForm {
  return {
    kind,
    name: '',
    description: '',
    tiers: { 'month:1': '', 'month:3': '', 'month:6': '', 'year:1': '' },
    amount: '',
    days: '',
    gb: '',
    devices: '',
    poolIds: [],
    resetOn: false,
    resetPrice: '',
    resetLimit: '',
    publish: true,
  }
}

export function createProblems(f: CreateForm): Fields {
  const out: Fields = {}
  const name = [...f.name.trim()].length
  if (name < 1 || name > 120) out.name = '请填写名称，最多 120 个字'
  if (f.kind === 'subscription') {
    let filled = 0
    for (const t of TIERS) {
      const text = f.tiers[t.key].trim()
      if (text === '') continue
      filled++
      if (parseAmount(text) === null) out[`tier.${t.key}`] = '填数字，最多两位小数'
    }
    if (filled === 0) out['tier.month:1'] = '至少填一个周期的价格'
  } else {
    if (parseAmount(f.amount) === null) out.unit_amount = '填价格，最多两位小数'
    if (parseOptionalInt(f.days, 36500) === 'bad') out.validity_days = '填 1–36500 的天数，或留空为永久'
  }
  if (parseOptionalInt(f.gb, 1048576) === 'bad') out.traffic_bytes = '填正整数，或留空为不限'
  if (parseOptionalInt(f.devices, 2147483647) === 'bad') out.max_devices = '填正整数，或留空为不限'
  if (f.resetOn) {
    const price = parseAmount(f.resetPrice)
    if (price === null || price <= 0) out['reset.price'] = '填每次价格，大于 0、最多两位小数'
    const limit = parseOptionalInt(f.resetLimit, 1000)
    if (limit === 'bad') out['reset.limit'] = '填 1–1000 的整数，或留空为不限'
  }
  return out
}

/** 新建的请求体：只带本类型的键，另一类型的价格键不出现 */
export function createBody(f: CreateForm) {
  const gb = parseOptionalInt(f.gb, 1048576)
  const devices = parseOptionalInt(f.devices, 2147483647)
  const resetLimit = f.resetOn ? parseOptionalInt(f.resetLimit, 1000) : null
  const base = {
    kind: f.kind,
    name: f.name.trim(),
    description: f.description.trim() || null,
    traffic_bytes: typeof gb === 'number' ? gb * GiB : null,
    max_devices: typeof devices === 'number' ? devices : null,
    throttle_kbps: null,
    pool_ids: [...f.poolIds],
    reset: f.resetOn ? { price: parseAmount(f.resetPrice) ?? 0, limit: typeof resetLimit === 'number' ? resetLimit : null } : null,
    visibility: 'public' as const,
    visible_group_ids: [] as string[],
    allow_new_purchase: true,
    allow_renewal: f.kind === 'subscription',
    allow_upgrade: true,
    recommended: false,
    publish: f.publish,
  }
  if (f.kind === 'subscription') {
    const prices = TIERS.filter((t) => f.tiers[t.key].trim() !== '').map((t) => ({
      billing_interval: t.interval,
      interval_count: t.count,
      unit_amount: parseAmount(f.tiers[t.key]) ?? 0,
    }))
    return { ...base, prices }
  }
  const days = parseOptionalInt(f.days, 36500)
  return { ...base, unit_amount: parseAmount(f.amount) ?? 0, validity_days: typeof days === 'number' ? days : null }
}

/** 后端 422 的 prices.<i> 指发出去的第 i 档，映射回表单的 tier.<周期>；其它键原样 */
export function createFieldKey(serverKey: string, f: CreateForm): string {
  const m = serverKey.match(/^prices\.(\d+)(?:\.unit_amount)?$/)
  if (m) {
    const filled = TIERS.filter((t) => f.tiers[t.key].trim() !== '')
    const idx = Number(m[1])
    if (idx < filled.length) return `tier.${filled[idx]!.key}`
  }
  return serverKey
}

// ===========================================================================
// 套餐代码预览：与 Go catalog/service/code.go 逐条相同，改一边要改另一边
// ===========================================================================
const CODE_WORDS: Record<string, string> = {
  标准: 'standard', 专业: 'pro', 高级: 'premium', 家庭: 'family', 体验: 'trial', 试用: 'trial',
  企业: 'enterprise', 定制: 'custom', 基础: 'basic', 旗舰: 'ultimate', 年: 'yearly', 季: 'quarterly',
  月: 'monthly', 旧: 'legacy', 新: 'new', 学生: 'student', 团队: 'team', 个人: 'personal',
}

/** 词表替换顺序：长词优先，同长按字典序 */
const CODE_WORD_ORDER = Object.keys(CODE_WORDS).sort((a, b) => {
  const la = [...a].length
  const lb = [...b].length
  if (la !== lb) return lb - la
  return a < b ? -1 : a > b ? 1 : 0
})

const MAX_CODE_BASE = 32

/** 按名称算出代码主体（未查重） */
function autoCodeBase(name: string): string {
  let s = name.trim()
  for (const k of CODE_WORD_ORDER) {
    s = s.split(k).join(` ${CODE_WORDS[k]} `)
  }
  let out = ''
  let dash = false
  for (const ch of s.toLowerCase()) {
    if ((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9')) {
      if (dash && out.length > 0) out += '-'
      dash = false
      out += ch
      continue
    }
    dash = true
  }
  if (out.length > MAX_CODE_BASE) out = out.slice(0, MAX_CODE_BASE).replace(/-+$/, '')
  if (out.length === 0) return 'plan'
  if (out.length === 1) return `plan-${out}`
  return out
}

/** base 被占用时依次试 -2、-3…… */
export function autoCode(name: string, taken: readonly string[]): string {
  const used = new Set(taken)
  const base = autoCodeBase(name)
  if (!used.has(base)) return base
  for (let i = 2; ; i++) {
    const c = `${base}-${i}`
    if (!used.has(c)) return c
  }
}

/** 新建弹窗里代码字段的预览提示 */
export function codeHint(name: string, taken: readonly string[]): string {
  if (name.trim() === '') return ''
  return `套餐代码将自动生成为 ${autoCode(name, taken)}`
}

export function createdToast(publish: boolean): string {
  return publish ? '已创建并上架' : '已保存为草稿'
}
