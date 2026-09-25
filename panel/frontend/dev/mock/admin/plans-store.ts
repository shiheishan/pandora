/**
 * [INPUT]: 依赖 node:crypto 的 randomUUID，依赖 ../types 的 Json / MockResult，依赖 ./users 的 PLAN_IDS / GROUPS / activeSubscriptions，依赖 ./nodes-infra 的 pools（只读）与 activeNodesInPool（在线节点数与节点池列表同口径）
 * [OUTPUT]: 对外提供套餐假接口的存储与规则：Plan 类型、种子目录 plans 与查找（find、currentOf、draftOf、trafficOf）、行形状（listRow、priceRow、detail）、与 Go 同键名同文案的校验（planFieldProblems、semanticsProblems、salesPointProblems、priceProblems、poolProblems、wizardPriceProblems、publishProblems）、写入小件（publish、applySemantics、applyBasics、applySalesPoints、blankVersion、quotasFor、seedPlan、seedPrice、newPrice、priceKey、touch）、在线节点数 activeNodes、工具（GiB、UUID、isInt、str）、错误结果（err、invalid、NOT_FOUND、DUP、stale、SALES_OFF、unknownField）与销售开关（sales、setSalesEnabled）
 * [POS]: dev/mock/admin 的「套餐（后台-04）」数据层，plans.ts 的路由与向导都经它读写。种子五个套餐沿用 users.ts 的固定套餐 id（批量筛选按套餐能命中）与用户组 id，节点池沿用 nodes-infra.ts 的池（id 一致），在线节点数是这里的固定值、不与节点假后端联动（企业专线为 0，演示「空订阅」警示）；有效订阅按 users.ts 的种子订阅实时数（active / trialing）；标准版 v1 是 R99 之前的「用完限速」存量行，专业版当前版本限速 300 Mbps、带卖点并标为推荐（R100）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomUUID } from 'node:crypto'
import type { Json, MockResult } from '../types.ts'
import { activeNodesInPool, pools } from './nodes-infra.ts'
import { activeSubscriptions, GROUPS, PLAN_IDS } from './users.ts'

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------
const DAY = 86_400_000
export const GiB = 1024 ** 3
export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const CODE = /^[a-z0-9][a-z0-9_-]{1,63}$/
const iso = (msAgo: number) => new Date(Date.now() - msAgo).toISOString()
export const err = (status: number, code: string, message: string, fields?: Record<string, string>): MockResult => ({ status, body: { error: { code, message, ...(fields ? { fields } : {}) } } })
export const invalid = (fields: Record<string, string>) => err(422, 'validation_failed', '请求参数校验未通过', fields)
export const NOT_FOUND = err(404, 'not_found', '资源不存在或无权访问')
export const DUP = err(409, 'conflict', '目录对象已存在或活动报价发生冲突')
export const stale = (current: number, kind = '套餐') => err(409, 'conflict', `${kind}已被其他管理员修改，请刷新后重试`, { row_version: `current=${current}` })
export const isInt = (v: unknown): v is number => typeof v === 'number' && Number.isInteger(v)
export const str = (v: unknown) => (typeof v === 'string' ? v : '')

/** 与后端 DisallowUnknownFields 一致：多一个字段就 400 */
export function unknownField(body: Json, allowed: readonly string[]): MockResult | null {
  const extra = Object.keys(body).find((k) => !allowed.includes(k))
  return extra ? err(400, 'bad_request', `请求体包含未知字段 "${extra}"`) : null
}

// 销售开关（AEGIS_SALES_ENABLED）：dev 默认开，tests/mock-admin-plans.test.ts 关掉它验 503
let salesEnabled = true
export function setSalesEnabled(on: boolean): void {
  salesEnabled = on
}
export const sales = () => salesEnabled
export const SALES_OFF = err(503, 'service_unavailable', '服务暂时不可用')

// 在线节点数：与节点池列表的 active_nodes 同一口径，取节点假后端的同一份节点表（节点页改了状态，套餐这边跟着变）
export const activeNodes = activeNodesInPool
const poolId = (code: string) => pools.find((p) => p.code === code)?.id ?? randomUUID()
const livePool = (id: string) => pools.some((p) => p.id === id && p.status !== 'disabled')

