/**
 * [INPUT]: 依赖 node:crypto 的 randomBytes / randomUUID，依赖 ../types 的 MockModule / MockContext / MockResult / Json，依赖 ./nodes-infra 的服务器 / 节点池 / 全局路由数据、infraRoutes 与 validateRouting，依赖 ./node-schemas 的协议 schema 夹具
 * [OUTPUT]: 对外提供 nodes 模块的假接口 MockModule，以及测试用的 storedProtocolConfig（读库里未抹敏的协议配置）
 * [POS]: dev/mock/admin 的「节点与服务器（后台-07）」假接口，归后台前端二。节点在这里：列表（含 R27 分页与 R46 国家、24h 流量、探针）、新建 / 编辑 / 复制 / 迁移（保留规则 5）/ 排序 / 批量改服务状态（合法边）/ 退役（R57）/ 上线（R108 / R113 activate：判断顺序与 409 文案照 nodefabric.ActivateNode，已 active 先于版本号回 200，服务器进 ready，无池或池没绑套餐时带 warnings、没有提示不出现这个键）/ 删除、列表的交付提示按 R105（先服务状态、再有没有池、再心跳）、PATCH 缺席的敏感键从库里补回（R106，mask_password 跟着 mask 开关走，R107）、协议 schema、REALITY 密钥、一键安装令牌、服务端令牌（R13）、吊销身份、发布配置、探针、单节点路由（R26，校验与全局共用 validateRouting）、节点身份（R46）；服务器、节点池、全局路由在 nodes-infra.ts（第 ③ 步），由 infraRoutes(store) 并入本模块，数据与节点共享。权限 / reauth / 幂等 scope / 校验文案照契约与 Go 处理器
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomBytes, randomUUID } from 'node:crypto'
import type { Json, MockContext, MockModule, MockResult } from '../types.ts'
import { emptyBody, globalRouting, infraRoutes, keepAlive, pools, servers, validateRouting } from './nodes-infra.ts'
import { NODE_PROTOCOL_SCHEMAS } from './node-schemas.ts'

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------
const err = (status: number, code: string, message: string, fields?: Record<string, string>): MockResult => ({ status, body: { error: { code, message, ...(fields ? { fields } : {}) } } })
const invalid = (fields: Record<string, string>) => err(422, 'validation_failed', '请求参数校验未通过', fields)
const notFound = (message = '资源不存在或无权访问') => err(404, 'not_found', message)
const reply = (ctx: MockContext, r: MockResult) => ctx.send(r.status, r.body)
const text = (v: unknown) => (typeof v === 'string' ? v : '')
const int = (v: unknown) => (typeof v === 'number' && Number.isInteger(v) ? v : null)
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const ago = (sec: number) => new Date(Date.now() - sec * 1000).toISOString()
const GB = 1024 ** 3
const unknownField = (body: Json, allowed: readonly string[]) => Object.keys(body).find((k) => !allowed.includes(k)) ?? null

type Schema = { node_type: string; version: number; status: string; required: readonly string[] | null; allowed_properties: readonly string[] | null; enums?: Readonly<Record<string, readonly string[]>>; methods?: readonly string[]; property_types?: Readonly<Record<string, string>> }
const SCHEMAS = NODE_PROTOCOL_SCHEMAS.schemas as unknown as readonly Schema[]

// ---------------------------------------------------------------------------
// 节点
// ---------------------------------------------------------------------------
interface Node {
  id: string
  node_no: number
  row_version: number
  name: string
  status: string
  serving_status: string
  server_id: string | null
  pool_id: string | null
  node_type: string | null
  server_host: string | null
  server_port: number | null
  kernel: string
  traffic_rate: number
  display_name: string | null
  country_code: string | null
  protocol_config: Json
  protocol_schema_version: number
  sort_order: number
  last_heartbeat_at: string | null
  identity_serial: number | null
  online_users: number
  online_ips: number
  traffic_bytes_24h: number
  created_at: string
  updated_at: string
  routing: { outbounds: Array<{ tag: string; type: string; settings: unknown }>; routes: Array<Json> }
  serverToken: { present: boolean; issued_at?: string; issued_by?: string; issued_by_name?: string }
  identityRevoked: boolean
}

let nodeNo = 100
const serverAt = (i: number) => servers[i]!
function node(p: Partial<Node> & Pick<Node, 'name' | 'node_type' | 'serving_status'>, server = 0): Node {
  nodeNo += 1
  const created = ago(86400 * 30)
  return {
    id: randomUUID(),
    node_no: nodeNo,
    row_version: 3,
    status: p.serving_status === 'draft' ? 'draft' : p.serving_status === 'retired' ? 'retired' : 'active',
    server_id: serverAt(server).id,
    pool_id: pools[0]!.id,
    server_host: `n${nodeNo}.pandora.run`,
    server_port: 443,
    kernel: 'auto',
    traffic_rate: 1,
    display_name: null,
    country_code: null,
    protocol_config: {},
    protocol_schema_version: 1,
    sort_order: (nodeNo - 100) * 10,
    last_heartbeat_at: ago(4),
    identity_serial: 3,
    online_users: 0,
    online_ips: 0,
    traffic_bytes_24h: 0,
    created_at: created,
    updated_at: created,
    routing: { outbounds: [], routes: [] },
    serverToken: { present: true, issued_at: ago(86400 * 20), issued_by: randomUUID(), issued_by_name: '林舟' },
    identityRevoked: false,
    ...p,
  }
}
const store: Node[] = [
  node({ name: '香港 01 · 原生', node_type: 'vless', serving_status: 'active', country_code: 'HK', online_users: 412, online_ips: 530, traffic_bytes_24h: Math.round(1.8 * 1024 * GB), protocol_config: { network: 'tcp', tls: 2, flow: 'xtls-rprx-vision', reality_settings: { dest: 'www.microsoft.com:443', server_name: 'www.microsoft.com', public_key: 'Hx3mQe9Z2y5rTq8WkVbN1cXyL0pOaSdFgHjK7uIoP4A', short_id: '6ba85179' } } }, 0),
  node({ name: '香港 02 · IPLC', node_type: 'hysteria2', serving_status: 'active', country_code: 'HK', server_port: 8443, online_users: 288, online_ips: 301, traffic_bytes_24h: Math.round(1.12 * 1024 * GB), protocol_config: { cert_path: '/etc/pandora/tls.crt', key_path: '/etc/pandora/tls.key', 'bandwidth': { up: 200, down: 1000 } } }, 0),
  node({ name: '东京 03', node_type: 'trojan', serving_status: 'active', country_code: 'JP', online_users: 356, online_ips: 402, traffic_bytes_24h: Math.round(1.49 * 1024 * GB), protocol_config: { network: 'tcp', tls: 2, reality_settings: { dest: 'www.apple.com:443', server_name: 'www.apple.com' } } }, 1),
  node({ name: '新加坡 02', node_type: 'shadowsocks', serving_status: 'active', country_code: 'SG', server_port: 8388, last_heartbeat_at: ago(900), online_users: 0, traffic_bytes_24h: 864 * GB, protocol_config: { cipher: 'aes-256-gcm' } }, 2),
  node({ name: '洛杉矶 01', node_type: 'vless', serving_status: 'draining', country_code: 'US', pool_id: pools[1]!.id, online_users: 96, online_ips: 110, traffic_bytes_24h: 540 * GB, protocol_config: { network: 'ws', tls: 0, ws_path: '/ws' } }, 3),
  node({ name: '首尔 01 · 灰度', node_type: 'tuic', serving_status: 'disabled', country_code: 'KR', pool_id: pools[2]!.id, protocol_config: { cert_path: '/etc/pandora/tls.crt', key_path: '/etc/pandora/tls.key' } }, 1),
  node({ name: '新加坡 03（草稿）', node_type: 'anytls', serving_status: 'draft', country_code: 'SG', last_heartbeat_at: null, identity_serial: null, serverToken: { present: false }, protocol_config: {} }, 2),
  node({ name: '台北 01', node_type: 'vmess', serving_status: 'retired', country_code: 'TW', last_heartbeat_at: ago(86400 * 9), protocol_config: { network: 'ws', tls: 0 } }, 5),
  // R105：在役但没划进节点池，不服务任何用户
  node({ name: '香港 03（未入池）', node_type: 'shadowsocks', serving_status: 'active', country_code: 'HK', server_port: 8389, pool_id: null, protocol_config: { cipher: 'aes-256-gcm' } }, 0),
  // R108：新服务器（大阪，草稿）上刚接入完的节点，生命周期停在 attesting，等「上线」一步推到 active
  node({ name: '大阪 01（待上线）', node_type: 'shadowsocks', serving_status: 'draft', status: 'attesting', country_code: 'JP', server_port: 8388, last_heartbeat_at: ago(20), identity_serial: 1, pool_id: pools[1]!.id, protocol_config: { cipher: 'aes-256-gcm' } }, 7),
]
keepAlive(store, (n) => n.serving_status !== 'retired' && n.status !== 'destroyed' && !n.identityRevoked)
store[0]!.routing = { outbounds: [], routes: [{ priority: 10, matcher: { domain_suffix: ['openai.com'] }, outbound_tag: 'US-LAX-01', enabled: true, note: '' }] }

const NO_POOL_NOTE = '未划入节点池，不服务任何用户'

// R108：接入尾段的生命周期（attesting 之后、active 之前），「上线」按 00005 的合法边逐条推进到 active
const ACTIVATABLE = ['attesting', 'installing', 'validating', 'standby', 'canary']
// 服务器状态机里能进 ready 的状态（已 ready 的不动）
const SERVER_TO_READY = ['draft', 'draining', 'maintenance']

/** nodefabric.activateRefusal：不在接入尾段时为什么不能上线 */
function activateRefusal(status: string): string {
  if (['draft', 'provisioning', 'bootstrapping'].includes(status)) return `节点还没完成接入（${status}），等接入提交后再上线`
  if (['provisioning_failed', 'bootstrap_failed', 'destroy_failed', 'quarantined', 'retired'].includes(status)) return `节点处于 ${status}，不能上线；需要重新接入或先处理这个状态`
  return `节点处于 ${status}，不在接入尾段，请用启用或状态操作恢复服务`
}

