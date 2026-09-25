/**
 * [INPUT]: 依赖 ../../../core/format 的 formatBytes / formatDateTime / formatMoney，依赖 ./api 的类型与枚举
 * [OUTPUT]: 对外提供 GiB、Tone、状态与可见性文案（PLAN_STATUS_VIEW、VISIBILITY_LABELS、RESET_LABELS、OVERAGE_LABELS、versionView、versionNote）、周期（PERIOD_OPTIONS、periodKey、parsePeriod、periodLabel）、金额与数字（parseAmount、parseCount）、列表与详情文案（priceFrom、quotaLabel、trafficOf、versionSummary、publishBlockers）、版本表单（VersionForm、versionForm、versionProblems、versionBody）、向导（WizardForm、WizardPrice、emptyWizard、wizardFromPlan、wizardProblems、createBody、updateBody、removedCurrencies、stepOfField、WIZARD_STEPS）、销售设置（SalesForm、salesForm、salesProblems、salesBody）、新增价格（PriceForm、emptyPriceForm、priceProblems、priceBody）、流量包（PackForm、packForm、packProblems、packBody、PACK_STATUS_VIEW）、时间输入互转（toLocalInput、fromLocalInput）
 * [POS]: admin/screens/plans 的纯逻辑：契约后台-04 的状态 / 版本 / 周期映射，额度在版本 quotas 里的读写（traffic.bytes 与 devices.active 同步维护、其余条目原样回填），向导两种提交体（新建 POST complete、编辑 PUT complete 的「null = 不动」与只同步出现过的币种），销售设置的整体覆盖，流量包 GB ↔ 字节；各校验与后端 Go 同规则、fields 键名与后端一致，页面把后端 422 与前端预检标在同一处。不碰 React 与网络，model.test.ts 覆盖
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { formatBytes, formatDateTime, formatMoney } from '../../../core/format'
import type { BillingInterval, Currency, OveragePolicy, PlanDetail, PlanRow, PlanStatus, PriceRow, Quota, ResetStrategy, TrafficPack, VersionRow, Visibility } from './api'

export type Tone = 'ok' | 'warn' | 'danger' | 'info' | 'neutral'
type Fields = Record<string, string>

/** 契约：GB = 字节 ÷ 1024³（与 adminops bytesPerGB 一致） */
export const GiB = 1024 ** 3

// ===========================================================================
// 状态与文案
// ===========================================================================
export const PLAN_STATUS_VIEW: Record<PlanStatus, { label: string; tone: Tone }> = {
  draft: { label: '草稿', tone: 'warn' },
  active: { label: '在售', tone: 'ok' },
  archived: { label: '已归档', tone: 'neutral' },
}

export const VISIBILITY_LABELS: Record<Visibility, string> = {
  public: '所有人可见',
  authenticated: '登录用户可见',
  group: '指定用户组可见',
  invite_only: '仅邀请（不能发布）',
  hidden: '隐藏',
}

export const RESET_LABELS: Record<ResetStrategy, string> = {
  billing_cycle: '按账单周期重置',
  natural_month: '每月 1 日重置',
  fixed_day: '每月固定日重置',
  never: '不重置',
}

export const OVERAGE_LABELS: Record<OveragePolicy, string> = {
  suspend: '用完暂停服务',
  throttle: '用完限速',
  metered_billing: '超额计费',
}

/** 版本状态：草稿 / 当前发布（id 等于 current_version_id）/ 历史 */
export function versionView(v: Pick<VersionRow, 'id' | 'status'>, currentId: string | null): { label: string; tone: Tone } {
  if (v.status === 'draft') return { label: '草稿', tone: 'warn' }
  if (v.status === 'published' && v.id === currentId) return { label: '当前发布', tone: 'ok' }
  return { label: '历史', tone: 'neutral' }
}

const day = (at: string) => formatDateTime(at).slice(0, 10)

/** 设计稿的「草稿 · 周敏 · 09-22」「2026-08-01 发布」 */
export function versionNote(v: Pick<VersionRow, 'status' | 'frozen_at' | 'created_at' | 'created_by_email'>): string {
  if (v.status === 'draft') return ['草稿', v.created_by_email, day(v.created_at).slice(5)].filter(Boolean).join(' · ')
  return v.frozen_at ? `${day(v.frozen_at)} 发布` : day(v.created_at)
}