// ---------------------------------------------------------------------------
// 存储
// ---------------------------------------------------------------------------
interface Quota {
  metric: string
  limit: number | null
  unit: string
  period: string
}
interface Version {
  id: string
  version: number
  status: 'draft' | 'published' | 'retired'
  frozen_at: string | null
  row_version: number
  quota_reset_strategy: string
  quota_reset_day: number | null
  grace_period_hours: number
  grace_keeps_service: boolean
  renewal_extends_period: boolean
  renewal_resets_quota: boolean
  renewal_keeps_addons: boolean
  max_devices: number | null
  max_concurrent: number | null
  device_release_hours: number
  overage_policy: string
  throttle_kbps: number | null
  notes: string | null
  entitlements: Array<{ code: string; value: unknown }>
  quotas: Quota[]
  pool_ids: string[]
  created_by_email: string | null
  created_at: string
}
interface Price {
  id: string
  currency: string
  unit_amount: number
  billing_interval: string
  interval_count: number
  trial_days: number
  status: 'active' | 'archived'
  user_group_id: string | null
  valid_from: string | null
  valid_until: string | null
  row_version: number
  created_at: string
}
export interface Plan {
  id: string
  product_id: string
  row_version: number
  code: string
  name: string
  description: string | null
  status: 'draft' | 'active' | 'archived'
  visibility: string
  visible_group_ids: string[]
  visible_from: string | null
  visible_until: string | null
  allow_new_purchase: boolean
  allow_renewal: boolean
  allow_upgrade: boolean
  purchase_limit_per_user: number | null
  stock_total: number | null
  stock_reserved: number
  sort_order: number
  highlights: string[]
  recommended: boolean
  current_version_id: string | null
  versions: Version[]
  prices: Price[]
  created_at: string
}

/** 版本语义的库默认值（POST versions 建出来就是这样，后端不复制当前版本） */
export function blankVersion(n: number, email: string | null): Version {
  return {
    id: randomUUID(),
    version: n,
    status: 'draft',
    frozen_at: null,
    row_version: 1,
    quota_reset_strategy: 'billing_cycle',
    quota_reset_day: null,
    grace_period_hours: 0,
    grace_keeps_service: true,
    renewal_extends_period: true,
    renewal_resets_quota: true,
    renewal_keeps_addons: true,
    max_devices: null,
    max_concurrent: null,
    device_release_hours: 0,
    overage_policy: 'suspend',
    throttle_kbps: null,
    notes: null,
    entitlements: [],
    quotas: [],
    pool_ids: [],
    created_by_email: email,
    created_at: new Date().toISOString(),
  }
}

export function quotasFor(gb: number | null, devices: number | null): Quota[] {
  const q: Quota[] = []
  if (gb) q.push({ metric: 'traffic.bytes', limit: gb * GiB, unit: 'bytes', period: 'cycle' })
  if (devices) q.push({ metric: 'devices.active', limit: devices, unit: 'count', period: 'cycle' })
  return q
}

function seedVersion(n: number, status: Version['status'], gb: number, devices: number, poolCodes: string[], daysAgo: number, email: string | null = 'ops@pandora.dev'): Version {
  return {
    ...blankVersion(n, email),
    status,
    frozen_at: status === 'draft' ? null : iso(daysAgo * DAY),
    row_version: status === 'draft' ? 2 : 3,
    grace_period_hours: 24,
    max_devices: devices,
    quotas: quotasFor(gb, devices),
    pool_ids: poolCodes.map(poolId),
    entitlements: status === 'draft' ? [] : [{ code: 'streaming', value: true }],
    created_at: iso((daysAgo + 2) * DAY),
  }
}

export function seedPrice(currency: string, amount: number, interval: string, count: number, daysAgo: number, over: Partial<Price> = {}): Price {
  return {
    id: randomUUID(),
    currency,
    unit_amount: amount,
    billing_interval: interval,
    interval_count: count,
    trial_days: 0,
    status: 'active',
    user_group_id: null,
    valid_from: null,
    valid_until: null,
    row_version: 1,
    created_at: iso(daysAgo * DAY),
    ...over,
  }
}

export function seedPlan(id: string, code: string, name: string, description: string, status: Plan['status'], sort: number, versions: Version[], prices: Price[], over: Partial<Plan> = {}): Plan {
  const current = versions.find((v) => v.status === 'published' && v.frozen_at && !versions.some((w) => w.status === 'published' && w.version > v.version))
  return {
    id,
    product_id: randomUUID(),
    row_version: 5,
    code,
    name,
    description,
    status,
    visibility: 'public',
    visible_group_ids: [],
    visible_from: null,
    visible_until: null,
    allow_new_purchase: status !== 'archived',
    allow_renewal: true,
    allow_upgrade: true,
    purchase_limit_per_user: null,
    stock_total: null,
    stock_reserved: 0,
    sort_order: sort,
    highlights: [],
    recommended: false,
    current_version_id: current?.id ?? null,
    versions: [...versions].sort((a, b) => b.version - a.version),
    prices: [...prices].sort((a, b) => b.created_at.localeCompare(a.created_at)),
    created_at: iso(400 * DAY),
    ...over,
  }
}