/** nodefabric.activationWarnings：上线成功但不会服务任何人的提示（与 delivery_note 同口径，R105 / R113） */
function activationWarnings(n: Node): string[] {
  if (n.pool_id === null) return [NO_POOL_NOTE]
  return pools.find((p) => p.id === n.pool_id)?.plans.length ? [] : ['所在节点池没有绑定任何套餐，暂时不服务任何用户']
}

function listRow(n: Node) {
  const srv = servers.find((s) => s.id === n.server_id)
  const pool = pools.find((p) => p.id === n.pool_id)
  const stale = !n.last_heartbeat_at || Date.now() - new Date(n.last_heartbeat_at).getTime() > 90_000
  const heartbeatOk = !!n.last_heartbeat_at && Date.now() - new Date(n.last_heartbeat_at).getTime() < 600_000
  // R105：先看服务状态，再看有没有池，再看心跳；无池节点不服务任何订阅
  const delivered = n.serving_status === 'active' && n.pool_id !== null && heartbeatOk
  const note = delivered
    ? ''
    : n.serving_status !== 'active'
      ? '服务状态不是 active，不会下发给用户'
      : n.pool_id === null
        ? NO_POOL_NOTE
        : !n.last_heartbeat_at
          ? '从未心跳，不会下发给用户'
          : '超过 10 分钟没有心跳，已停止下发'
  const cpu = srv?.probe ? srv.probe.cpu_bp / 100 : null
  return {
    id: n.id,
    node_no: n.node_no,
    row_version: n.row_version,
    name: n.name,
    status: n.status,
    serving_status: n.serving_status,
    server_id: n.server_id,
    server_name: srv?.name ?? null,
    pool_id: n.pool_id,
    pool_name: pool?.name ?? null,
    agent_version: n.last_heartbeat_at ? 'core r53' : null,
    hostname: n.last_heartbeat_at ? srv?.name ?? null : null,
    public_ipv4: srv?.public_ipv4 ?? null,
    cpu_cores: 4,
    memory_mb: 4096,
    disk_gb: 80,
    health_score: n.last_heartbeat_at ? 96 : null,
    applied_config_version: n.last_heartbeat_at ? 42 : null,
    desired_config_version: n.serving_status === 'retired' ? null : 42,
    last_heartbeat_at: n.last_heartbeat_at,
    stale,
    delivered_to_users: delivered,
    delivery_note: note,
    identity_serial: n.identity_serial,
    created_at: n.created_at,
    node_type: n.node_type,
    server_host: n.server_host,
    server_port: n.server_port,
    traffic_rate: n.traffic_rate,
    display_name: n.display_name,
    country_code: n.country_code,
    kernel: n.kernel,
    protocol_config: redact(n.protocol_config),
    protocol_schema_version: n.protocol_schema_version,
    config_validated_at: n.node_type ? n.updated_at : null,
    sort_order: n.sort_order,
    online_users: n.online_users,
    online_ips: n.online_ips,
    traffic_bytes_24h: n.traffic_bytes_24h,
    cpu_percent: cpu,
    mem_percent: srv?.probe ? Math.round((srv.probe.mem_used_mb / srv.probe.mem_total_mb) * 1000) / 10 : null,
    metrics_at: cpu === null ? null : ago(20),
    traffic_bytes: n.traffic_bytes_24h * 30,
    granted_plans: pool?.plans.length ? pool.plans : null,
  }
}

