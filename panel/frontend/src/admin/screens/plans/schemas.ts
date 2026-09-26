/**
 * [INPUT]: 依赖 zod
 * [OUTPUT]: 对外提供套餐模块的封闭枚举与 zod schema（PlanRow、PriceRow、PlanDetail、VersionRow、Quota、Entitlement、PlanPools、PoolOption、TrafficPack 及各写响应）与类型
 * [POS]: admin/screens/plans 的形状层（纯 zod，不碰 React，tests/mock-admin-plans.test.ts 也用它核对假后端）：照 api-contract.md 后台-04（含修订 R1 / R65 / R66 / R73 / R99 / R100），并按 domain/adminops/service.go 的 PlanRow / PriceRow、catalog.go 的 CatalogPlanDetail / VersionRow、plan_wizard*.go、traffic_packs.go 与 api/admin/pools.go 的 json tag 核对；Go 可能给 nil 切片的数组写 nullable 并归一成 []
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { z } from 'zod'

// ---------------------------------------------------------------------------
// 封闭枚举（迁移里的 CHECK 与 Go 校验）：未知值判为不符约定
// ---------------------------------------------------------------------------
export const PLAN_STATUSES = ['draft', 'active', 'archived'] as const
export const VISIBILITIES = ['public', 'authenticated', 'group', 'invite_only', 'hidden'] as const
export const VERSION_STATUSES = ['draft', 'published', 'retired'] as const
export const RESET_STRATEGIES = ['never', 'natural_month', 'billing_cycle', 'fixed_day'] as const
export const OVERAGE_POLICIES = ['suspend', 'throttle', 'metered_billing'] as const
export const QUOTA_PERIODS = ['total', 'cycle', 'day', 'month'] as const
export const BILLING_INTERVALS = ['day', 'week', 'month', 'quarter', 'year', 'one_time'] as const
export const CURRENCIES = ['CNY', 'USD'] as const
export const PACK_STATUSES = ['active', 'archived'] as const
export type PlanStatus = (typeof PLAN_STATUSES)[number]
export type Visibility = (typeof VISIBILITIES)[number]
export type VersionStatus = (typeof VERSION_STATUSES)[number]
export type ResetStrategy = (typeof RESET_STRATEGIES)[number]
export type OveragePolicy = (typeof OVERAGE_POLICIES)[number]
export type BillingInterval = (typeof BILLING_INTERVALS)[number]
export type Currency = (typeof CURRENCIES)[number]
export type PackStatus = (typeof PACK_STATUSES)[number]

const int = z.number().int()
const count = int.nonnegative()
const time = z.string()
/** Go 的 nil 切片编成 null：收下后归一成 [] */
const list = <T extends z.ZodType>(item: T) =>
  z
    .array(item)
    .nullable()
    .transform((v) => v ?? [])

// ---------------------------------------------------------------------------
// 价格行（列表与详情同形，含已归档）
// ---------------------------------------------------------------------------
export const priceRowSchema = z.object({
  id: z.string(),
  currency: z.enum(CURRENCIES),
  unit_amount: count,
  billing_interval: z.enum(BILLING_INTERVALS),
  interval_count: int.positive(),
  trial_days: count,
  status: z.enum(['active', 'archived']),
  user_group_id: z.string().nullable(),
  valid_from: time.nullable(),
  valid_until: time.nullable(),
  row_version: int,
})
export type PriceRow = z.output<typeof priceRowSchema>

// ---------------------------------------------------------------------------
// GET v1/plans：左栏卡片（不分页，含已归档）
// ---------------------------------------------------------------------------
export const planRowSchema = z.object({
  id: z.string(),
  product_id: z.string(),
  row_version: int,
  code: z.string(),
  name: z.string(),
  description: z.string().nullable(),
  status: z.enum(PLAN_STATUSES),
  visibility: z.enum(VISIBILITIES),
  sort_order: int,
  current_version_id: z.string().nullable(),
  draft_version_id: z.string().nullable(),
  version: int.nullable(),
  max_devices: int.nullable(),
  traffic_limit: int.nullable(),
  prices: list(priceRowSchema),
  active_subscriptions: count,
  node_count: count,
  // 修订 R100：卖点与「推荐」
  highlights: list(z.string()),
  recommended: z.boolean(),
})
export const plansSchema = z.object({ plans: list(planRowSchema) })
export type PlanRow = z.output<typeof planRowSchema>

// ---------------------------------------------------------------------------
// GET v1/plans/{id}：详情（版本 version 降序，价格 created_at 降序）
// ---------------------------------------------------------------------------
export const quotaSchema = z.object({ metric: z.string(), limit: int.nullable(), unit: z.string(), period: z.enum(QUOTA_PERIODS) })
export type Quota = z.output<typeof quotaSchema>
export const entitlementSchema = z.object({ code: z.string(), value: z.unknown() })
export type Entitlement = z.output<typeof entitlementSchema>

