import { z } from 'zod'

const iso = z.string()
const uuid = z.string()
const list = <T extends z.ZodType>(item: T) =>
  z
    .array(item)
    .nullable()
    .transform((v) => v ?? [])
const strs = list(z.string())

// ---------------------------------------------------------------------------
// 节点：列表行（GET v1/nodes）、单取的详情行（GET v1/nodes?id=）与写接口回的 AdminNode
// ---------------------------------------------------------------------------
const SERVING_STATUSES = ['draft', 'active', 'disabled', 'retired'] as const
export type ServingStatus = (typeof SERVING_STATUSES)[number]

// 节点补齐 Xboard（迁移 00045，node/handler 的 dto_xb_nodes.go）：时段倍率一段、本周期流量
const rateRangeSchema = z.object({ start: z.string(), end: z.string(), rate: z.number() })
export type RateRange = z.output<typeof rateRangeSchema>
/** limit_bytes / reset_day 为 null 是不限、不自动重置；exceeded_at 非空 = 本周期已超限、已停止下发 */
const nodeTrafficSchema = z.object({ limit_bytes: z.number().nullable(), reset_day: z.number().nullable(), used_bytes: z.number(), reset_at: iso, exceeded_at: iso.nullable() })
export type NodeTraffic = z.output<typeof nodeTrafficSchema>
const xbNodeFields = { tags: strs, traffic: nodeTrafficSchema, traffic_rate_schedule: list(rateRangeSchema) }

export const nodeRowSchema = z.object({
  id: uuid,
  node_no: z.number(),
  row_version: z.number(),
  name: z.string(),
  serving_status: z.enum(SERVING_STATUSES),
  server_id: uuid.nullable(),
  server_name: z.string().nullable(),
  pool_id: uuid.nullable(),
  pool_name: z.string().nullable(),
  agent_version: z.string().nullable(),
  hostname: z.string().nullable(),
  public_ipv4: z.string().nullable(),
  cpu_cores: z.number().nullable(),
  memory_mb: z.number().nullable(),
  disk_gb: z.number().nullable(),
  last_heartbeat_at: iso.nullable(),
  stale: z.boolean(),
  delivered_to_users: z.boolean(),
  delivery_note: z.string(),
  identity_serial: z.number().nullable(),
  /** 能不能改绑 / 解绑（草稿或已停用）：后端 service.nodeMovableState 算好，前端不自己判断 */
  movable: z.boolean(),
  created_at: iso,
  node_type: z.string().nullable(),
  server_host: z.string().nullable(),
  server_port: z.number().nullable(),
  display_name: z.string().nullable(),
  country_code: z.string().nullable(),
  sort_order: z.number(),
  online_users: z.number(),
  online_ips: z.number(),
  traffic_bytes_24h: z.number().int(),
  cpu_percent: z.number().nullable(),
  mem_percent: z.number().nullable(),
  metrics_at: iso.nullable(),
  granted_plans: strs,
  // 下发与运行的真实状态（读模型在 node/service 的 node_list_delivery.go）。后端总是给；开发期假后端
  // 还没跟上时按「没有这项信息」处理，不让整张列表解析失败
  effective_state: z.enum(['', 'applied', 'pending', 'failed']).catch(''),
  last_apply_failure: z
    .object({ phase: z.string(), detail: z.string(), at: iso })
    .nullish()
    .transform((v) => v ?? null),
  runtime_status: z.string().nullish().transform((v) => v ?? null),
  runtime_reason: z.string().nullish().transform((v) => v ?? null),
  runtime_state_at: iso.nullish().transform((v) => v ?? null),
  runtime_reason_node: z.string().nullish().transform((v) => v ?? null),
  delivery_degraded: z.boolean().catch(false),
  ...xbNodeFields,
  relay_count: z.number(),
  ech_enabled: z.boolean(),
})
export const nodesResponse = z.object({ nodes: list(nodeRowSchema), total: z.number() })
export type NodeRow = z.output<typeof nodeRowSchema>

/**
 * 单取（?id=）才带的编辑字段（node/handler 的 nodeDetailResponse）：列表不带，1000 个节点每次刷新省掉
 * 全部 protocol_config。protocol_config 已由后端抹掉敏感键
 */
export const nodeDetailSchema = nodeRowSchema.extend({
  traffic_rate: z.number(),
  protocol_config: z.record(z.string(), z.unknown()).nullable().transform((v) => v ?? {}),
  protocol_schema_version: z.number(),
  config_validated_at: iso.nullable(),
  // 存量协议配置不满足现行规则的提示（node/service 的 AdminNode.Warnings）；Go 是 omitempty
  warnings: z.array(z.string()).min(1).optional(),
})
export const nodeDetailResponse = z.object({ nodes: list(nodeDetailSchema), total: z.number() })
export type NodeDetail = z.output<typeof nodeDetailSchema>

