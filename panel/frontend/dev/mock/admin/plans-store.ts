import { randomUUID } from 'node:crypto'
import type { Json, MockResult } from '../types.ts'
import { activeNodesInPool, pools } from './nodes-infra.ts'
import { activeSubscriptions, GROUPS, PLAN_IDS } from './users.ts'

// ---------------------------------------------------------------------------
// 工具与通用常量
// ---------------------------------------------------------------------------
const DAY = 86_400_000
export const GiB = 1024 ** 3
export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const iso = (msAgo: number) => new Date(Date.now() - msAgo).toISOString()

export const err = (status: number, code: string, message: string, fields?: Record<string, string>): MockResult => ({
  status,
  body: { error: { code, message, ...(fields ? { fields } : {}) } },
})
export const invalid = (fields: Record<string, string>) => err(422, 'validation_failed', '请求参数校验未通过', fields)
export const NOT_FOUND = err(404, 'not_found', '资源不存在或无权访问')
export const DUP = err(409, 'conflict', '目录对象已存在或活动报价发生冲突')
export const stale = (current: number, kind = '套餐') =>
  err(409, 'conflict', `${kind}已被其他管理员修改，请刷新后重试`, { row_version: `current=${current}` })
export const isInt = (v: unknown): v is number => typeof v === 'number' && Number.isInteger(v)
export const str = (v: unknown) => (typeof v === 'string' ? v : '')

/** 与后端 DisallowUnknownFields 一致：多一个字段就 400 */
export function unknownField(body: Json, allowed: readonly string[]): MockResult | null {
  const extra = Object.keys(body).find((k) => !allowed.includes(k))
  return extra ? err(400, 'bad_request', `请求体包含未知字段 "${extra}"`) : null
}

const poolId = (code: string) => pools.find((p) => p.code === code)?.id ?? randomUUID()
const livePool = (id: string) => pools.some((p) => p.id === id && p.status !== 'disabled')

// ---------------------------------------------------------------------------
// 存储类型
// ---------------------------------------------------------------------------
export interface Version {
  id: string
  version: number
  status: 'draft' | 'published'
  published_at: string | null
  traffic_bytes: number | null
  max_devices: number | null
  throttle_kbps: number | null
  created_by_email: string | null
  created_at: string
}

export interface Price {
  id: string
  billing_interval: 'month' | 'year' | 'one_time'
  interval_count: number
  unit_amount: number // 整数分
  validity_days: number | null
  status: 'active' | 'archived'
  created_at: string
}

export interface Plan {
  id: string
  row_version: number
  code: string
  name: string
  description: string | null
  kind: 'subscription' | 'one_time'
  status: 'draft' | 'active' | 'archived'
  visibility: 'public' | 'authenticated' | 'group' | 'hidden'
  visible_group_ids: string[]
  allow_new_purchase: boolean
  allow_renewal: boolean
  allow_upgrade: boolean
  recommended: boolean
  reset_price: number | null
  reset_limit: number | null
  current_version_id: string | null
  pool_ids: string[]
  versions: Version[] // version 降序
  prices: Price[] // created_at 降序
  created_at: string
}

// ---------------------------------------------------------------------------
// 辅助导出
// ---------------------------------------------------------------------------
export const find = (id: string) => plans.find((p) => p.id === id)
export const currentOf = (p: Plan) => p.versions.find((v) => v.id === p.current_version_id)
export const draftOf = (p: Plan) => p.versions.find((v) => v.status === 'draft')
export const trafficOf = (v: Version | undefined): number | null => v?.traffic_bytes ?? null

// ---------------------------------------------------------------------------
// 种子数据
// ---------------------------------------------------------------------------
function seedPrice(
  interval: 'month' | 'year' | 'one_time',
  count: number,
  amount: number,
  daysAgo: number,
  over: Partial<Price> = {},
): Price {
  return {
    id: randomUUID(),
    billing_interval: interval,
    interval_count: count,
    unit_amount: amount,
    validity_days: null,
    status: 'active',
    created_at: iso(daysAgo * DAY),
    ...over,
  }
}