const VIP = GROUPS[0]!.id
const ENTERPRISE = GROUPS[1]!.id
export const plans: Plan[] = [
  seedPlan(
    PLAN_IDS[0]!,
    'std',
    '标准版',
    '日常浏览与流媒体，适合个人单设备到三设备。',
    'active',
    10,
    // v1 是 R99 之前的存量写法（「用完限速」策略），读取照收、保存时改回 suspend
    [{ ...seedVersion(1, 'published', 150, 3, ['global'], 200), overage_policy: 'throttle', throttle_kbps: 5000 }, seedVersion(2, 'published', 200, 3, ['global'], 55)],
    [
      seedPrice('CNY', 2000, 'month', 1, 300, { status: 'archived', row_version: 2 }),
      seedPrice('CNY', 2500, 'month', 1, 60),
      seedPrice('CNY', 6900, 'month', 3, 60),
      seedPrice('USD', 390, 'month', 1, 40),
    ],
    { highlights: ['全部常规线路', '工单支持'] },
  ),
  seedPlan(
    PLAN_IDS[1]!,
    'pro',
    '专业版',
    '高流量与低延迟线路，包含亚太精选与全部线路。',
    'active',
    20,
    [
      seedVersion(1, 'published', 400, 5, ['global'], 260),
      { ...seedVersion(2, 'published', 500, 5, ['asia', 'global'], 100), throttle_kbps: 300_000 },
      seedVersion(3, 'draft', 600, 5, ['asia', 'global'], 1, 'zhou.min@pandora.dev'),
    ],
    [
      seedPrice('CNY', 4500, 'month', 1, 120),
      seedPrice('CNY', 45000, 'year', 1, 120, { trial_days: 3 }),
      seedPrice('USD', 690, 'month', 1, 90),
      seedPrice('CNY', 3900, 'month', 1, 30, { user_group_id: VIP }),
    ],
    { stock_total: 500, stock_reserved: 3, highlights: ['亚太精选 + 欧美线路', '流媒体解锁', '工单优先处理'], recommended: true },
  ),
  seedPlan(PLAN_IDS[2]!, 'family', '家庭版', '多设备共享，适合家庭与小团队。', 'active', 30, [seedVersion(1, 'published', 1000, 8, ['global'], 140)], [seedPrice('CNY', 6800, 'month', 1, 140)], {
    visible_until: new Date(Date.now() + 45 * DAY).toISOString(),
    purchase_limit_per_user: 2,
  }),
  seedPlan(PLAN_IDS[3]!, 'trial', '体验版', '新用户 3 天体验。', 'archived', 40, [seedVersion(1, 'published', 20, 1, ['global'], 320)], [seedPrice('CNY', 0, 'one_time', 1, 320)]),
  seedPlan(
    '9c0e1a2b-3333-4b00-8000-000000000005',
    'ent-line',
    '企业专线',
    '对公结算，专属线路与 SLA。',
    'draft',
    50,
    [seedVersion(1, 'draft', 2000, 20, ['enterprise'], 3)],
    [seedPrice('CNY', 199900, 'month', 1, 3)],
    { visibility: 'group', visible_group_ids: [ENTERPRISE], allow_new_purchase: true },
  ),
]

export const find = (id: string) => plans.find((p) => p.id === id)
export const currentOf = (p: Plan) => p.versions.find((v) => v.id === p.current_version_id)
export const draftOf = (p: Plan) => p.versions.find((v) => v.status === 'draft')
export const trafficOf = (v: Version | undefined) => v?.quotas.find((q) => q.metric === 'traffic.bytes')?.limit ?? null
export const touch = (x: { row_version: number }) => void (x.row_version += 1)