/** nodefabric.RedactProtocolConfig：按名字在任意深度删掉敏感键 */
// R107：mask_password（mKCP）也在抹敏名单里
const SENSITIVE = new Set(['password', 'passwd', 'secret', 'token', 'private_key', 'private-key', 'psk', 'obfs_password', 'obfs-password', 'mask_password'])
// R107：跟着开关走的密钥——请求里带了开关键且值没变时才从库里补回（关掉掩码、去掉开关、切出 mKCP 都不补）
const SECRET_SWITCH: Record<string, string> = { mask_password: 'mask' }

/**
 * R106：PATCH 的 protocol_config 是整体替换，但**缺席**的敏感键按原路径从库里补回；显式给了的（含空串与 null）
 * 以请求为准；普通键缺席仍是删除；数组两边长度相同才按下标补。换协议类型时调用方不补。返回补好的新对象
 */
function restoreSecrets(stored: unknown, incoming: unknown): unknown {
  if (Array.isArray(stored) && Array.isArray(incoming)) return stored.length === incoming.length ? incoming.map((v, i) => restoreSecrets(stored[i], v)) : incoming
  if (!stored || !incoming || typeof stored !== 'object' || typeof incoming !== 'object' || Array.isArray(stored) || Array.isArray(incoming)) return incoming
  const from = stored as Json
  const out: Json = { ...(incoming as Json) }
  for (const [k, v] of Object.entries(from)) {
    if (k in out) out[k] = restoreSecrets(v, out[k])
    else if (SENSITIVE.has(k.toLowerCase())) {
      const sw = SECRET_SWITCH[k]
      if (!sw || (sw in out && out[sw] === from[sw])) out[k] = v
    }
  }
  return out
}
function redact(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(redact)
  if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value).filter(([k]) => !SENSITIVE.has(k.toLowerCase())).map(([k, v]) => [k, redact(v)]))
  return value
}