// ===========================================================================
// 周期：设计稿五档 ↔ (billing_interval, interval_count)；季付就是 month×3
// ===========================================================================
export const PERIOD_OPTIONS = [
  { value: 'month:1', label: '每月' },
  { value: 'month:3', label: '每季度' },
  { value: 'month:6', label: '每半年' },
  { value: 'year:1', label: '每年' },
  { value: 'one_time:1', label: '一次性' },
] as const

const UNIT_LABELS: Record<BillingInterval, string> = { day: '天', week: '周', month: '个月', quarter: '个季度', year: '年', one_time: '' }
export const CUSTOM_UNITS = [
  { value: 'day', label: '天' },
  { value: 'week', label: '周' },
  { value: 'month', label: '个月' },
  { value: 'year', label: '年' },
] as const

export const periodKey = (interval: BillingInterval, count: number) => `${interval}:${count}`

export function parsePeriod(key: string): { billing_interval: BillingInterval; interval_count: number } {
  const [interval, n] = key.split(':')
  return { billing_interval: interval as BillingInterval, interval_count: Number(n) }
}

export function periodLabel(interval: BillingInterval, count: number): string {
  if (interval === 'one_time') return '一次性'
  if (interval === 'quarter' && count === 1) return '每季度'
  const preset = PERIOD_OPTIONS.find((o) => o.value === periodKey(interval, count))
  if (preset) return preset.label
  return count === 1 && interval !== 'month' ? `每${UNIT_LABELS[interval]}` : `每 ${count} ${UNIT_LABELS[interval]}`
}

// ===========================================================================
// 数字输入
// ===========================================================================
/** 元 → 分：非负、最多两位小数，「¥」前缀可有可无；不合法为 null */
export function parseAmount(input: string): number | null {
  const text = input.trim().replace(/^[¥$]/, '')
  if (!/^\d+(\.\d{1,2})?$/.test(text)) return null
  const [whole = '0', frac = ''] = text.split('.')
  const cents = Number(whole) * 100 + Number(frac.padEnd(2, '0'))
  return Number.isSafeInteger(cents) ? cents : null
}

/** 非负整数；空串为 null，不合法为 NaN */
export function parseCount(input: string): number | null {
  const text = input.trim()
  if (text === '') return null
  return /^\d+$/.test(text) && Number.isSafeInteger(Number(text)) ? Number(text) : Number.NaN
}

const isBad = (n: number | null) => n !== null && Number.isNaN(n)

// ===========================================================================
// 列表卡片与详情事实
// ===========================================================================
/** 起价：在售价里优先公开价、优先 CNY，取最低一档 */
export function priceFrom(prices: readonly PriceRow[]): string {
  const live = prices.filter((p) => p.status === 'active')
  const pool = live.filter((p) => p.user_group_id === null)
  const pick = (pool.length ? pool : live).filter((p, _, all) => (all.some((x) => x.currency === 'CNY') ? p.currency === 'CNY' : true))
  if (!pick.length) return '无价格'
  const low = pick.reduce((a, b) => (b.unit_amount < a.unit_amount ? b : a))
  return `${formatMoney(low.unit_amount, low.currency)} 起`
}

export function quotaLabel(trafficBytes: number | null, devices: number | null): string {
  return `${trafficBytes === null ? '不限流量' : formatBytes(trafficBytes)} · ${devices === null ? '不限设备' : `${devices} 设备`}`
}

export function trafficOf(quotas: readonly Quota[]): number | null {
  return quotas.find((q) => q.metric === 'traffic.bytes')?.limit ?? null
}

export function versionSummary(v: Pick<VersionRow, 'quotas' | 'max_devices' | 'throttle_kbps'>): string {
  const traffic = trafficOf(v.quotas)
  const parts = [traffic === null ? '不限流量' : `${formatBytes(traffic)} / 周期`, v.max_devices === null ? '不限设备' : `${v.max_devices} 台设备`]
  if (v.throttle_kbps !== null) parts.push(`限速 ${v.throttle_kbps / 1000} Mbps`)
  return parts.join(' · ')
}