export const plans: Plan[] = [
  // 0: 标准版 standard
  {
    id: PLAN_IDS[0]!,
    row_version: 5,
    code: 'standard',
    name: '标准版',
    description: '日常浏览与流媒体，适合个人单设备到三设备。',
    kind: 'subscription',
    status: 'active',
    visibility: 'public',
    visible_group_ids: [],
    allow_new_purchase: true,
    allow_renewal: true,
    allow_upgrade: true,
    recommended: true,
    reset_price: 1000,
    reset_limit: 2,
    current_version_id: '9c0e1a2b-4444-4b00-8000-000000000102',
    pool_ids: [poolId('global')],
    versions: [
      {
        id: '9c0e1a2b-4444-4b00-8000-000000000102',
        version: 2,
        status: 'published',
        published_at: iso(55 * DAY),
        traffic_bytes: 200 * GiB,
        max_devices: 3,
        throttle_kbps: null,
        created_by_email: 'ops@example.test',
        created_at: iso(57 * DAY),
      },
      {
        id: '9c0e1a2b-4444-4b00-8000-000000000101',
        version: 1,
        status: 'published',
        published_at: iso(200 * DAY),
        traffic_bytes: 150 * GiB,
        max_devices: 3,
        throttle_kbps: 5000,
        created_by_email: 'ops@example.test',
        created_at: iso(202 * DAY),
      },
    ],
    prices: [
      seedPrice('year', 1, 25800, 50),
      seedPrice('month', 3, 6900, 55),
      seedPrice('month', 1, 2500, 60),
      seedPrice('month', 1, 2000, 300, { status: 'archived' }),
    ],
    created_at: iso(400 * DAY),
  },
  // 1: 专业版 pro
  {
    id: PLAN_IDS[1]!,
    row_version: 5,
    code: 'pro',
    name: '专业版',
    description: '高流量与低延迟线路，包含亚太精选与全部线路。',
    kind: 'subscription',
    status: 'active',
    visibility: 'public',
    visible_group_ids: [],
    allow_new_purchase: true,
    allow_renewal: true,
    allow_upgrade: true,
    recommended: false,
    reset_price: 1800,
    reset_limit: null,
    current_version_id: '9c0e1a2b-4444-4b00-8000-000000000202',
    pool_ids: [poolId('asia'), poolId('global')],
    versions: [
      {
        id: '9c0e1a2b-4444-4b00-8000-000000000203',
        version: 3,
        status: 'draft',
        published_at: null,
        traffic_bytes: 600 * GiB,
        max_devices: 5,
        throttle_kbps: 300_000,
        created_by_email: 'zhou.min@example.test',
        created_at: iso(1 * DAY),
      },
      {
        id: '9c0e1a2b-4444-4b00-8000-000000000202',
        version: 2,
        status: 'published',
        published_at: iso(100 * DAY),
        traffic_bytes: 500 * GiB,
        max_devices: 5,
        throttle_kbps: 300_000,
        created_by_email: 'ops@example.test',
        created_at: iso(102 * DAY),
      },
      {
        id: '9c0e1a2b-4444-4b00-8000-000000000201',
        version: 1,
        status: 'published',
        published_at: iso(260 * DAY),
        traffic_bytes: 400 * GiB,
        max_devices: 5,
        throttle_kbps: null,
        created_by_email: 'ops@example.test',
        created_at: iso(262 * DAY),
      },
    ],
    prices: [
      seedPrice('year', 1, 45000, 110),
      seedPrice('month', 1, 4500, 120),
    ],
    created_at: iso(400 * DAY),
  },
  // 2: 家庭版 family
  {
    id: PLAN_IDS[2]!,
    row_version: 5,
    code: 'family',
    name: '家庭版',
    description: '多设备共享，适合家庭与小团队。',
    kind: 'subscription',
    status: 'active',
    visibility: 'public',
    visible_group_ids: [],
    allow_new_purchase: false,
    allow_renewal: true,
    allow_upgrade: true,
    recommended: false,
    reset_price: null,
    reset_limit: null,
    current_version_id: '9c0e1a2b-4444-4b00-8000-000000000301',
    pool_ids: [poolId('global')],
    versions: [
      {
        id: '9c0e1a2b-4444-4b00-8000-000000000301',
        version: 1,
        status: 'published',
        published_at: iso(140 * DAY),
        traffic_bytes: 1000 * GiB,
        max_devices: 8,
        throttle_kbps: null,
        created_by_email: 'ops@example.test',
        created_at: iso(142 * DAY),
      },
    ],
    prices: [seedPrice('month', 1, 6800, 140)],
    created_at: iso(400 * DAY),
  },
  // 3: 体验版 trial
  {
    id: PLAN_IDS[3]!,
    row_version: 5,
    code: 'trial',
    name: '体验版',
    description: '新用户体验。',
    kind: 'one_time',
    status: 'archived',
    visibility: 'public',
    visible_group_ids: [],
    allow_new_purchase: false,
    allow_renewal: false,
    allow_upgrade: true,
    recommended: false,
    reset_price: null,
    reset_limit: null,
    current_version_id: '9c0e1a2b-4444-4b00-8000-000000000401',
    pool_ids: [poolId('global')],
    versions: [
      {
        id: '9c0e1a2b-4444-4b00-8000-000000000401',
        version: 1,
        status: 'published',
        published_at: iso(320 * DAY),
        traffic_bytes: 20 * GiB,
        max_devices: 1,
        throttle_kbps: null,
        created_by_email: 'ops@example.test',
        created_at: iso(322 * DAY),
      },
    ],
    prices: [
      seedPrice('one_time', 1, 0, 320, { validity_days: 7 }),
    ],
    created_at: iso(400 * DAY),
  },
  // 4: 企业专线 enterprise-line
  {
    id: '9c0e1a2b-3333-4b00-8000-000000000005',
    row_version: 5,
    code: 'enterprise-line',
    name: '企业专线',
    description: '对公结算，专属线路与 SLA。',
    kind: 'subscription',
    status: 'draft',
    visibility: 'group',
    visible_group_ids: [GROUPS[1]!.id],
    allow_new_purchase: true,
    allow_renewal: true,
    allow_upgrade: true,
    recommended: false,
    reset_price: null,
    reset_limit: null,
    current_version_id: null,
    pool_ids: [poolId('enterprise')],
    versions: [
      {
        id: '9c0e1a2b-4444-4b00-8000-000000000501',
        version: 1,
        status: 'draft',
        published_at: null,
        traffic_bytes: 2000 * GiB,
        max_devices: 20,
        throttle_kbps: null,
        created_by_email: 'ops@example.test',
        created_at: iso(3 * DAY),
      },
    ],
    prices: [seedPrice('month', 1, 199900, 3)],
    created_at: iso(400 * DAY),
  },
  // 5: 年度买断 annual-once
  {
    id: '9c0e1a2b-3333-4b00-8000-000000000006',
    row_version: 5,
    code: 'annual-once',
    name: '年度买断',
    description: '一年有效，大流量总额度。',
    kind: 'one_time',
    status: 'active',
    visibility: 'public',
    visible_group_ids: [],
    allow_new_purchase: true,
    allow_renewal: false,
    allow_upgrade: true,
    recommended: false,
    reset_price: null,
    reset_limit: null,
    current_version_id: '9c0e1a2b-4444-4b00-8000-000000000601',
    pool_ids: [poolId('asia'), poolId('global')],
    versions: [
      {
        id: '9c0e1a2b-4444-4b00-8000-000000000601',
        version: 1,
        status: 'published',
        published_at: iso(20 * DAY),
        traffic_bytes: 1200 * GiB,
        max_devices: 3,
        throttle_kbps: null,
        created_by_email: 'ops@example.test',
        created_at: iso(21 * DAY),
      },
    ],
    prices: [
      seedPrice('one_time', 1, 19900, 20, { validity_days: 365 }),
    ],
    created_at: iso(400 * DAY),
  },
]