export const versionRowSchema = z.object({
  id: z.string(),
  version: int,
  status: z.enum(VERSION_STATUSES),
  frozen_at: time.nullable(),
  row_version: int,
  quota_reset_strategy: z.enum(RESET_STRATEGIES),
  quota_reset_day: int.nullable(),
  grace_period_hours: count,
  grace_keeps_service: z.boolean(),
  renewal_extends_period: z.boolean(),
  renewal_resets_quota: z.boolean(),
  renewal_keeps_addons: z.boolean(),
  max_devices: int.nullable(),
  max_concurrent: int.nullable(),
  device_release_hours: count,
  // R99：新写入只收 suspend，存量行可能还是另外两种，读取照收
  overage_policy: z.enum(OVERAGE_POLICIES),
  // R99：全程生效的按用户限速，null = 不限
  throttle_kbps: int.positive().nullable(),
  notes: z.string().nullable(),
  entitlements: list(entitlementSchema),
  quotas: list(quotaSchema),
  pool_ids: list(z.string()),
  // 修订 R66：创建人（JOIN users，账号已删时为 null）
  created_by_email: z.string().nullable(),
  created_at: time,
})
export type VersionRow = z.output<typeof versionRowSchema>

export const planDetailSchema = z.object({
  id: z.string(),
  product_id: z.string(),
  current_version_id: z.string().nullable(),
  row_version: int,
  code: z.string(),
  name: z.string(),
  description: z.string().nullable(),
  status: z.enum(PLAN_STATUSES),
  visibility: z.enum(VISIBILITIES),
  visible_group_ids: list(z.string()),
  visible_from: time.nullable(),
  visible_until: time.nullable(),
  allow_new_purchase: z.boolean(),
  allow_renewal: z.boolean(),
  allow_upgrade: z.boolean(),
  purchase_limit_per_user: int.nullable(),
  stock_total: int.nullable(),
  stock_reserved: count,
  sort_order: int,
  // 修订 R100
  highlights: list(z.string()),
  recommended: z.boolean(),
  versions: list(versionRowSchema),
  prices: list(priceRowSchema),
})
export const planResponseSchema = z.object({ plan: planDetailSchema })
export type PlanDetail = z.output<typeof planDetailSchema>

// ---------------------------------------------------------------------------
// GET v1/plans/{id}/pools：有草稿时是草稿的绑定（可改），否则是当前发布版本的（只读）
// ---------------------------------------------------------------------------
export const planPoolsSchema = z.object({
  version_id: z.string(),
  version_status: z.string(),
  row_version: int,
  editable: z.boolean(),
  pools: list(z.object({ id: z.string(), name: z.string(), active_nodes: count, bound: z.boolean() })),
})
export type PlanPools = z.output<typeof planPoolsSchema>

// GET v1/node-pools（node.read，节点分段）：新建向导没有套餐 id，只能从这里取候选；
// 只收向导要的字段，其余字段归节点页的 schema 管，所以查询键与节点页分开
export const poolOptionsSchema = z.object({ pools: list(z.object({ id: z.string(), name: z.string(), status: z.string(), active_nodes: count })) })
export type PoolOption = z.output<typeof poolOptionsSchema>['pools'][number]

// ---------------------------------------------------------------------------
// 流量包（修订 R73）：乐观锁是 updated_at，原样回传
// ---------------------------------------------------------------------------
export const packSchema = z.object({
  id: z.string(),
  name: z.string(),
  traffic_bytes: int.positive(),
  currency: z.enum(CURRENCIES),
  unit_amount: int.positive(),
  recommended: z.boolean(),
  status: z.enum(PACK_STATUSES),
  sort_order: int,
  sold_count: count,
  created_at: time,
  updated_at: time,
})
export const packsSchema = z.object({ packs: list(packSchema) })
export const packResponseSchema = z.object({ pack: packSchema })
export type TrafficPack = z.output<typeof packSchema>

// ---------------------------------------------------------------------------
// 写响应
// ---------------------------------------------------------------------------
/** POST v1/plans/complete（R65：plan 是建成后的完整详情） */
export const planCreatedSchema = z.object({ plan: planDetailSchema, version_id: z.string(), price_ids: list(z.string()), published: z.boolean() })
/** PUT v1/plans/{id}/complete：changed 是可直接展示的中文生效说明 */
export const planUpdatedSchema = z.object({ plan: planDetailSchema, changed: list(z.string()) })
/** PUT v1/plans/{id}、PUT versions、归档价格、归档套餐 */
export const rowVersionSchema = z.object({ ok: z.literal(true), row_version: int })
/** POST v1/plans/{id}/versions：只有 id / version / status / frozen_at / row_version / created_at 有意义，其余为默认值 */
export const versionCreatedSchema = z.object({ version: versionRowSchema })
export const publishedSchema = z.object({ ok: z.literal(true), plan_row_version: int, version_row_version: int })
export const priceCreatedSchema = z.object({ price: priceRowSchema })
export const poolsBoundSchema = z.object({ bound: count, row_version: int, version_id: z.string() })