/** 发布前能在前端看出来的阻碍（后端 422 的前置条件），只做提示，最终以后端为准 */
export function publishBlockers(plan: Pick<PlanDetail, 'prices' | 'visibility'>, version: Pick<VersionRow, 'pool_ids'>): string[] {
  const out: string[] = []
  if (!plan.prices.some((p) => p.status === 'active')) out.push('还没有在售价格，发布会被拒绝')
  if (!version.pool_ids.length) out.push('这个版本没有绑定节点池，发布会被拒绝')
  if (plan.visibility === 'invite_only') out.push('「仅邀请」可见的套餐不能发布，先在销售设置里改可见范围')
  return out
}

// ===========================================================================
// 版本表单：设计稿三项 + 「高级」折叠；entitlements 与其它 quotas 原样回填
// ===========================================================================
export interface VersionForm {
  gb: string
  devices: string
  mbps: string
  strategy: ResetStrategy
  resetDay: string
  graceHours: string
  graceKeeps: boolean
  renewalExtends: boolean
  renewalResets: boolean
  renewalKeepsAddons: boolean
  maxConcurrent: string
  releaseHours: string
  overage: OveragePolicy
  notes: string
}

const str = (n: number | null) => (n === null ? '' : String(n))

export function versionForm(v: VersionRow): VersionForm {
  const traffic = trafficOf(v.quotas)
  return {
    gb: traffic === null ? '' : String(Math.floor(traffic / GiB)),
    devices: str(v.max_devices),
    mbps: v.throttle_kbps === null ? '' : String(v.throttle_kbps / 1000),
    strategy: v.quota_reset_strategy,
    resetDay: str(v.quota_reset_day),
    graceHours: String(v.grace_period_hours),
    graceKeeps: v.grace_keeps_service,
    renewalExtends: v.renewal_extends_period,
    renewalResets: v.renewal_resets_quota,
    renewalKeepsAddons: v.renewal_keeps_addons,
    maxConcurrent: str(v.max_concurrent),
    releaseHours: String(v.device_release_hours),
    overage: v.overage_policy,
    notes: v.notes ?? '',
  }
}

/** Mbps → kbps（正整数）；空为 null，不合法为 NaN */
function parseKbps(input: string): number | null {
  const text = input.trim()
  if (text === '') return null
  if (!/^\d+(\.\d{1,3})?$/.test(text)) return Number.NaN
  const kbps = Math.round(Number(text) * 1000)
  return kbps > 0 ? kbps : Number.NaN
}

/** 键名与后端 validateVersionSemantics 一致；流量框另记为 traffic（后端报在 quotas.{i}.limit） */
export function versionProblems(f: VersionForm): Fields {
  const out: Fields = {}
  if (isBad(parseCount(f.gb)) || parseCount(f.gb) === 0) out.traffic = '填正整数 GB；不限流量请留空'
  const devices = parseCount(f.devices)
  if (isBad(devices) || devices === 0) out.max_devices = '必须为正整数；不限请留空'
  const concurrent = parseCount(f.maxConcurrent)
  if (isBad(concurrent) || concurrent === 0) out.max_concurrent = '必须为正整数；不限请留空'
  if (parseCount(f.graceHours) === null || isBad(parseCount(f.graceHours))) out.grace_period_hours = '填 0 或正整数小时'
  if (parseCount(f.releaseHours) === null || isBad(parseCount(f.releaseHours))) out.device_release_hours = '填 0 或正整数小时'
  if (f.strategy === 'fixed_day') {
    const d = parseCount(f.resetDay)
    if (d === null || Number.isNaN(d) || d < 1 || d > 28) out.quota_reset_day = '固定日必须为 1-28'
  }
  // D-C-5 未决前按后端校验：限速只能配「用完限速」策略，且该策略必须有速率
  const kbps = parseKbps(f.mbps)
  if (isBad(kbps)) out.throttle_kbps = '填正数 Mbps'
  else if (f.overage === 'throttle' && kbps === null) out.throttle_kbps = '「用完限速」策略必须设置速率'
  else if (f.overage !== 'throttle' && kbps !== null) out.throttle_kbps = '只有「用完限速」策略可以设置速率'
  return out
}