function adminNode(n: Node, warnings?: string[]) {
  const pick = <K extends keyof Node>(...keys: K[]) => Object.fromEntries(keys.map((k) => [k, n[k]]))
  return {
    ...pick('id', 'row_version', 'name', 'server_id', 'pool_id', 'status', 'serving_status', 'node_type', 'server_host', 'server_port', 'kernel', 'traffic_rate', 'display_name', 'country_code', 'protocol_schema_version', 'sort_order', 'created_at', 'updated_at'),
    protocol_config: redact(n.protocol_config),
    config_validated_at: n.updated_at,
    ...(warnings?.length ? { warnings } : {}),
  }
}

function flatten(obj: unknown, prefix = ''): Array<[string, unknown]> {
  if (!obj || typeof obj !== 'object' || Array.isArray(obj)) return [[prefix, obj]]
  return Object.entries(obj).flatMap(([k, v]) => (v && typeof v === 'object' && !Array.isArray(v) && !['headers', 'padding_scheme'].includes(k) ? flatten(v, `${prefix}${k}.`) : [[`${prefix}${k}`, v]]))
}

/** 简化的协议校验：协议必须 stable、字段在 allowed 内、必填齐、枚举值合法；vless tls=2 需要 REALITY 四项 */
function validateProtocol(nodeType: string, config: unknown): Record<string, string> {
  const s = SCHEMAS.find((x) => x.node_type === nodeType)
  if (!s || s.status !== 'stable') return { node_type: `不支持的协议：${nodeType}` }
  if (!config || typeof config !== 'object' || Array.isArray(config)) return { protocol_config: '必须是 JSON 对象' }
  const fields: Record<string, string> = {}
  const flat = new Map(flatten(config))
  for (const [path, value] of flat) {
    if (!(s.allowed_properties ?? []).includes(path)) fields[`protocol_config.${path}`] = '不支持的字段'
    const options = s.enums?.[path] ?? (path === 'cipher' ? s.methods : undefined)
    if (options && !options.includes(String(value))) fields[`protocol_config.${path}`] = `只能是 ${options.join(' / ')}`
  }
  for (const path of s.required ?? []) if (!flat.has(path)) fields[`protocol_config.${path}`] = '必填'
  if (nodeType === 'vless' && String(flat.get('tls')) === '2') {
    for (const key of ['dest', 'server_name', 'private_key', 'short_id']) {
      if (!flat.get(`reality_settings.${key}`)) fields[`protocol_config.${key}`] = `REALITY 需要 ${key}`
    }
  }
  return fields
}

const EDGES: Readonly<Record<string, readonly string[]>> = {
  draft: ['active', 'disabled', 'retired'],
  active: ['draining', 'disabled'],
  draining: ['active', 'disabled', 'retired'],
  disabled: ['draft', 'active', 'retired'],
  retired: [],
}

function basicErrors(body: Json, creating: boolean): Record<string, string> {
  const f: Record<string, string> = {}
  const name = body.name === undefined && !creating ? null : text(body.name).trim()
  if (name !== null && (!name || [...name].length > 120)) f.name = '名称必填，不超过 120 字'
  if (creating && !servers.some((s) => s.id === body.server_id)) f.server_id = '服务器不存在'
  if (body.pool_id && !pools.some((p) => p.id === body.pool_id)) f.pool_id = '资源池不存在'
  if ((creating || body.server_host !== undefined) && !/^[A-Za-z0-9.-]+$/.test(text(body.server_host))) f.server_host = '必须是 IP 或 ASCII 主机名'
  if ((creating || body.server_port !== undefined) && !(int(body.server_port)! >= 1 && int(body.server_port)! <= 65535)) f.server_port = '端口 1–65535'
  if (body.traffic_rate !== undefined && !(Number(body.traffic_rate) > 0)) f.traffic_rate = '必须大于 0'
  if (body.country_code != null && body.country_code !== '' && !/^[A-Za-z]{2}$/.test(text(body.country_code))) f.country_code = '必须是两位字母国家代码'
  return f
}

const findNode = (id: string | undefined) => store.find((n) => n.id === id && n.status !== 'destroyed')

/** 只给 tests/mock-admin-nodes.test.ts 核对 R106 / R107 的补回：接口永远抹掉敏感键，测试只能从库里看 */
export const storedProtocolConfig = (id: string) => structuredClone(findNode(id)?.protocol_config ?? null)
const touch = (n: Node) => {
  n.row_version += 1
  n.updated_at = new Date().toISOString()
}
const conflict = (n: Node) => err(409, 'conflict', '节点已被其他人修改，请刷新后重试', { row_version: `current=${n.row_version}` })