export function listRow(p: Plan) {
  const cur = currentOf(p)
  const draft = draftOf(p)
  return {
    id: p.id,
    product_id: p.product_id,
    row_version: p.row_version,
    code: p.code,
    name: p.name,
    description: p.description,
    status: p.status,
    visibility: p.visibility,
    sort_order: p.sort_order,
    current_version_id: p.current_version_id,
    draft_version_id: draft?.id ?? null,
    version: cur?.version ?? null,
    max_devices: cur?.max_devices ?? null,
    traffic_limit: trafficOf(cur),
    prices: p.prices.map(priceRow),
    active_subscriptions: activeSubscriptions(p.id),
    node_count: cur ? cur.pool_ids.reduce((n, id) => n + activeNodes(id), 0) : 0,
    highlights: p.highlights,
    recommended: p.recommended,
  }
}

/** 接口形状里没有 created_at（只在这里用于排序） */
export function priceRow(x: Price) {
  const row: Partial<Price> = { ...x }
  delete row.created_at
  return row
}

export function detail(p: Plan) {
  const rest: Partial<Plan> = { ...p }
  delete rest.created_at
  return { ...rest, prices: p.prices.map(priceRow) }
}

// ---------------------------------------------------------------------------
// 校验：与 Go 同键名、同文案
// ---------------------------------------------------------------------------
const VISIBILITIES = ['public', 'authenticated', 'group', 'invite_only', 'hidden']
const INTERVALS = ['day', 'week', 'month', 'quarter', 'year', 'one_time']

/** validatePlanFields */
export function planFieldProblems(b: Json): Record<string, string> {
  const f: Record<string, string> = {}
  if (!CODE.test(str(b.code).trim())) f.code = '需为 2-64 位小写字母、数字、下划线或连字符'
  const name = [...str(b.name).trim()].length
  if (name < 1 || name > 120) f.name = '必填且最多 120 个字符'
  const vis = str(b.visibility)
  if (!VISIBILITIES.includes(vis)) f.visibility = '不支持的可见性'
  const groups = Array.isArray(b.visible_group_ids) ? (b.visible_group_ids as unknown[]) : []
  if (
    b.visible_group_ids !== undefined &&
    b.visible_group_ids !== null &&
    (!Array.isArray(b.visible_group_ids) || groups.some((g) => typeof g !== 'string' || !UUID.test(g)) || new Set(groups).size !== groups.length)
  )
    f.visible_group_ids = '必须是无重复的 UUID 列表'
  else if (vis === 'group' && !groups.length) f.visible_group_ids = '分组可见套餐至少需要一个用户组'
  else if (vis !== 'group' && groups.length) f.visible_group_ids = '只有分组可见套餐可以设置用户组'
  const from = b.visible_from ? Date.parse(str(b.visible_from)) : null
  const until = b.visible_until ? Date.parse(str(b.visible_until)) : null
  if (from !== null && until !== null && until <= from) f.visible_until = '必须晚于 visible_from'
  if (b.purchase_limit_per_user != null && !(isInt(b.purchase_limit_per_user) && b.purchase_limit_per_user > 0)) f.purchase_limit_per_user = '必须为正整数'
  if (b.stock_total != null && !(isInt(b.stock_total) && b.stock_total >= 0)) f.stock_total = '不能为负数'
  return f
}

/** validateVersionSemantics（不含 pool_ids，那是专用接口的事） */
export function semanticsProblems(b: Json): Record<string, string> {
  const f: Record<string, string> = {}
  const strategy = str(b.quota_reset_strategy)
  if (!['never', 'natural_month', 'billing_cycle', 'fixed_day'].includes(strategy)) f.quota_reset_strategy = '不支持的重置策略'
  if (strategy === 'fixed_day' && !(isInt(b.quota_reset_day) && b.quota_reset_day >= 1 && b.quota_reset_day <= 28)) f.quota_reset_day = '固定日必须为 1-28'
  if (strategy !== 'fixed_day' && b.quota_reset_day != null) f.quota_reset_day = '非固定日策略不能设置该字段'
  if (!(isInt(b.grace_period_hours) && b.grace_period_hours >= 0)) f.grace_period_hours = '不能为负数'
  if (b.max_devices != null && !(isInt(b.max_devices) && b.max_devices > 0)) f.max_devices = '必须为正整数'
  if (b.max_concurrent != null && !(isInt(b.max_concurrent) && b.max_concurrent > 0)) f.max_concurrent = '必须为正整数'
  if (!(isInt(b.device_release_hours) && b.device_release_hours >= 0)) f.device_release_hours = '不能为负数'
  // R99：新写入的超额策略只收 suspend（省略按 suspend），限速与策略解耦，只校验 null 或正整数
  if (b.overage_policy !== undefined && b.overage_policy !== 'suspend') f.overage_policy = '超额策略只支持 suspend（流量用完后停止服务）'
  if (b.throttle_kbps != null && !(isInt(b.throttle_kbps) && b.throttle_kbps > 0)) f.throttle_kbps = '必须为正整数'
  const quotas = Array.isArray(b.quotas) ? (b.quotas as Json[]) : []
  quotas.forEach((q, i) => {
    if (!q || typeof q.metric !== 'string' || !q.metric) f[`quotas.${i}`] = '缺少 metric'
    else if (!['total', 'cycle', 'day', 'month'].includes(str(q.period))) f[`quotas.${i}.period`] = '不支持的周期'
    else if (q.limit != null && !(isInt(q.limit) && q.limit >= 0)) f[`quotas.${i}.limit`] = '不能为负数'
  })
  const ents = Array.isArray(b.entitlements) ? (b.entitlements as Json[]) : []
  ents.forEach((e, i) => {
    if (!e || typeof e.code !== 'string' || !e.code) f[`entitlements.${i}`] = '缺少 code'
  })
  return f
}