// ---------------------------------------------------------------------------
// DTO 转换（严格对齐 Go 结构体字段）
// ---------------------------------------------------------------------------
export function priceRow(p: Price) {
  return {
    id: p.id,
    billing_interval: p.billing_interval,
    interval_count: p.interval_count,
    unit_amount: p.unit_amount,
    validity_days: p.validity_days,
    status: p.status,
    created_at: p.created_at,
  }
}

const tierOrderKey = (p: Price): number => {
  if (p.billing_interval === 'month' && p.interval_count === 1) return 1
  if (p.billing_interval === 'month' && p.interval_count === 3) return 2
  if (p.billing_interval === 'month' && p.interval_count === 6) return 3
  if (p.billing_interval === 'year' && p.interval_count === 1) return 4
  if (p.billing_interval === 'one_time' && p.interval_count === 1) return 5
  return 99
}

export function versionRow(v: Version) {
  return {
    id: v.id,
    version: v.version,
    status: v.status,
    published_at: v.published_at,
    traffic_bytes: v.traffic_bytes,
    max_devices: v.max_devices,
    throttle_kbps: v.throttle_kbps,
    created_by_email: v.created_by_email,
    created_at: v.created_at,
  }
}

export function listRow(p: Plan) {
  const cur = currentOf(p)
  const draft = draftOf(p)
  const usageVersion = cur ?? draft

  // prices 只给在售的、按 (month,1) (month,3) (month,6) (year,1) (one_time,1) 排
  const activePrices = p.prices
    .filter((x) => x.status === 'active')
    .sort((a, b) => tierOrderKey(a) - tierOrderKey(b))
    .map(priceRow)

  const activeSubs = activeSubscriptions(p.id)
  const nodeCount = p.pool_ids.reduce((n, pid) => n + activeNodesInPool(pid), 0)

  return {
    id: p.id,
    row_version: p.row_version,
    code: p.code,
    name: p.name,
    description: p.description,
    kind: p.kind,
    status: p.status,
    visibility: p.visibility,
    recommended: p.recommended,
    prices: activePrices,
    traffic_bytes: usageVersion?.traffic_bytes ?? null,
    max_devices: usageVersion?.max_devices ?? null,
    throttle_kbps: usageVersion?.throttle_kbps ?? null,
    version: cur?.version ?? null,
    draft_version: draft?.version ?? null,
    draft_version_id: draft?.id ?? null,
    pool_ids: p.pool_ids,
    reset: p.reset_price !== null ? { price: p.reset_price, limit: p.reset_limit } : null,
    active_subscriptions: activeSubs,
    node_count: nodeCount,
  }
}