/** 流量与设备数写进 quotas（同 metric 各一条，周期沿用原条目，没有就按 cycle），其余条目原样保留 */
function syncQuotas(quotas: readonly Quota[], trafficBytes: number | null, devices: number | null): Quota[] {
  const keep = quotas.filter((q) => q.metric !== 'traffic.bytes' && q.metric !== 'devices.active')
  const periodOf = (metric: string) => quotas.find((q) => q.metric === metric)?.period ?? 'cycle'
  const out = [...keep]
  if (trafficBytes !== null) out.push({ metric: 'traffic.bytes', limit: trafficBytes, unit: 'bytes', period: periodOf('traffic.bytes') })
  if (devices !== null) out.push({ metric: 'devices.active', limit: devices, unit: 'count', period: periodOf('devices.active') })
  return out
}

/**
 * PUT v1/plans/{id}/versions/{vid} 的请求体（全量覆盖，pool_ids 必须省略）。
 * base 是表单来源版本（entitlements 与其它 quotas 取自它），rowVersion 是目标草稿的乐观锁——
 * 「另存为新版本」时两者不是同一个版本。先过 versionProblems 再调用。
 */
export function versionBody(f: VersionForm, base: Pick<VersionRow, 'quotas' | 'entitlements'>, rowVersion: number) {
  const gb = parseCount(f.gb)
  const devices = parseCount(f.devices)
  const kbps = parseKbps(f.mbps)
  return {
    expected_row_version: rowVersion,
    quota_reset_strategy: f.strategy,
    quota_reset_day: f.strategy === 'fixed_day' ? parseCount(f.resetDay) : null,
    grace_period_hours: parseCount(f.graceHours) ?? 0,
    grace_keeps_service: f.graceKeeps,
    renewal_extends_period: f.renewalExtends,
    renewal_resets_quota: f.renewalResets,
    renewal_keeps_addons: f.renewalKeepsAddons,
    max_devices: devices,
    max_concurrent: parseCount(f.maxConcurrent),
    device_release_hours: parseCount(f.releaseHours) ?? 0,
    overage_policy: f.overage,
    throttle_kbps: f.overage === 'throttle' ? kbps : null,
    notes: f.notes.trim() || null,
    entitlements: base.entitlements.map((e) => ({ code: e.code, value: e.value })),
    quotas: syncQuotas(base.quotas, gb === null ? null : gb * GiB, devices),
  }
}

// ===========================================================================
// 向导：新建走 POST v1/plans/complete，编辑走 PUT v1/plans/{id}/complete
// ===========================================================================
export const WIZARD_STEPS = ['基本资料', '用量与设备', '销售价格', '可用线路', '确认'] as const

export interface WizardPrice {
  key: string
  amount: string
  currency: Currency
  period: string
  trialDays: string
}

export interface WizardForm {
  name: string
  code: string
  description: string
  visibility: Visibility
  groupIds: string[]
  sortOrder: string
  gb: string
  devices: string
  strategy: ResetStrategy
  resetDay: string
  prices: WizardPrice[]
  poolIds: string[]
  allowNew: boolean
  allowRenewal: boolean
  allowUpgrade: boolean
  purchaseLimit: string
  stockTotal: string
  publish: boolean
}

let seq = 0
export const newPriceRow = (period = 'month:1'): WizardPrice => ({ key: `p${++seq}`, amount: '', currency: 'CNY', period, trialDays: '' })

export function emptyWizard(): WizardForm {
  return {
    name: '',
    code: '',
    description: '',
    visibility: 'public',
    groupIds: [],
    sortOrder: '0',
    gb: '',
    devices: '',
    strategy: 'billing_cycle',
    resetDay: '',
    prices: [newPriceRow()],
    poolIds: [],
    allowNew: true,
    allowRenewal: true,
    allowUpgrade: true,
    purchaseLimit: '',
    stockTotal: '',
    publish: true,
  }
}