/** R100 卖点：数组、最多 5 条、每条去首尾空白后 1–40 字、不许重复；recommended 必须是 bool。只校验出现了的字段 */
export function salesPointProblems(b: Json): Record<string, string> {
  const f: Record<string, string> = {}
  if (b.recommended !== undefined && typeof b.recommended !== 'boolean') f.recommended = '必须是布尔值'
  if (b.highlights === undefined) return f
  if (!Array.isArray(b.highlights) || b.highlights.some((h) => typeof h !== 'string')) return { ...f, highlights: '必须是字符串数组' }
  const list = (b.highlights as string[]).map((h) => h.trim())
  if (list.length > 5) f.highlights = '最多 5 条'
  list.forEach((h, i) => {
    const n = [...h].length
    if (n < 1 || n > 40) f[`highlights.${i}`] = '每条 1-40 个字'
    else if (list.indexOf(h) !== i) f[`highlights.${i}`] = '不能重复'
  })
  return f
}

/** 写入出现了的卖点与推荐（按去空白后的顺序） */
export function applySalesPoints(p: Plan, b: Json): void {
  if (Array.isArray(b.highlights)) p.highlights = (b.highlights as string[]).map((h) => h.trim())
  if (typeof b.recommended === 'boolean') p.recommended = b.recommended
}

/** validatePriceInput */
export function priceProblems(b: Json): Record<string, string> {
  const f: Record<string, string> = {}
  if (!['CNY', 'USD'].includes(str(b.currency))) f.currency = '仅允许 CNY 或 USD'
  if (!(isInt(b.unit_amount) && b.unit_amount >= 0)) f.unit_amount = '不能为负数'
  if (!INTERVALS.includes(str(b.billing_interval))) f.billing_interval = '不支持的计费周期'
  if (!(isInt(b.interval_count) && b.interval_count > 0)) f.interval_count = '必须为正整数'
  if (b.trial_days !== undefined && !(isInt(b.trial_days) && b.trial_days >= 0)) f.trial_days = '不能为负数'
  if (b.user_group_id != null && !(typeof b.user_group_id === 'string' && UUID.test(b.user_group_id))) f.user_group_id = '必须是 UUID'
  else if (b.user_group_id != null && !GROUPS.some((g) => g.id === b.user_group_id)) f.user_group_id = '用户组不存在'
  const from = b.valid_from ? Date.parse(str(b.valid_from)) : null
  const until = b.valid_until ? Date.parse(str(b.valid_until)) : null
  if (from !== null && until !== null && until <= from) f.valid_until = '必须晚于 valid_from'
  return f
}

export function poolProblems(ids: unknown): string | null {
  if (!Array.isArray(ids) || ids.some((x) => typeof x !== 'string' || !UUID.test(x)) || new Set(ids).size !== ids.length || ids.length > 500) return '必须是不重复的节点分组 UUID 列表'
  if (ids.some((x) => !livePool(x as string))) return '包含不存在或已禁用的节点分组'
  return null
}