export const adminNodeSchema = z.object({
  id: uuid,
  row_version: z.number(),
  name: z.string(),
  server_id: uuid.nullable(),
  pool_id: uuid.nullable(),
  serving_status: z.enum(SERVING_STATUSES),
  node_type: z.string().nullable(),
  server_host: z.string().nullable(),
  server_port: z.number().nullable(),
  traffic_rate: z.number(),
  display_name: z.string().nullable(),
  country_code: z.string().nullable(),
  protocol_config: z.unknown(),
  protocol_schema_version: z.number(),
  config_validated_at: iso.nullable(),
  sort_order: z.number(),
  created_at: iso,
  updated_at: iso,
  ...xbNodeFields,
  // 上线（R108 activate）与建、改、复制、迁移（保留端口提示）会带：Go 是 omitempty，没有提示时
  // 不出现这个键，出现就至少一条（R113）
  warnings: z.array(z.string()).min(1).optional(),
})
export type AdminNode = z.output<typeof adminNodeSchema>

// ---------------------------------------------------------------------------
// 协议 schema（GET v1/node-protocol-schemas）。13 个 stable，表单只渲染这些。
// 未配置的草稿 protocol_schema_version 仍是 0，那不是一种协议类型。
// ---------------------------------------------------------------------------
const protocolSchemaSchema = z.object({
  node_type: z.string(),
  version: z.number(),
  status: z.string(),
  required: strs,
  allowed_properties: strs,
  methods: z.array(z.string()).optional(),
  enums: z.record(z.string(), z.array(z.string())).optional(),
  // list：逗号分隔录入，一个值存字符串、多个存数组（REALITY 的 server_name / short_id）
  property_types: z.record(z.string(), z.enum(['number', 'boolean', 'json', 'list'])).optional(),
  sensitive_properties: z.array(z.string()).optional(),
  /** 字段下的一句说明（按字段路径），跟着后端 schema 走 */
  hints: z.record(z.string(), z.string()).optional(),
  /** 10-16 表单元数据：一句话备注（可缺席） */
  note: z.string().optional(),
  /**
   * 字段显示顺序与显示名、条件显示、高级折叠、默认值、占位文字、枚举文案（全部可缺席；
   * 有则按此顺序渲染，没有时退回 allowed_properties 的旧口径）。show_if 的键是另一字段的
   * 路径，值是让本字段显示的表单取值（按表单字符串比较），所有键同时满足才显示
   */
  fields: z
    .array(
      z.object({
        path: z.string(),
        label: z.string(),
        advanced: z.boolean().optional(),
        show_if: z.record(z.string(), z.array(z.string())).optional(),
        default: z.union([z.string(), z.number(), z.boolean()]).optional(),
        placeholder: z.string().optional(),
        option_labels: z.record(z.string(), z.string()).optional(),
      }),
    )
    .optional(),
})
export const protocolSchemasResponse = z.object({ schemas: list(protocolSchemaSchema) })
export type ProtocolSchema = z.output<typeof protocolSchemaSchema>

// ---------------------------------------------------------------------------
// 服务器（GET v1/servers 与 /servers/{id}，node/handler 的 serverResponse）：指针字段全是 null 而不是缺键
// ---------------------------------------------------------------------------
// 库里只有这五态（00018_node.sql）
const SERVER_STATUSES = ['draft', 'ready', 'draining', 'maintenance', 'retired'] as const
export type ServerStatus = (typeof SERVER_STATUSES)[number]

export const serverSchema = z.object({
  id: uuid,
  name: z.string(),
  status: z.enum(SERVER_STATUSES),
  status_reason: z.string().nullable(),
  row_version: z.number(),
  region: z.string().nullable(),
  hostname: z.string().nullable(),
  public_ipv4: z.string().nullable(),
  public_ipv6: z.string().nullable(),
  private_ipv4: z.string().nullable(),
  architecture: z.string().nullable(),
  os_name: z.string().nullable(),
  agent_version: z.string().nullable(),
  last_heartbeat_at: iso.nullable(),
  heartbeat_online: z.boolean(),
  cpu_cores: z.number().nullable(),
  memory_mb: z.number().nullable(),
  disk_gb: z.number().nullable(),
  capacity_nodes: z.number(),
  notes: z.string().nullable(),
  node_count: z.number(),
  active_node_count: z.number(),
  serving_node_count: z.number(),
  never_seen_node_count: z.number(),
  created_at: iso,
  updated_at: iso,
  cpu_bp: z.number().nullable(),
  mem_used_mb: z.number().nullable(),
  mem_total_mb: z.number().nullable(),
  disk_used_gb: z.number().nullable(),
  disk_total_gb: z.number().nullable(),
  metrics_at: iso.nullable(),
})
// 三个列表 Go 侧都以空切片起步，不会是 null，按严格数组写
export const serversResponse = z.object({ servers: z.array(serverSchema), total: z.number() })
export type Server = z.output<typeof serverSchema>