const yuan = (minor: number) => (minor % 100 === 0 ? String(minor / 100) : (minor / 100).toFixed(2))

/** 编辑初值：额度与线路取当前发布版本（没有就取最新版本），价格回填全部在售公开价（两种币种都要，契约后台-04） */
export function wizardFromPlan(plan: PlanDetail): WizardForm {
  const cur = plan.versions.find((v) => v.id === plan.current_version_id) ?? plan.versions[0]
  const traffic = cur ? trafficOf(cur.quotas) : null
  return {
    name: plan.name,
    code: plan.code,
    description: plan.description ?? '',
    visibility: plan.visibility,
    groupIds: [...plan.visible_group_ids],
    sortOrder: String(plan.sort_order),
    gb: traffic === null ? '' : String(Math.floor(traffic / GiB)),
    devices: str(cur?.max_devices ?? null),
    strategy: cur?.quota_reset_strategy ?? 'billing_cycle',
    resetDay: str(cur?.quota_reset_day ?? null),
    prices: publicPrices(plan.prices).map((p) => ({
      key: p.id,
      amount: yuan(p.unit_amount),
      currency: p.currency,
      period: periodKey(p.billing_interval, p.interval_count),
      trialDays: p.trial_days ? String(p.trial_days) : '',
    })),
    poolIds: [...(cur?.pool_ids ?? [])],
    allowNew: plan.allow_new_purchase,
    allowRenewal: plan.allow_renewal,
    allowUpgrade: plan.allow_upgrade,
    purchaseLimit: str(plan.purchase_limit_per_user),
    stockTotal: str(plan.stock_total),
    publish: plan.status === 'active',
  }
}

/** 向导管得到的价格：在售、非用户组专属（组价与时间窗只在详情价格卡里管） */
const publicPrices = (prices: readonly PriceRow[]) => prices.filter((p) => p.status === 'active' && p.user_group_id === null)

const CODE_RE = /^[a-z0-9][a-z0-9_-]{1,63}$/

/**
 * 与 Go 的 validatePlanFields / validateWizardInput 同规则，键名一致。
 * edit 时 original 是打开向导时的值：后端「null = 不动」表达不了把设备数改回不限（0 过不了正整数校验）。
 */
export function wizardProblems(f: WizardForm, mode: 'new' | 'edit', original?: WizardForm): Fields {
  const out: Fields = {}
  if (!CODE_RE.test(f.code.trim())) out.code = '需为 2-64 位小写字母、数字、下划线或连字符，首位不能是符号'
  const name = [...f.name.trim()].length
  if (name < 1 || name > 120) out.name = '必填且最多 120 个字符'
  if (f.visibility === 'group' && !f.groupIds.length) out.visible_group_ids = '分组可见套餐至少需要一个用户组'
  if (!/^-?\d+$/.test(f.sortOrder.trim())) out.sort_order = '填整数，越小越靠前'
  const gb = parseCount(f.gb)
  if (isBad(gb)) out.traffic_gb = '填整数 GB；不限流量请留空'
  const devices = parseCount(f.devices)
  if (isBad(devices) || devices === 0) out.max_devices = '必须为正整数；不限请留空'
  else if (mode === 'edit' && devices === null && original && original.devices !== '') out.max_devices = '编辑向导不能把设备数改回不限；需要时新建版本，在版本里清空设备上限'
  if (mode === 'new' && f.strategy === 'fixed_day') {
    const d = parseCount(f.resetDay)
    if (d === null || Number.isNaN(d) || d < 1 || d > 28) out.quota_reset_day = '固定日必须为 1-28'
  }
  const seen = new Map<string, number>()
  f.prices.forEach((p, i) => {
    const amount = parseAmount(p.amount)
    if (amount === null || amount <= 0) out[`prices.${i}.unit_amount`] = `第 ${i + 1} 档价格要大于 0`
    if (isBad(parseCount(p.trialDays))) out[`prices.${i}.trial_days`] = '试用天数填 0 或正整数'
    const key = `${p.period}/${p.currency}`
    const prev = seen.get(key)
    if (prev !== undefined) out[`prices.${i}.billing_interval`] = `第 ${i + 1} 档和第 ${prev} 档的周期与币种完全相同`
    seen.set(key, i + 1)
  })
  if (mode === 'new' && f.publish) {
    if (!f.prices.length) out.prices = '要上架就至少得有一档价格，否则用户看得到却买不了'
    if (!f.poolIds.length) out.pool_ids = '要上架就得选节点池，否则买了也没有线路可用'
    if (f.visibility === 'invite_only') out.visibility = '「仅邀请」可见的套餐不能发布'
  }
  const limit = parseCount(f.purchaseLimit)
  if (isBad(limit) || limit === 0) out.purchase_limit_per_user = '必须为正整数；不限请留空'
  if (isBad(parseCount(f.stockTotal))) out.stock_total = '填 0 或正整数；不限请留空'
  return out
}

