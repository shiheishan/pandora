import { z } from 'zod'

// ---------------------------------------------------------------------------
// 后台套餐接口的响应形状，照 panel/internal/module/catalog/handler/dto_admin.go 写。
// 封闭枚举（迁移里的 CHECK 与 Go 校验）：未知值判为不符约定。
// catalog 的切片都由 make / 空切片构造，不会编成 null，所以这些数组字段不放宽成 nullable。
// ---------------------------------------------------------------------------
export const PLAN_KINDS = ['subscription', 'one_time'] as const
const PLAN_STATUSES = ['draft', 'active', 'archived'] as const
export const VISIBILITIES = ['public', 'authenticated', 'group', 'hidden'] as const
const VERSION_STATUSES = ['draft', 'published'] as const
const BILLING_INTERVALS = ['month', 'year', 'one_time'] as const
export type PlanKind = (typeof PLAN_KINDS)[number]
export type PlanStatus = (typeof PLAN_STATUSES)[number]
export type Visibility = (typeof VISIBILITIES)[number]

const int = z.number().int()
const count = int.nonnegative()
const time = z.string()

// ---------------------------------------------------------------------------
// 价格：订阅套餐只有 (month,1) (month,3) (month,6) (year,1) 四档，一次性套餐只有 (one_time,1)。
// unit_amount 是整数分；validity_days 只有一次性价有，null = 永久。
// ---------------------------------------------------------------------------
const priceRowSchema = z.object({
  id: z.string(),
  billing_interval: z.enum(BILLING_INTERVALS),
  interval_count: int.positive(),
  unit_amount: count,
  validity_days: int.positive().nullable(),
  status: z.enum(['active', 'archived']),
  created_at: time,
})
export type PriceRow = z.output<typeof priceRowSchema>

/** 流量重置：price 是每次的整数分，limit null = 不限次数；整个字段 null = 不卖重置 */
const resetSchema = z.object({ price: count, limit: int.positive().nullable() })

// ---------------------------------------------------------------------------
// GET v1/plans：列表行。prices 只有在售的（按周期从短到长）；用量三项取当前版本，
// 还没发布过取草稿版本，null = 不限；traffic_bytes 订阅是每周期、一次性是总量。
// version / draft_version 是版本号，draft_version_id 是草稿版本的 id（发布接口要它）。
// ---------------------------------------------------------------------------
export const planRowSchema = z.object({
  id: z.string(),
  row_version: int.positive(),
  code: z.string(),
  name: z.string(),
  description: z.string().nullable(),
  kind: z.enum(PLAN_KINDS),
  status: z.enum(PLAN_STATUSES),
  visibility: z.enum(VISIBILITIES),
  recommended: z.boolean(),
  prices: z.array(priceRowSchema),
  traffic_bytes: int.positive().nullable(),
  max_devices: int.positive().nullable(),
  throttle_kbps: int.positive().nullable(),
  version: int.positive().nullable(),
  draft_version: int.positive().nullable(),
  draft_version_id: z.string().nullable(),
  pool_ids: z.array(z.string()),
  reset: resetSchema.nullable(),
  active_subscriptions: count,
  node_count: count,
})
export const plansSchema = z.object({ plans: z.array(planRowSchema) })
export type PlanRow = z.output<typeof planRowSchema>

// ---------------------------------------------------------------------------
// GET v1/plans/{id} 与全部写接口的响应 {plan}：列表行 + 销售细节 + 版本（version 降序）；
// 这里的 prices 是全部价格（含已归档，created_at 降序）
// ---------------------------------------------------------------------------
export const versionRowSchema = z.object({
  id: z.string(),
  version: int.positive(),
  status: z.enum(VERSION_STATUSES),
  published_at: time.nullable(),
  traffic_bytes: int.positive().nullable(),
  max_devices: int.positive().nullable(),
  throttle_kbps: int.positive().nullable(),
  created_by_email: z.string().nullable(),
  created_at: time,
})
export type VersionRow = z.output<typeof versionRowSchema>

export const planDetailSchema = planRowSchema.extend({
  visible_group_ids: z.array(z.string()),
  allow_new_purchase: z.boolean(),
  allow_renewal: z.boolean(),
  allow_upgrade: z.boolean(),
  versions: z.array(versionRowSchema),
})
export const planResponseSchema = z.object({ plan: planDetailSchema })
export type PlanDetail = z.output<typeof planDetailSchema>

// ---------------------------------------------------------------------------
// GET v1/plans/{id}/pools（node 模块注册）：绑池选择器，列出未停用的池与是否已绑。
// active_nodes 是生命周期 active 且定了协议的节点数（与节点池列表同口径）；deliverable_nodes 是其中
// 真能写进订阅的（服务器就绪、在役、见过心跳、协议稳定、有地址，后端 subscription.DeliverableNodeSQL）
// ---------------------------------------------------------------------------
export const planPoolsSchema = z.object({
  pools: z.array(z.object({ id: z.string(), name: z.string(), active_nodes: count, deliverable_nodes: count, bound: z.boolean() })),
})

// GET v1/node-pools（节点模块）：新建套餐时还没有套餐 id，只能从这里取候选；
// 只收这里要的字段，其余字段归节点页的 schema 管，所以查询键与节点页分开
export const poolOptionsSchema = z.object({
  pools: z
    .array(z.object({ id: z.string(), name: z.string(), status: z.string(), active_nodes: count }))
    .nullable()
    .transform((v) => v ?? []),
})