/** 发布前置条件（publishPlanVersionTx）：可见范围内有当前有效价、池里有可服务节点、仅邀请不能发布 */
export function publishProblems(p: Plan, v: Version): Record<string, string> | null {
  const now = Date.now()
  const live = p.prices.filter((x) => x.status === 'active' && (!x.valid_from || Date.parse(x.valid_from) <= now) && (!x.valid_until || Date.parse(x.valid_until) > now))
  const priced = p.visibility === 'group' ? live.some((x) => x.user_group_id === null || p.visible_group_ids.includes(x.user_group_id)) : live.some((x) => x.user_group_id === null)
  if (!priced) return { prices: '没有覆盖可见范围的当前有效 CNY / USD 价格' }
  if (!v.pool_ids.length || v.pool_ids.every((id) => activeNodes(id) === 0)) return { pool_ids: '没有启用的节点分组，或分组里没有可服务节点' }
  if (p.visibility === 'invite_only') return { visibility: '仅邀请可见的套餐不能发布' }
  return null
}

export function publish(p: Plan, v: Version): void {
  v.status = 'published'
  v.frozen_at = new Date().toISOString()
  touch(v)
  p.current_version_id = v.id
  if (p.status === 'draft') p.status = 'active'
  touch(p)
}

export function applySemantics(v: Version, b: Json): void {
  v.quota_reset_strategy = str(b.quota_reset_strategy)
  v.quota_reset_day = (b.quota_reset_day as number | null) ?? null
  v.grace_period_hours = b.grace_period_hours as number
  v.grace_keeps_service = b.grace_keeps_service === true
  v.renewal_extends_period = b.renewal_extends_period === true
  v.renewal_resets_quota = b.renewal_resets_quota === true
  v.renewal_keeps_addons = b.renewal_keeps_addons === true
  v.max_devices = (b.max_devices as number | null) ?? null
  v.max_concurrent = (b.max_concurrent as number | null) ?? null
  v.device_release_hours = b.device_release_hours as number
  v.overage_policy = str(b.overage_policy) || 'suspend'
  v.throttle_kbps = (b.throttle_kbps as number | null) ?? null
  v.notes = typeof b.notes === 'string' && b.notes.trim() ? b.notes : null
  v.entitlements = (Array.isArray(b.entitlements) ? (b.entitlements as Json[]) : []).map((e) => ({ code: str(e.code), value: e.value ?? null }))
  v.quotas = (Array.isArray(b.quotas) ? (b.quotas as Json[]) : []).map((q) => ({ metric: str(q.metric), limit: (q.limit as number | null) ?? null, unit: str(q.unit), period: str(q.period) }))
}

export function applyBasics(p: Plan, b: Json): void {
  p.code = str(b.code).trim()
  p.name = str(b.name).trim()
  p.description = typeof b.description === 'string' && b.description.trim() ? b.description : null
  p.visibility = str(b.visibility)
  p.visible_group_ids = Array.isArray(b.visible_group_ids) ? (b.visible_group_ids as string[]) : []
  p.purchase_limit_per_user = (b.purchase_limit_per_user as number | null) ?? null
  p.stock_total = (b.stock_total as number | null) ?? null
  p.sort_order = isInt(b.sort_order) ? b.sort_order : 0
}

export const priceKey = (x: Json | Price) => `${String(x.billing_interval)}/${String(x.interval_count)}/${String(x.currency)}`

/** validateWizardInput：向导的价格清单（prices.{i}.x） */
export function wizardPriceProblems(list: Json[]): Record<string, string> {
  const f: Record<string, string> = {}
  const seen = new Map<string, number>()
  list.forEach((x, i) => {
    const n = i + 1
    if (!(isInt(x.unit_amount) && x.unit_amount > 0)) f[`prices.${i}.unit_amount`] = `第 ${n} 档价格要大于 0`
    if (!(isInt(x.interval_count) && x.interval_count > 0)) f[`prices.${i}.interval_count`] = `第 ${n} 档的周期数要大于 0`
    if (!['CNY', 'USD'].includes(str(x.currency))) f[`prices.${i}.currency`] = `第 ${n} 档没有选币种`
    if (!INTERVALS.includes(str(x.billing_interval))) f[`prices.${i}.billing_interval`] = `第 ${n} 档周期不支持`
    const prev = seen.get(priceKey(x))
    if (prev !== undefined) f[`prices.${i}.billing_interval`] = `第 ${n} 档和第 ${prev} 档的周期与币种完全相同`
    seen.set(priceKey(x), n)
  })
  return f
}

export const newPrice = (x: Json): Price =>
  seedPrice(str(x.currency), x.unit_amount as number, str(x.billing_interval), x.interval_count as number, 0, {
    trial_days: isInt(x.trial_days) ? x.trial_days : 0,
    created_at: new Date().toISOString(),
  })