const STEP_OF: ReadonlyArray<[number, RegExp]> = [
  [1, /^(name|code|description|visibility|visible_group_ids|sort_order)$/],
  [2, /^(traffic_gb|max_devices|throttle_kbps|quota_reset_strategy|quota_reset_day)$/],
  [3, /^prices(\.|$)/],
  [4, /^pool_ids$/],
  [5, /^(purchase_limit_per_user|stock_total|allow_\w+|publish)$/],
]

/** 后端或前端的 fields 键落在第几步；认不出的为 null（页面改用 Toast） */
export function stepOfField(key: string): number | null {
  return STEP_OF.find(([, re]) => re.test(key))?.[0] ?? null
}

const pricesBody = (prices: readonly WizardPrice[]) =>
  prices.map((p) => ({ ...parsePeriod(p.period), unit_amount: parseAmount(p.amount) ?? 0, currency: p.currency, trial_days: parseCount(p.trialDays) ?? 0 }))

const planBasics = (f: WizardForm) => ({
  code: f.code.trim(),
  name: f.name.trim(),
  description: f.description.trim() || null,
  visibility: f.visibility,
  sort_order: Number(f.sortOrder.trim()),
  visible_group_ids: f.visibility === 'group' ? f.groupIds : [],
  purchase_limit_per_user: parseCount(f.purchaseLimit),
  stock_total: parseCount(f.stockTotal),
})

/** POST v1/plans/complete；流量 / 设备留空 = 不限（发 null，不发 0：设备 0 会被版本校验拒绝） */
export function createBody(f: WizardForm) {
  const devices = parseCount(f.devices)
  return {
    ...planBasics(f),
    allow_new_purchase: f.allowNew,
    allow_renewal: f.allowRenewal,
    allow_upgrade: f.allowUpgrade,
    traffic_gb: parseCount(f.gb) || null,
    max_devices: devices || null,
    quota_reset_strategy: f.strategy,
    ...(f.strategy === 'fixed_day' ? { quota_reset_day: parseCount(f.resetDay) } : {}),
    pool_ids: f.poolIds,
    prices: pricesBody(f.prices),
    publish: f.publish,
  }
}

const sameList = (a: readonly string[], b: readonly string[]) => a.length === b.length && [...a].sort().join() === [...b].sort().join()
const priceSig = (prices: readonly WizardPrice[]) =>
  pricesBody(prices)
    .map((p) => `${p.billing_interval}:${p.interval_count}:${p.currency}:${p.unit_amount}:${p.trial_days}`)
    .sort()
    .join('|')

/**
 * PUT v1/plans/{id}/complete（修订 R1）：基本资料每次整体覆盖；额度、价格、线路没改就发 null（= 不动），
 * 改了才发——额度或线路一变后端就开新版本并立即发布。价格只同步清单里出现的币种的公开价。
 */
export function updateBody(f: WizardForm, original: WizardForm, rowVersion: number) {
  const gb = parseCount(f.gb)
  const devices = parseCount(f.devices)
  return {
    expected_row_version: rowVersion,
    ...planBasics(f),
    allow_new_purchase: f.allowNew,
    allow_renewal: f.allowRenewal,
    allow_upgrade: f.allowUpgrade,
    // 流量清空 = 改为不限，后端认 0
    traffic_gb: f.gb.trim() === original.gb.trim() ? null : (gb ?? 0),
    max_devices: f.devices.trim() === original.devices.trim() ? null : devices,
    prices: priceSig(f.prices) === priceSig(original.prices) ? null : pricesBody(f.prices),
    pool_ids: sameList(f.poolIds, original.poolIds) ? null : f.poolIds,
  }
}