// ---------------------------------------------------------------------------
// 路由
// ---------------------------------------------------------------------------
export const nodes: MockModule = {
  routes: {
    ...infraRoutes(store),
    'GET /v1/node-protocol-schemas': (ctx) => {
      if (!ctx.requirePermission('node.read')) return
      ctx.send(200, NODE_PROTOCOL_SCHEMAS)
    },
    'GET /v1/nodes': (ctx) => {
      if (!ctx.requirePermission('node.read')) return
      const withRetired = ctx.query.get('include_retired') === '1'
      const limit = Number(ctx.query.get('limit'))
      const offset = Number(ctx.query.get('offset')) || 0
      const rows = store.filter((n) => n.status !== 'destroyed' && (withRetired || n.serving_status !== 'retired')).sort((a, b) => a.sort_order - b.sort_order || a.node_no - b.node_no)
      const size = limit >= 1 && limit <= 1000 ? limit : 500
      ctx.send(200, { nodes: rows.slice(offset, offset + size).map(listRow), total: rows.length })
    },
    'POST /v1/nodes': async (ctx) => {
      if (!ctx.requirePermission('node.provision')) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('node_create', () => {
        const extra = unknownField(body, ['name', 'server_id', 'node_type', 'server_host', 'server_port', 'protocol_config', 'pool_id', 'kernel', 'traffic_rate', 'display_name', 'sort_order', 'country_code'])
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        const fields = { ...basicErrors(body, true), ...validateProtocol(text(body.node_type), body.protocol_config) }
        if (Object.keys(fields).length) return invalid(fields)
        const srv = servers.find((s) => s.id === body.server_id)!
        if (srv.status !== 'ready' && srv.status !== 'draft') return err(409, 'conflict', '目标服务器当前不接受节点')
        if (store.some((n) => n.name === text(body.name).trim() && n.status !== 'destroyed')) return err(409, 'conflict', '节点名称已存在')
        const n = node({
          name: text(body.name).trim(),
          node_type: text(body.node_type),
          serving_status: 'draft',
          server_host: text(body.server_host),
          server_port: int(body.server_port),
          pool_id: text(body.pool_id) || null,
          kernel: text(body.kernel) || 'auto',
          traffic_rate: Number(body.traffic_rate) > 0 ? Number(body.traffic_rate) : 1,
          display_name: text(body.display_name) || null,
          country_code: text(body.country_code).toUpperCase() || null,
          protocol_config: body.protocol_config as Json,
          last_heartbeat_at: null,
          identity_serial: null,
          serverToken: { present: false },
        })
        n.server_id = srv.id
        n.row_version = 1
        store.push(n)
        return { status: 201, body: adminNode(n) }
      })
    },
    'PATCH /v1/nodes/:id': async (ctx) => {
      if (!ctx.requirePermission('node.write')) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const extra = unknownField(body, ['row_version', 'name', 'pool_id', 'node_type', 'server_host', 'server_port', 'kernel', 'traffic_rate', 'display_name', 'protocol_config', 'country_code'])
      if (extra) return ctx.fail(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
      const n = findNode(ctx.params.id)
      if (!n) return reply(ctx, notFound('节点不存在'))
      if (int(body.row_version) === null) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { row_version: '必填' })
      if (body.row_version !== n.row_version) return reply(ctx, conflict(n))
      const nodeType = body.node_type === undefined ? n.node_type ?? '' : text(body.node_type)
      // 换协议类型时不补旧密钥（R106）
      const config = body.protocol_config === undefined ? n.protocol_config : nodeType === n.node_type ? restoreSecrets(n.protocol_config, body.protocol_config) : body.protocol_config
      const touched = ['node_type', 'server_host', 'server_port', 'kernel', 'protocol_config'].some((k) => body[k] !== undefined)
      const fields = { ...basicErrors(body, false), ...(touched ? validateProtocol(nodeType, config) : {}) }
      if (Object.keys(fields).length) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', fields)
      if (body.name !== undefined && store.some((x) => x !== n && x.name === text(body.name).trim())) return ctx.fail(409, 'conflict', '节点名称已存在')
      if (body.name !== undefined) n.name = text(body.name).trim()
      if (body.pool_id !== undefined) n.pool_id = text(body.pool_id) || null
      if (body.node_type !== undefined) n.node_type = nodeType
      if (body.server_host !== undefined) n.server_host = text(body.server_host)
      if (body.server_port !== undefined) n.server_port = int(body.server_port)
      if (body.kernel !== undefined) n.kernel = text(body.kernel) || 'auto'
      if (body.traffic_rate !== undefined) n.traffic_rate = Number(body.traffic_rate)
      if (body.display_name !== undefined) n.display_name = text(body.display_name) || null
      if (body.country_code !== undefined) n.country_code = text(body.country_code).toUpperCase() || null
      if (body.protocol_config !== undefined) n.protocol_config = config as Json
      touch(n)
      ctx.send(200, adminNode(n))
    },
    'POST /v1/nodes/:id/copy': async (ctx) => {
      if (!ctx.requirePermission('node.provision')) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('node_copy', () => {
        const src = findNode(ctx.params.id)
        if (!src) return notFound('节点不存在')
        if (body.row_version !== src.row_version) return conflict(src)
        const name = text(body.name).trim() || `${src.name}-copy`
        if (store.some((n) => n.name === name && n.status !== 'destroyed')) return err(409, 'conflict', '节点名称已存在')
        const target = body.target_server_id ? servers.find((s) => s.id === body.target_server_id) : servers.find((s) => s.id === src.server_id)
        if (!target) return invalid({ target_server_id: '服务器不存在' })
        if (target.status !== 'ready') return err(409, 'conflict', '目标服务器当前不接受节点')
        const copy = node({ ...structuredClone({ ...src, routing: body.copy_routing ? src.routing : { outbounds: [], routes: [] } }), name, serving_status: 'draft', status: 'draft', last_heartbeat_at: null, identity_serial: null, online_users: 0, online_ips: 0, traffic_bytes_24h: 0, serverToken: { present: false } })
        // node() 先给新 id 与编号，再被源节点的字段覆盖：复制出来的必须是新节点
        copy.id = randomUUID()
        copy.node_no = nodeNo
        copy.server_id = target.id
        copy.sort_order = src.sort_order + 1
        copy.row_version = 1
        store.push(copy)
        return { status: 201, body: adminNode(copy) }
      })
    },
    'POST /v1/nodes/:id/move': async (ctx) => {
      if (!ctx.requirePermission('node.provision')) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('node_server_move', () => {
        const n = findNode(ctx.params.id)
        if (!n) return notFound('节点不存在')
        if (body.row_version !== n.row_version) return conflict(n)
        const target = servers.find((s) => s.id === body.server_id)
        if (!target) return invalid({ server_id: '服务器不存在' })
        if (target.id === n.server_id) return { status: 200, body: adminNode(n) }
        if (n.serving_status === 'active' || n.serving_status === 'draining') return err(409, 'conflict', '活动或排空中的节点不能移动')
        if (n.last_heartbeat_at || n.identity_serial) {
          return err(409, 'conflict', '节点仍绑定控制面或 Agent 资产', { control_node: '0', active_identities: n.identityRevoked ? '0' : '1', runtime_instances: '1', metrics: '288', pending_tasks: '0', provisioning: '1', bootstrap_tokens: '0', config_applications: '12', usage_sources: '1', usage_batches: '30', traffic_reports: '1440', active_server_token: n.serverToken.present ? '1' : '0' })
        }
        if (target.status !== 'ready') return err(409, 'conflict', '目标服务器当前不接受节点')
        n.server_id = target.id
        touch(n)
        return { status: 200, body: adminNode(n) }
      })
    },
    'PUT /v1/nodes/order': async (ctx) => {
      if (!ctx.requirePermission('node.write')) return
      const body = await ctx.body()
      const items = Array.isArray(body?.items) ? (body!.items as Json[]) : []
      if (items.length < 1 || items.length > 200) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { items: '一次 1–200 项' })
      for (const it of items) {
        const n = findNode(text(it.id))
        if (!n) return reply(ctx, notFound('节点不存在'))
        if (it.row_version !== n.row_version) return reply(ctx, conflict(n))
      }
      for (const it of items) {
        const n = findNode(text(it.id))!
        n.sort_order = int(it.sort_order) ?? n.sort_order
        touch(n)
      }
      ctx.send(200, { ok: true, updated: items.length })
    },
    'POST /v1/nodes/status:batch': async (ctx) => {
      if (!ctx.requirePermission('node.lifecycle')) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('node_status_batch', () => {
        const items = Array.isArray(body.items) ? (body.items as Json[]) : []
        const to = text(body.serving_status)
        if (items.length < 1 || items.length > 100) return invalid({ items: '一次 1–100 项' })
        if (!(to in EDGES)) return invalid({ serving_status: '不支持的服务状态' })
        for (const it of items) {
          const n = findNode(text(it.id))
          if (!n) return notFound('节点不存在')
          if (it.row_version !== n.row_version) return conflict(n)
          if (!EDGES[n.serving_status]!.includes(to)) return err(409, 'conflict', '不允许的节点服务状态转换', { serving_status: `${n.serving_status} -> ${to}` })
          if (to === 'active' && (!n.node_type || servers.find((s) => s.id === n.server_id)?.status !== 'ready')) return err(409, 'conflict', '节点协议或服务器状态未满足启用条件')
        }
        for (const it of items) {
          const n = findNode(text(it.id))!
          n.serving_status = to
          if (to === 'active' && n.status === 'draft') n.status = 'active'
          touch(n)
        }
        return { status: 200, body: { ok: true, updated: items.length, serving_status: to } }
      })
    },
    'POST /v1/nodes/bootstrap-token': async (ctx) => {
      if (!ctx.requirePermission('node.provision') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('node_bootstrap_token_issue', () => {
        const name = text(body.node_name).trim()
        if (!name) return invalid({ node_name: '必填' })
        const n = store.find((x) => x.name === name)
        if (n && (n.serving_status === 'retired' || n.status === 'destroyed')) return err(409, 'conflict', '已退役或销毁节点不能签发引导令牌')
        const ttl = int(body.ttl_minutes)
        const minutes = ttl && ttl >= 1 && ttl <= 30 ? ttl : 20
        return {
          status: 201,
          body: { token: `pbt_${randomBytes(24).toString('base64url')}`, expires_at: new Date(Date.now() + minutes * 60_000).toISOString(), install_command: 'curl -fsSL https://panel.pandora.run/install.sh | sudo bash -s -- --enroll' },
        }
      })
    },
    'POST /v1/nodes/reality-keypair': (ctx) => {
      if (!ctx.requirePermission('node.read')) return
      ctx.send(200, { private_key: randomBytes(32).toString('base64url'), public_key: randomBytes(32).toString('base64url'), short_id: randomBytes(4).toString('hex'), hint: '私钥只在保存时写入，之后读接口不回显' })
    },
    'DELETE /v1/nodes/:id': async (ctx) => {
      if (!ctx.requirePermission('node.lifecycle') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body || emptyBody(ctx)) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const n = findNode(ctx.params.id)
      if (!n) return reply(ctx, notFound('节点不存在'))
      if (int(body.row_version) && body.row_version !== n.row_version) return reply(ctx, conflict(n))
      if (n.serving_status === 'active' || n.serving_status === 'draining') return ctx.fail(422, 'validation_failed', '节点还在服务中')
      if (!['draft', 'provisioning_failed', 'bootstrap_failed', 'retired', 'destroy_failed'].includes(n.status)) return ctx.fail(422, 'validation_failed', '节点当前状态不能直接销毁，请先将其退役')
      n.status = 'destroyed'
      n.name = `${n.name}#destroyed-${Date.now()}`
      ctx.send(200, { deleted: true })
    },
    // R108 / R113：一步上线，照 nodefabric.ActivateNode：node.lifecycle、幂等 node_activate、无 reauth；
    // 判断顺序同 Go——版本号非法 400、节点不存在 404、已是 active 回 200 不改动（先于版本号）、版本冲突 409，
    // 再依次是接入尾段、节点身份、协议就绪、服务器；服务器跟着进 ready；warnings 没有提示时不出现
    'POST /v1/nodes/:id/activate': async (ctx) => {
      if (!ctx.requirePermission('node.lifecycle')) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('node_activate', () => {
        const extra = Object.keys(body).find((k) => k !== 'row_version')
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        if (!(typeof body.row_version === 'number' && body.row_version > 0)) return invalid({ row_version: '必须提供正整数版本号' })
        const n = findNode(ctx.params.id)
        if (!n) return notFound('节点不存在')
        if (n.status === 'active') return { status: 200, body: adminNode(n, activationWarnings(n)) }
        if (body.row_version !== n.row_version) return conflict(n)
        if (!ACTIVATABLE.includes(n.status)) return err(409, 'conflict', activateRefusal(n.status))
        if (!n.identity_serial || n.identityRevoked) return err(409, 'conflict', '节点没有有效的节点身份，请重新接入后再上线')
        if (!n.node_type || n.server_port === null) return err(409, 'conflict', '节点的协议配置还没就绪（协议类型、端口与稳定协议配置），先保存协议再上线')
        if (!n.server_id) return err(409, 'conflict', '节点没有绑定服务器，不能上线')
        const srv = servers.find((s) => s.id === n.server_id)
        if (!srv) return err(409, 'conflict', '节点所在的服务器已删除，不能上线')
        if (srv.status !== 'ready' && !SERVER_TO_READY.includes(srv.status)) return err(409, 'conflict', `节点所在的服务器处于 ${srv.status}，不能进入 ready，先处理服务器状态再上线`)
        n.status = 'active'
        n.serving_status = 'active'
        srv.status = 'ready'
        touch(n)
        return { status: 200, body: adminNode(n, activationWarnings(n)) }
      })
    },

    'POST /v1/nodes/:id/retire': async (ctx) => {
      if (!ctx.requirePermission('node.lifecycle') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('node_retire', () => {
        const n = findNode(ctx.params.id)
        if (!n) return notFound('节点不存在')
        if (body.row_version !== n.row_version) return conflict(n)
        if (n.serving_status === 'retired') return err(409, 'conflict', '节点已经退役')
        n.serving_status = 'retired'
        if (n.status !== 'draft') n.status = 'retired'
        n.identityRevoked = true
        touch(n)
        return { status: 200, body: adminNode(n) }
      })
    },
    'POST /v1/nodes/:id/revoke-identity': (ctx) => {
      if (!ctx.requirePermission('node.identity.revoke') || !ctx.requireReauth()) return
      const n = findNode(ctx.params.id)
      if (!n || !n.identity_serial || n.identityRevoked) return reply(ctx, notFound('该节点没有有效身份'))
      n.identityRevoked = true
      ctx.send(200, { ok: true })
    },
    'POST /v1/nodes/config/publish': async (ctx) => {
      if (!ctx.requirePermission('node.config.publish') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('node_config_publish', () => {
        if (!['global', 'pool', 'node'].includes(text(body.scope))) return invalid({ scope: '只支持 global/pool/node' })
        if (body.scope === 'node' && !findNode(text(body.scope_ref))) return notFound('节点不存在或已退役')
        if (!body.payload || typeof body.payload !== 'object') return invalid({ payload: '必须是 JSON 对象' })
        return { status: 201, body: { config_id: randomUUID(), version: 200 + Math.floor(Math.random() * 100), scope: body.scope, affected_nodes: body.scope === 'node' ? 1 : store.length } }
      })
    },
    'GET /v1/nodes/:id/metrics': (ctx) => {
      if (!ctx.requirePermission('node.read')) return
      const n = findNode(ctx.params.id)
      if (!n || !n.last_heartbeat_at || n.serving_status === 'retired') return ctx.send(200, { points: null, latest: null })
      const now = Date.now()
      const scale = n.online_users / 400 || 0.05
      const points = Array.from({ length: 144 }, (_, i) => {
        const at = now - (143 - i) * 600_000
        const wave = 0.5 + 0.5 * Math.sin(((at / 3600_000) % 24) / 3.8)
        return { at: new Date(at).toISOString(), cpu_percent: Math.round(20 + 50 * wave * scale), mem_percent: 55, load1: 0.8, rx_speed: Math.round(40e6 * wave * scale), tx_speed: Math.round(8e6 * wave * scale), tcp_conns: Math.round(900 * wave * scale) }
      })
      ctx.send(200, { points, latest: { cpu_percent: points.at(-1)!.cpu_percent, mem_used_mb: 2400, mem_total_mb: 4096, disk_used_gb: 22, disk_total_gb: 80, load1: 0.8, load5: 0.7, load15: 0.6, tcp_conns: points.at(-1)!.tcp_conns, uptime_sec: 86400 * 12, rx_total: 9e12, tx_total: 2e12 } })
    },
    'GET /v1/nodes/:id/routing': (ctx) => {
      if (!ctx.requirePermission('node.read')) return
      const n = findNode(ctx.params.id)
      if (!n) return reply(ctx, notFound('节点不存在'))
      ctx.send(200, { row_version: n.row_version, ...n.routing })
    },
    'PUT /v1/nodes/:id/routing': async (ctx) => {
      if (!ctx.requirePermission('node.config.publish')) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const n = findNode(ctx.params.id)
      if (!n) return reply(ctx, notFound('节点不存在'))
      if (body.row_version !== n.row_version) return reply(ctx, conflict(n))
      const outbounds = (Array.isArray(body.outbounds) ? body.outbounds : []) as Array<{ tag: string; type: string; settings?: unknown }>
      const routes = (Array.isArray(body.routes) ? body.routes : []) as Json[]
      const bad = validateRouting(outbounds, routes, globalRouting.outbounds.map((o) => o.tag))
      if (bad) return reply(ctx, bad)
      n.routing = { outbounds: outbounds.map((o) => ({ tag: o.tag, type: o.type, settings: o.settings ?? {} })), routes: routes.map((r, i) => ({ priority: int(r.priority) || (i + 1) * 10, matcher: r.matcher as Json, outbound_tag: text(r.outbound_tag), enabled: r.enabled === true, note: text(r.note) })) }
      touch(n)
      ctx.send(200, { ok: true, row_version: n.row_version })
    },
    'POST /v1/nodes/:id/server-token': async (ctx) => {
      if (!ctx.requirePermission('node.provision') || !ctx.requireReauth()) return
      await ctx.idempotent('node_server_token_issue', () => {
        const n = findNode(ctx.params.id)
        if (!n) return notFound('节点不存在')
        if (n.serving_status === 'retired') return err(409, 'conflict', '已退役或销毁的节点不能签发服务端令牌')
        n.serverToken = { present: true, issued_at: new Date().toISOString(), issued_by: ctx.user.userId, issued_by_name: ctx.user.displayName ?? ctx.user.email }
        return {
          status: 201,
          body: { token: `srv_${randomBytes(24).toString('hex')}`, node_type: n.node_type ?? '', panel_url: 'https://panel.pandora.run', install_command: 'curl -fsSL https://panel.pandora.run/uniproxy.sh | sudo bash -s --', hint: '旧令牌立即失效；在节点上执行命令后粘贴令牌' },
        }
      })
    },
    'GET /v1/nodes/:id/identity': (ctx) => {
      if (!ctx.requirePermission('node.read')) return
      if (!UUID.test(ctx.params.id!)) return reply(ctx, notFound('节点不存在'))
      const n = findNode(ctx.params.id)
      if (!n) return reply(ctx, notFound('节点不存在'))
      const identity = n.identity_serial
        ? {
            serial: n.identity_serial,
            status: n.identityRevoked ? 'revoked' : 'active',
            spiffe_id: `spiffe://pandora/node/${n.id}`,
            fingerprint_sha256: randomBytes(32).toString('hex'),
            issued_at: ago(86400 * 20),
            expires_at: new Date(Date.now() + 86400_000 * 70).toISOString(),
            ...(n.identityRevoked ? { revoked_at: ago(60), revoked_reason: 'admin_revoked' } : {}),
          }
        : null
      ctx.send(200, { identity, server_token: n.serverToken, bootstrap_tokens_pending: n.last_heartbeat_at ? 0 : 1 })
    },
  },
}