export function detail(p: Plan) {
  const row = listRow(p)
  // 详情里 prices 换成全部价格、created_at 降序
  const allPrices = [...p.prices]
    .sort((a, b) => b.created_at.localeCompare(a.created_at))
    .map(priceRow)

  const allVersions = [...p.versions]
    .sort((a, b) => b.version - a.version)
    .map(versionRow)

  return {
    ...row,
    prices: allPrices,
    visible_group_ids: p.visible_group_ids,
    allow_new_purchase: p.allow_new_purchase,
    allow_renewal: p.allow_renewal,
    allow_upgrade: p.allow_upgrade,
    versions: allVersions,
  }
}

// ---------------------------------------------------------------------------
// 校验规则与文案（同 Go rules.go）
// ---------------------------------------------------------------------------
const maxInt32 = 2147483647
const maxTrafficBytes = 1024 ** 5 // 1 PiB
const maxValidityDays = 36500
const maxResetLimit = 1000
const maxPlanPools = 500
const maxNameRunes = 120
const maxDescRunes = 2000
const maxAmountCents = 1_000_000 * 100 // 1,000,000 元 = 100,000,000 分

const isSubTier = (interval: string, count: number) =>
  (interval === 'month' && [1, 3, 6].includes(count)) || (interval === 'year' && count === 1)

const tierKey = (interval: string, count: number) => `${interval}/${count}`

function checkAmount(key: string, v: unknown, positive: boolean, fields: Record<string, string>) {
  if (typeof v !== 'number' || !Number.isInteger(v)) {
    fields[key] = '最多两位小数'
    return
  }
  if (v < 0) {
    fields[key] = '不能为负数'
    return
  }
  if (positive && v === 0) {
    fields[key] = '必须大于 0'
    return
  }
  if (v > maxAmountCents) {
    fields[key] = '不能超过 1000000 元'
  }
}

export function prepareBasic(nameIn: unknown, descIn: unknown, fields: Record<string, string>): { name: string; description: string | null } {
  const name = str(nameIn).trim()
  const nameRunes = [...name].length
  if (nameRunes === 0 || nameRunes > maxNameRunes || name.includes('\0')) {
    fields.name = `必填，最多 ${maxNameRunes} 个字符，且不能包含非法字符`
  }
  let description: string | null = null
  if (descIn !== undefined && descIn !== null) {
    const d = str(descIn).trim()
    if (d === '') {
      description = null
    } else if (d.includes('\0') || [...d].length > maxDescRunes) {
      fields.description = `最多 ${maxDescRunes} 个字符，且不能包含非法字符`
      description = d
    } else {
      description = d
    }
  }
  return { name, description }
}