/** 编辑时整币种删光的价格不会被归档（后端只同步清单里出现的币种），要提示去价格卡里归档 */
export function removedCurrencies(f: WizardForm, original: WizardForm): Currency[] {
  const had = new Set(original.prices.map((p) => p.currency))
  const has = new Set(f.prices.map((p) => p.currency))
  return [...had].filter((c) => !has.has(c))
}

// ===========================================================================
// 销售设置：PUT v1/plans/{id} 整体覆盖（三个 allow_* 是 bool）
// ===========================================================================
export interface SalesForm {
  visibility: Visibility
  groupIds: string[]
  from: string
  until: string
  allowNew: boolean
  allowRenewal: boolean
  allowUpgrade: boolean
  purchaseLimit: string
  stockTotal: string
  sortOrder: string
}

/** RFC3339 → <input type="datetime-local"> 的本地时间值 */
export function toLocalInput(at: string | null): string {
  if (!at) return ''
  const d = new Date(at)
  if (Number.isNaN(d.getTime())) return ''
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
}

export function fromLocalInput(value: string): string | null {
  if (!value) return null
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? null : d.toISOString()
}

export function salesForm(p: PlanDetail): SalesForm {
  return {
    visibility: p.visibility,
    groupIds: [...p.visible_group_ids],
    from: toLocalInput(p.visible_from),
    until: toLocalInput(p.visible_until),
    allowNew: p.allow_new_purchase,
    allowRenewal: p.allow_renewal,
    allowUpgrade: p.allow_upgrade,
    purchaseLimit: str(p.purchase_limit_per_user),
    stockTotal: str(p.stock_total),
    sortOrder: String(p.sort_order),
  }
}

export function salesProblems(f: SalesForm, stockReserved: number): Fields {
  const out: Fields = {}
  if (f.visibility === 'group' && !f.groupIds.length) out.visible_group_ids = '分组可见套餐至少需要一个用户组'
  const from = fromLocalInput(f.from)
  const until = fromLocalInput(f.until)
  if (from && until && until <= from) out.visible_until = '必须晚于上架时间'
  const limit = parseCount(f.purchaseLimit)
  if (isBad(limit) || limit === 0) out.purchase_limit_per_user = '必须为正整数；不限请留空'
  const stock = parseCount(f.stockTotal)
  if (isBad(stock)) out.stock_total = '填 0 或正整数；不限请留空'
  else if (stock !== null && stock < stockReserved) out.stock_total = `不能低于已预留库存 ${stockReserved}`
  if (!/^-?\d+$/.test(f.sortOrder.trim())) out.sort_order = '填整数，越小越靠前'
  return out
}

export function salesBody(f: SalesForm, p: PlanDetail) {
  return {
    expected_row_version: p.row_version,
    code: p.code,
    name: p.name,
    description: p.description,
    visibility: f.visibility,
    visible_group_ids: f.visibility === 'group' ? f.groupIds : [],
    visible_from: fromLocalInput(f.from),
    visible_until: fromLocalInput(f.until),
    allow_new_purchase: f.allowNew,
    allow_renewal: f.allowRenewal,
    allow_upgrade: f.allowUpgrade,
    purchase_limit_per_user: parseCount(f.purchaseLimit),
    stock_total: parseCount(f.stockTotal),
    sort_order: Number(f.sortOrder.trim()),
  }
}

// ===========================================================================
// 新增价格：POST v1/plans/{id}/prices（周期可自定义；「高级」里是试用、用户组、时间窗）
// ===========================================================================
export interface PriceForm {
  currency: Currency
  amount: string
  period: string
  customCount: string
  customUnit: BillingInterval
  trialDays: string
  groupId: string
  from: string
  until: string
}

