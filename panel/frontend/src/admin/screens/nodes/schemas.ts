/**
 * [INPUT]: 依赖 zod
 * [OUTPUT]: 对外提供节点页全部接口的 zod schema 与推导类型：节点列表行、AdminNode（写接口回的节点）、协议 schema、服务器 / 节点池的选择项子集、节点身份、探针、单节点与全局路由、各写操作的响应
 * [POS]: admin/screens/nodes 与后端对账的唯一防线：形状取自 api-contract.md 后台-07 · 节点（含 R10 R13 R26 R27 R46 R57）并与 Go json tag 核对；Go 指针字段没有 omitempty，缺值序列化成 null 而不是缺键，所以这些字段写 nullable；nil 切片写 nullable 并归一成 []
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
// 选择项：服务器与节点池（完整形状属第 ③ 步，这里只收节点页要的字段）
// ---------------------------------------------------------------------------
export const serverOptionSchema = z.object({
  id: uuid,
  name: z.string(),
  status: z.string(),
  region: z.string().nullable().optional(),
  public_ipv4: z.string().nullable().optional(),
  capacity_nodes: z.number(),
  node_count: z.number(),
  cpu_bp: z.number().nullable().optional(),
  mem_used_mb: z.number().nullable().optional(),
  mem_total_mb: z.number().nullable().optional(),
})
export const serversResponse = z.object({ servers: list(serverOptionSchema), total: z.number() })
export type ServerOption = z.output<typeof serverOptionSchema>

export const poolOptionSchema = z.object({ id: uuid, code: z.string(), name: z.string(), status: z.string() })
export const poolsResponse = z.object({ pools: list(poolOptionSchema) })
export type PoolOption = z.output<typeof poolOptionSchema>

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
export const deletedResponse = z.object({ deleted: z.literal(true) })