export function validateUsage(inUsage: { traffic_bytes?: unknown; max_devices?: unknown; throttle_kbps?: unknown }, fields: Record<string, string>) {
  if (inUsage.traffic_bytes !== undefined && inUsage.traffic_bytes !== null) {
    const tb = inUsage.traffic_bytes
    if (!isInt(tb) || tb <= 0 || tb > maxTrafficBytes) {
      fields.traffic_bytes = '必须大于 0 且不超过 1 PiB；不限流量请留空'
    }
  }
  if (inUsage.max_devices !== undefined && inUsage.max_devices !== null) {
    const md = inUsage.max_devices
    if (!isInt(md) || md <= 0 || md > maxInt32) {
      fields.max_devices = '必须为正整数；不限设备请留空'
    }
  }
  if (inUsage.throttle_kbps !== undefined && inUsage.throttle_kbps !== null) {
    const tk = inUsage.throttle_kbps
    if (!isInt(tk) || tk <= 0 || tk > maxInt32) {
      fields.throttle_kbps = '必须为正整数；不限速请留空'
    }
  }
}

export function validateReset(price: unknown, limit: unknown, fields: Record<string, string>) {
  checkAmount('reset.price', price, true, fields)
  if (limit !== undefined && limit !== null) {
    if (!isInt(limit) || limit < 1 || limit > maxResetLimit) {
      fields['reset.limit'] = `次数上限为 1–${maxResetLimit}，不限请留空`
    }
  }
}

export function validateSales(
  kind: string,
  inSales: {
    visibility?: unknown
    visible_group_ids?: unknown
    allow_new_purchase?: unknown
    allow_renewal?: unknown
    allow_upgrade?: unknown
    recommended?: unknown
  },
  fields: Record<string, string>,
) {
  const vis = str(inSales.visibility) || 'public'
  if (!['public', 'authenticated', 'group', 'hidden'].includes(vis)) {
    fields.visibility = '不支持的可见范围'
  }
  const groups = Array.isArray(inSales.visible_group_ids) ? (inSales.visible_group_ids as unknown[]) : []
  if (
    inSales.visible_group_ids !== undefined &&
    inSales.visible_group_ids !== null &&
    (!Array.isArray(inSales.visible_group_ids) || groups.some((g) => typeof g !== 'string' || !UUID.test(g)) || new Set(groups).size !== groups.length)
  ) {
    fields.visible_group_ids = '必须是无重复的 UUID 列表'
  } else {
    for (const gid of groups) {
      if (!GROUPS.some((g) => g.id === gid)) {
        fields.visible_group_ids = '用户组不存在'
        break
      }
    }
  }
  if (!fields.visible_group_ids) {
    if (vis === 'group' && groups.length === 0) {
      fields.visible_group_ids = '指定用户组可见时至少选一个用户组'
    }
    if (vis !== 'group' && groups.length > 0) {
      fields.visible_group_ids = '只有指定用户组可见时才能选用户组'
    }
  }
  if (kind === 'one_time' && inSales.allow_renewal === true) {
    fields.allow_renewal = '一次性套餐不能续费'
  }
}

export function validateTiers(tiers: unknown, fields: Record<string, string>) {
  if (!Array.isArray(tiers) || tiers.length === 0) {
    fields.prices = '订阅套餐至少设一档价格'
    return
  }
  const seen = new Set<string>()
  tiers.forEach((t: Json, i: number) => {
    const key = `prices.${i}`
    const bi = str(t?.billing_interval)
    const ic = Number(t?.interval_count)
    if (!isSubTier(bi, ic)) {
      fields[key] = '周期只能是月付、季付、半年付或年付'
      return
    }
    const tk = tierKey(bi, ic)
    if (seen.has(tk)) {
      fields[key] = '同一周期只能有一档价格'
      return
    }
    seen.add(tk)
    checkAmount(`${key}.unit_amount`, t?.unit_amount, false, fields)
  })
}

export function validateTierChanges(changes: unknown, fields: Record<string, string>) {
  if (!Array.isArray(changes) || changes.length === 0) {
    fields.prices = '至少给出一档价格'
    return
  }
  const seen = new Set<string>()
  changes.forEach((t: Json, i: number) => {
    const key = `prices.${i}`
    const bi = str(t?.billing_interval)
    const ic = Number(t?.interval_count)
    if (!isSubTier(bi, ic)) {
      fields[key] = '周期只能是月付、季付、半年付或年付'
      return
    }
    const tk = tierKey(bi, ic)
    if (seen.has(tk)) {
      fields[key] = '同一周期只能出现一次'
      return
    }
    seen.add(tk)
    if (t?.unit_amount !== undefined && t?.unit_amount !== null) {
      checkAmount(`${key}.unit_amount`, t.unit_amount, false, fields)
    }
  })
}