/** GET v1/servers/{id}/nodes：含已退役与已销毁，按 sort_order */
const serverNodeSchema = z.object({
  id: uuid,
  name: z.string(),
  serving_status: z.string(),
  node_type: z.string().nullable(),
  display_name: z.string().nullable(),
  server_host: z.string().nullable(),
  server_port: z.number().nullable(),
  traffic_rate: z.number(),
  config_validated_at: iso.nullable(),
  last_heartbeat_at: iso.nullable(),
  created_at: iso,
})
export const serverNodesResponse = z.object({ nodes: z.array(serverNodeSchema), total: z.number() })

// ---------------------------------------------------------------------------
// 节点池（GET v1/node-pools）：members 不含已销毁节点，plan_names 去重（两者 SQL 里 coalesce 过，恒为数组）；
// allowed_user_groups 是 R104「仅限用户组」名单（空 = 不限定），Go 的 nil 切片可能编成 null，归一成 []
// ---------------------------------------------------------------------------
export const POOL_STATUSES = ['active', 'draining', 'disabled'] as const
export type PoolStatus = (typeof POOL_STATUSES)[number]

const poolSchema = z.object({
  id: uuid,
  code: z.string(),
  name: z.string(),
  region: z.string(),
  status: z.enum(POOL_STATUSES),
  nodes: z.number(),
  active_nodes: z.number(),
  plans: z.number(),
  members: z.array(z.object({ id: uuid, name: z.string(), node_no: z.number() })),
  plan_names: z.array(z.string()),
  allowed_user_groups: z
    .array(z.object({ id: uuid, name: z.string() }))
    .nullable()
    .transform((v) => v ?? []),
})
export const poolsResponse = z.object({ pools: z.array(poolSchema) })
export type Pool = z.output<typeof poolSchema>

// ---------------------------------------------------------------------------
// 身份、探针、路由
// ---------------------------------------------------------------------------
export const identitySchema = z.object({
  identity: z
    .object({
      serial: z.number(),
      status: z.string(),
      spiffe_id: z.string(),
      fingerprint_sha256: z.string(),
      issued_at: iso,
      expires_at: iso,
      revoked_at: iso.optional(),
      revoked_reason: z.string().optional(),
    })
    .nullable(),
  bootstrap_tokens_pending: z.number(),
})

const metricPointSchema = z.object({
  at: iso,
  cpu_percent: z.number(),
  mem_percent: z.number(),
  load1: z.number(),
  rx_speed: z.number(),
  tx_speed: z.number(),
  tcp_conns: z.number(),
})
export const metricsSchema = z.object({
  points: list(metricPointSchema),
  latest: z
    .object({
      cpu_percent: z.number(),
      mem_used_mb: z.number(),
      mem_total_mb: z.number(),
      disk_used_gb: z.number(),
      disk_total_gb: z.number(),
      load1: z.number(),
      load5: z.number(),
      load15: z.number(),
      tcp_conns: z.number(),
      uptime_sec: z.number(),
      rx_total: z.number(),
      tx_total: z.number(),
    })
    .nullable(),
})
export type MetricPoint = z.output<typeof metricPointSchema>

const outboundSchema = z.object({ tag: z.string(), type: z.string(), settings: z.unknown() })
const routeSchema = z.object({
  priority: z.number(),
  matcher: z.record(z.string(), z.unknown()),
  outbound_tag: z.string(),
  enabled: z.boolean(),
  note: z.string(),
})
export type Outbound = z.output<typeof outboundSchema>
export type Route = z.output<typeof routeSchema>
/** 节点所在的路由组（00096），按生效顺序（sort_order 小的先） */
const routeGroupRefSchema = z.object({ id: uuid, name: z.string(), sort_order: z.number() })
export type RouteGroupRef = z.output<typeof routeGroupRefSchema>
export const nodeRoutingSchema = z.object({ row_version: z.number(), groups: list(routeGroupRefSchema), outbounds: list(outboundSchema), routes: list(routeSchema) })
export type NodeRouting = z.output<typeof nodeRoutingSchema>
export const globalRoutingSchema = z.object({ revision: z.string(), outbounds: list(outboundSchema), routes: list(routeSchema), online_nodes: z.number() })
export type GlobalRouting = z.output<typeof globalRoutingSchema>

