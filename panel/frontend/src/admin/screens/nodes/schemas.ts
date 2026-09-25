/**
 * [INPUT]: 依赖 zod
 * [OUTPUT]: 对外提供节点与服务器页全部接口的 zod schema 与推导类型：节点列表行、AdminNode（写接口回的节点）、协议 schema、服务器与其下属节点、节点池（members / plan_names / R104 allowed_user_groups）、R108 上线响应 activatedResponse、节点身份、探针、单节点与全局路由、各写操作的响应
 * [POS]: admin/screens/nodes 与后端对账的唯一防线：形状取自 api-contract.md 后台-07 的节点 / 服务器 / 节点池 / 路由四节（含 R10 R13 R26 R27 R46 R56 R57 R77–R79 R104 R105 R108）并与 Go json tag 核对；Go 指针字段没有 omitempty，缺值序列化成 null 而不是缺键，所以这些字段写 nullable；nil 切片写 nullable 并归一成 []
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
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
// 节点：列表行（GET v1/nodes）与写接口回的 AdminNode
// ---------------------------------------------------------------------------
export const SERVING_STATUSES = ['draft', 'active', 'draining', 'disabled', 'retired'] as const
export type ServingStatus = (typeof SERVING_STATUSES)[number]

export const nodeRowSchema = z.object({
  id: uuid,
  node_no: z.number(),
  row_version: z.number(),
  name: z.string(),
  status: z.string(),
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
  health_score: z.number().nullable(),
  applied_config_version: z.number().nullable(),
  desired_config_version: z.number().nullable(),
  last_heartbeat_at: iso.nullable(),
  stale: z.boolean(),
  delivered_to_users: z.boolean(),
  delivery_note: z.string(),
  identity_serial: z.number().nullable(),
  created_at: iso,
  node_type: z.string().nullable(),
  server_host: z.string().nullable(),
  server_port: z.number().nullable(),
  traffic_rate: z.number(),
  display_name: z.string().nullable(),
  country_code: z.string().nullable(),
  kernel: z.string(),
  protocol_config: z.record(z.string(), z.unknown()).nullable().transform((v) => v ?? {}),
  protocol_schema_version: z.number(),
  config_validated_at: iso.nullable(),
  sort_order: z.number(),
  online_users: z.number(),
  online_ips: z.number(),
  traffic_bytes_24h: z.number().int(),
  cpu_percent: z.number().nullable(),
  mem_percent: z.number().nullable(),
  metrics_at: iso.nullable(),
  traffic_bytes: z.number().int(),
  granted_plans: strs,
})
export const nodesResponse = z.object({ nodes: list(nodeRowSchema), total: z.number() })
export type NodeRow = z.output<typeof nodeRowSchema>

export const adminNodeSchema = z.object({
  id: uuid,
  row_version: z.number(),
  name: z.string(),
  server_id: uuid.nullable(),
  pool_id: uuid.nullable(),
  status: z.string(),
  serving_status: z.enum(SERVING_STATUSES),
  node_type: z.string().nullable(),
  server_host: z.string().nullable(),
  server_port: z.number().nullable(),
  kernel: z.string(),
  traffic_rate: z.number(),
  display_name: z.string().nullable(),
  country_code: z.string().nullable(),
  protocol_config: z.unknown(),
  protocol_schema_version: z.number(),
  config_validated_at: iso.nullable(),
  sort_order: z.number(),
  created_at: iso,
  updated_at: iso,
  warnings: z.array(z.string()).optional(),
})
export type AdminNode = z.output<typeof adminNodeSchema>

// ---------------------------------------------------------------------------
// 协议 schema（GET v1/node-protocol-schemas）。除 13 个 stable 外还有
// status=legacy-read-compatible 的 v2ray / hysteria（version 0、数组为 null），只读不可新建
// ---------------------------------------------------------------------------
export const protocolSchemaSchema = z.object({
  node_type: z.string(),
  version: z.number(),
  status: z.string(),
  required: strs,
  allowed_properties: strs,
  methods: z.array(z.string()).optional(),
  enums: z.record(z.string(), z.array(z.string())).optional(),
  property_types: z.record(z.string(), z.enum(['number', 'boolean', 'json'])).optional(),
  sensitive_properties: z.array(z.string()).optional(),
})
export const protocolSchemasResponse = z.object({ schemas: list(protocolSchemaSchema) })
export type ProtocolSchema = z.output<typeof protocolSchemaSchema>

// ---------------------------------------------------------------------------
// 服务器（GET v1/servers 与 /servers/{id}，nodefabric.Server）：指针字段全是 null 而不是缺键
// ---------------------------------------------------------------------------
export const SERVER_STATUSES = ['draft', 'ready', 'draining', 'maintenance', 'unhealthy', 'quarantined', 'retired'] as const
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
  control_node_id: uuid.nullable(),
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
export const serverNodeSchema = z.object({
  id: uuid,
  name: z.string(),
  status: z.string(),
  serving_status: z.string(),
  node_type: z.string().nullable(),
  display_name: z.string().nullable(),
  server_host: z.string().nullable(),
  server_port: z.number().nullable(),
  kernel: z.string(),
  traffic_rate: z.number(),
  config_validated_at: iso.nullable(),
  last_heartbeat_at: iso.nullable(),
  created_at: iso,
})
export const serverNodesResponse = z.object({ nodes: z.array(serverNodeSchema), total: z.number() })
export type ServerNode = z.output<typeof serverNodeSchema>

// ---------------------------------------------------------------------------
// 节点池（GET v1/node-pools）：members 不含已销毁节点，plan_names 去重（两者 SQL 里 coalesce 过，恒为数组）；
// allowed_user_groups 是 R104「仅限用户组」名单（空 = 不限定），Go 的 nil 切片可能编成 null，归一成 []
// ---------------------------------------------------------------------------
export const POOL_STATUSES = ['active', 'draining', 'disabled'] as const
export type PoolStatus = (typeof POOL_STATUSES)[number]

export const poolSchema = z.object({
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
  server_token: z.object({ present: z.boolean(), issued_at: iso.optional(), issued_by: uuid.optional(), issued_by_name: z.string().optional() }),
  bootstrap_tokens_pending: z.number(),
})
export type NodeIdentity = z.output<typeof identitySchema>

export const metricPointSchema = z.object({
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
export type NodeMetrics = z.output<typeof metricsSchema>

export const outboundSchema = z.object({ tag: z.string(), type: z.string(), settings: z.unknown() })
export const routeSchema = z.object({
  priority: z.number(),
  matcher: z.record(z.string(), z.unknown()),
  outbound_tag: z.string(),
  enabled: z.boolean(),
  note: z.string(),
})
export type Outbound = z.output<typeof outboundSchema>
export type Route = z.output<typeof routeSchema>
export const nodeRoutingSchema = z.object({ row_version: z.number(), outbounds: list(outboundSchema), routes: list(routeSchema) })
export type NodeRouting = z.output<typeof nodeRoutingSchema>
export const globalRoutingSchema = z.object({ revision: z.string(), outbounds: list(outboundSchema), routes: list(routeSchema), online_nodes: z.number() })
export type GlobalRouting = z.output<typeof globalRoutingSchema>

// ---------------------------------------------------------------------------
// 写操作的响应
// ---------------------------------------------------------------------------
export const okUpdated = z.object({ ok: z.literal(true), updated: z.number() })
export const batchStatusResponse = z.object({ ok: z.literal(true), updated: z.number(), serving_status: z.string() })
export const bootstrapTokenResponse = z.object({ token: z.string(), expires_at: iso, install_command: z.string() })
export type BootstrapToken = z.output<typeof bootstrapTokenResponse>
export const serverTokenResponse = z.object({ token: z.string(), node_type: z.string(), panel_url: z.string(), install_command: z.string(), hint: z.string() })
export type ServerToken = z.output<typeof serverTokenResponse>
export const realityKeypairResponse = z.object({ private_key: z.string(), public_key: z.string(), short_id: z.string(), hint: z.string() })
export const publishResponse = z.object({ config_id: uuid, version: z.number(), scope: z.string(), affected_nodes: z.number() })
export const routingSaved = z.object({ ok: z.literal(true), row_version: z.number() })
export const okResponse = z.object({ ok: z.literal(true) })
export const poolCreated = z.object({ id: uuid })

/**
 * R108 POST v1/nodes/{id}/activate：契约写「AdminNode（同 GET v1/nodes 的 Node）另带 warnings」，两种形状都有这几个字段，
 * 页面也只用这几个（成功后重拉列表），所以只收它们；后端四 ④ 定了形状再收紧
 */
export const activatedResponse = z.object({
  id: uuid,
  row_version: z.number(),
  status: z.string(),
  serving_status: z.enum(SERVING_STATUSES),
  warnings: z.array(z.string()).optional(),
})
export const serverDeleted = z.object({ ok: z.literal(true), id: uuid })
export const globalRoutingSaved = z.object({ ok: z.literal(true), revision: z.string(), affected_nodes: z.number() })
export const deletedResponse = z.object({ deleted: z.literal(true) })