export function validateOneTime(inOne: { unit_amount?: unknown; validity_days?: unknown }, fields: Record<string, string>) {
  checkAmount('unit_amount', inOne.unit_amount, false, fields)
  if (inOne.validity_days !== undefined && inOne.validity_days !== null) {
    const vd = inOne.validity_days
    if (!isInt(vd) || vd < 1 || vd > maxValidityDays) {
      fields.validity_days = `有效天数为 1–${maxValidityDays}，永久请留空`
    }
  }
}

export function poolProblems(ids: unknown): string | null {
  if (ids === undefined || ids === null) return null
  if (!Array.isArray(ids) || ids.some((x) => typeof x !== 'string' || !UUID.test(x)) || new Set(ids).size !== ids.length) {
    return '必须是无重复的 UUID 列表'
  }
  if (ids.length > maxPlanPools) {
    return `一个套餐最多绑定 ${maxPlanPools} 个节点池`
  }
  if (ids.some((x) => !livePool(x as string))) {
    return '包含不存在或已停用的节点池'
  }
  return null
}

export function publishProblems(p: { prices: Price[]; pool_ids: string[] }): Record<string, string> | null {
  const activePrices = p.prices.filter((x) => x.status === 'active')
  if (activePrices.length === 0) {
    return { prices: '发布前至少设一档在售价格' }
  }
  if (p.pool_ids.length === 0) {
    return { pool_ids: '发布前至少绑定一个节点池' }
  }
  const assignable = p.pool_ids.filter(livePool)
  if (assignable.length === 0) {
    return { pool_ids: '绑定的节点池都已停用：先在「线路 · 节点池」里启用，或换绑一个启用中的池' }
  }
  const totalActiveNodes = p.pool_ids.reduce((sum, pid) => sum + activeNodesInPool(pid), 0)
  if (totalActiveNodes === 0) {
    return { pool_ids: '绑定池中至少需要一个可服务节点' }
  }
  return null
}

// ---------------------------------------------------------------------------
// autoCode：逐行移植自 code.go (autoCodeBase + pickCode)
// ---------------------------------------------------------------------------
const codeWords: Record<string, string> = {
  标准: 'standard',
  专业: 'pro',
  高级: 'premium',
  家庭: 'family',
  体验: 'trial',
  试用: 'trial',
  企业: 'enterprise',
  定制: 'custom',
  基础: 'basic',
  旗舰: 'ultimate',
  年: 'yearly',
  季: 'quarterly',
  月: 'monthly',
  旧: 'legacy',
  新: 'new',
  学生: 'student',
  团队: 'team',
  个人: 'personal',
}

const codeWordOrder: string[] = Object.keys(codeWords).sort((a, b) => {
  const la = [...a].length
  const lb = [...b].length
  if (la !== lb) return lb - la
  return a.localeCompare(b)
})

const maxCodeBase = 32

export function autoCodeBase(name: string): string {
  let s = name.trim()
  for (const k of codeWordOrder) {
    s = s.replaceAll(k, ` ${codeWords[k]} `)
  }
  const chars: string[] = []
  let dash = false
  for (const r of s.toLowerCase()) {
    const code = r.charCodeAt(0)
    if (r.length === 1 && ((code >= 97 && code <= 122) || (code >= 48 && code <= 57))) {
      if (dash && chars.length > 0) {
        chars.push('-')
      }
      dash = false
      chars.push(r)
      continue
    }
    dash = true
  }
  let base = chars.join('')
  if (base.length > maxCodeBase) {
    base = base.slice(0, maxCodeBase).replace(/-+$/, '')
  }
  if (base.length === 0) return 'plan'
  if (base.length === 1) return `plan-${base}`
  return base
}

export function pickCode(base: string, taken: string[]): string {
  const used = new Set(taken)
  if (!used.has(base)) return base
  for (let i = 2; ; i++) {
    const c = `${base}-${i}`
    if (!used.has(c)) return c
  }
}

export function autoCode(name: string, taken: string[]): string {
  return pickCode(autoCodeBase(name), taken)
}