// ---------------------------------------------------------------------------
// 路由组（00096）：GET v1/route-groups、组内路由、节点生效预览
// ---------------------------------------------------------------------------
export const routeGroupSchema = z.object({
  id: uuid,
  name: z.string(),
  description: z.string(),
  sort_order: z.number(),
  row_version: z.number(),
  outbound_count: z.number(),
  rule_count: z.number(),
  members: z.array(z.object({ id: uuid, name: z.string() })),
  created_at: iso,
  updated_at: iso,
})
export type RouteGroup = z.output<typeof routeGroupSchema>
export const routeGroupsResponse = z.object({ groups: z.array(routeGroupSchema) })
export const groupRoutingSchema = z.object({ row_version: z.number(), outbounds: list(outboundSchema), routes: list(routeSchema) })
export type GroupRouting = z.output<typeof groupRoutingSchema>
/** 生效出站 / 规则的来源层；group_id / group_name 只在 scope = group 时出现（omitempty） */
const routingSourceSchema = z.object({ scope: z.enum(['node', 'group', 'global']), group_id: uuid.optional(), group_name: z.string().optional() })
export type RoutingSource = z.output<typeof routingSourceSchema>
export const effectiveRoutingSchema = z.object({
  groups: z.array(routeGroupRefSchema),
  outbounds: z.array(z.object({ tag: z.string(), type: z.string(), settings: z.unknown(), source: routingSourceSchema })),
  routes: z.array(z.object({ matcher: z.record(z.string(), z.unknown()), outbound: z.string(), source: routingSourceSchema })),
})

// ---------------------------------------------------------------------------
// 写操作的响应
// ---------------------------------------------------------------------------
export const okUpdated = z.object({ ok: z.literal(true), updated: z.number() })
export const batchStatusResponse = z.object({ ok: z.literal(true), updated: z.number(), serving_status: z.string() })
export const bootstrapTokenResponse = z.object({ token: z.string(), expires_at: iso, install_command: z.string() })
export const realityKeypairResponse = z.object({ private_key: z.string(), public_key: z.string(), short_id: z.string(), hint: z.string() })
export const routingSaved = z.object({ ok: z.literal(true), row_version: z.number() })
export const okResponse = z.object({ ok: z.literal(true) })
export const poolCreated = z.object({ id: uuid })

export const serverDeleted = z.object({ ok: z.literal(true), id: uuid })
export const globalRoutingSaved = z.object({ ok: z.literal(true), revision: z.string(), affected_nodes: z.number() })
export const routeGroupUpdated = z.object({ group: routeGroupSchema, affected_nodes: z.number() })
export const groupRoutingSaved = z.object({ ok: z.literal(true), row_version: z.number(), affected_nodes: z.number() })
export const routeGroupDeleted = z.object({ deleted: z.literal(true), affected_nodes: z.number() })
export const deletedResponse = z.object({ deleted: z.literal(true) })

// ---------------------------------------------------------------------------
// 中转、ECH（迁移 00045，node/handler 的 dto_xb_nodes.go）
// ---------------------------------------------------------------------------
/** 中转（Xboard parent_id）：只有自己的名称、地址、端口与可选 SNI / Host，其余全继承父节点；不占端口、不下发配置 */
export const relaySchema = z.object({
  id: uuid,
  parent_node_id: uuid,
  name: z.string(),
  server_host: z.string(),
  server_port: z.number(),
  sni: z.string().nullable(),
  host_header: z.string().nullable(),
  enabled: z.boolean(),
  sort_order: z.number(),
  row_version: z.number(),
  created_at: iso,
  updated_at: iso,
})
export type Relay = z.output<typeof relaySchema>
/** 中转列表；sni_hint 是父节点 TLS 配置给中转 SNI 的提示（REALITY 只能填 server_names 之一、IP 地址父节点要填 SNI），空串表示没有 */
export const relaysResponse = z.object({ relays: z.array(relaySchema), sni_hint: z.string() })

/** 节点 ECH 状态：config_list 是当前钥的 ECHConfigList（标准 base64，没开为 null）；retired_keys 是过渡期内还在下发的旧钥数 */
export const echSchema = z.object({
  supported: z.boolean(),
  unsupported_reason: z.string(),
  enabled: z.boolean(),
  public_name: z.string(),
  config_list: z.string().nullable(),
  config_pem: z.string(),
  created_at: iso.nullable(),
  retired_keys: z.number(),
})
export type NodeECH = z.output<typeof echSchema>