export const emptyPriceForm = (): PriceForm => ({ currency: 'CNY', amount: '', period: 'month:1', customCount: '', customUnit: 'day', trialDays: '', groupId: '', from: '', until: '' })

export function priceProblems(f: PriceForm): Fields {
  const out: Fields = {}
  if (parseAmount(f.amount) === null) out.unit_amount = '填金额（元），最多两位小数'
  if (f.period === 'custom') {
    const n = parseCount(f.customCount)
    if (n === null || Number.isNaN(n) || n < 1 || n > 32767) out.interval_count = '填正整数'
  }
  if (isBad(parseCount(f.trialDays))) out.trial_days = '填 0 或正整数'
  const from = fromLocalInput(f.from)
  const until = fromLocalInput(f.until)
  if (from && until && until <= from) out.valid_until = '必须晚于生效时间'
  return out
}

export function priceBody(f: PriceForm) {
  const period = f.period === 'custom' ? { billing_interval: f.customUnit, interval_count: parseCount(f.customCount) ?? 1 } : parsePeriod(f.period)
  return {
    currency: f.currency,
    unit_amount: parseAmount(f.amount) ?? 0,
    ...period,
    trial_days: parseCount(f.trialDays) ?? 0,
    user_group_id: f.groupId || null,
    valid_from: fromLocalInput(f.from),
    valid_until: fromLocalInput(f.until),
  }
}

// ===========================================================================
// 流量包（修订 R73）
// ===========================================================================
export const PACK_STATUS_VIEW = { active: { label: '在售', tone: 'ok' }, archived: { label: '已下架', tone: 'neutral' } } as const satisfies Record<string, { label: string; tone: Tone }>

export interface PackForm {
  name: string
  gb: string
  currency: Currency
  amount: string
  recommended: boolean
  sortOrder: string
}

export function packForm(p?: TrafficPack): PackForm {
  if (!p) return { name: '', gb: '', currency: 'CNY', amount: '', recommended: false, sortOrder: '0' }
  return { name: p.name, gb: String(Math.round((p.traffic_bytes / GiB) * 100) / 100), currency: p.currency, amount: yuan(p.unit_amount), recommended: p.recommended, sortOrder: String(p.sort_order) }
}

/** GB 可带两位小数 → 字节取整 */
function packBytes(gb: string): number | null {
  const text = gb.trim()
  if (!/^\d+(\.\d{1,2})?$/.test(text)) return null
  const bytes = Math.round(Number(text) * GiB)
  return bytes > 0 ? bytes : null
}

/** 与 adminops.validatePackInput 同规则、同键名 */
export function packProblems(f: PackForm): Fields {
  const out: Fields = {}
  const name = [...f.name.trim()].length
  if (name < 1 || name > 60) out.name = '名称 1 到 60 个字'
  const bytes = packBytes(f.gb)
  if (bytes === null || bytes > 2 ** 50) out.traffic_bytes = '容量必须大于 0，且不超过 1 PiB'
  const amount = parseAmount(f.amount)
  if (amount === null || amount < 1 || amount > 100_000_000) out.unit_amount = '价格必须大于 0，且不超过 100 万'
  const sort = f.sortOrder.trim()
  if (!/^-?\d+$/.test(sort) || Math.abs(Number(sort)) > 1_000_000) out.sort_order = '排序号填 -1000000 到 1000000 的整数'
  return out
}

export function packBody(f: PackForm) {
  return {
    name: f.name.trim(),
    traffic_bytes: packBytes(f.gb) ?? 0,
    currency: f.currency,
    unit_amount: parseAmount(f.amount) ?? 0,
    recommended: f.recommended,
    sort_order: Number(f.sortOrder.trim()),
  }
}

/** 列表行的一句话摘要（卡片副行、确认框里用） */
export function planFacts(row: Pick<PlanRow, 'traffic_limit' | 'max_devices' | 'prices' | 'version'>): { price: string; quota: string } {
  // 列表的额度取自当前发布版本：还没发布过时两列都是 null，不能读成「不限」
  return { price: priceFrom(row.prices), quota: row.version === null ? '未发布' : quotaLabel(row.traffic_limit, row.max_devices) }
}
