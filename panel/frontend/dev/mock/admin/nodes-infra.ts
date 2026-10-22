import { createHash, randomBytes, randomUUID } from 'node:crypto'
import type { Json, MockContext, MockResult, MockRoute } from '../types.ts'
import { GROUPS, setPoolSource } from './users.ts'

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------
const err = (status: number, code: string, message: string, fields?: Record<string, string>): MockResult => ({ status, body: { error: { code, message, ...(fields ? { fields } : {}) } } })
const invalid = (fields: Record<string, string>) => err(422, 'validation_failed', '请求参数校验未通过', fields)
const notFound = () => err(404, 'not_found', '资源不存在或无权访问')
const reply = (ctx: MockContext, r: MockResult) => ctx.send(r.status, r.body)
const text = (v: unknown) => (typeof v === 'string' ? v : '')
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const ago = (sec: number) => new Date(Date.now() - sec * 1000).toISOString()
const unknownField = (body: Json, allowed: readonly string[]) => Object.keys(body).find((k) => !allowed.includes(k)) ?? null
const tooLong = (v: string, n: number) => [...v.trim()].length > n
/** 规则指向内置 direct / block：去空白、不分大小写（routing_admin.go 的 isBuiltinOutbound） */
export const isBuiltin = (v: unknown) => ['direct', 'block'].includes(text(v).trim().toLowerCase())
/** 内置出站引用的规范小写，自定义出站原样（routing_merge.go 的 canonicalRouteTag）：后端只在保存时用（routing_admin.go），下发原样用已规范的值 */
export const canonicalTag = (v: unknown) => (isBuiltin(v) ? text(v).trim().toLowerCase() : text(v))
/** 请求没带体（DELETE 空体）：httpx.DecodeJSON 对空体回 400，而 ctx.body() 把空体读成 {}，只能看头 */
export const emptyBody = (ctx: MockContext) => {
  const h = ctx.req.headers
  return h['content-length'] === '0' || (h['content-length'] === undefined && h['transfer-encoding'] === undefined)
}
const IPV4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/
const MOCK_CA_SHA256 = 'sha256:' + createHash('sha256').update('pandora-mock-node-ca').digest('hex')

/**
 * 心跳保活：60 秒内有心跳且 alive 的行每 10 秒刷新；心跳停在更早的（如种子里的新加坡机器）不会被拉回来。
 * alive 判断行此刻是否还该心跳（如已退役、身份已吊销就停）。定时器 unref，不拖住测试进程退出
 */
export function keepAlive<T extends { last_heartbeat_at: string | null }>(rows: readonly T[], alive: (row: T) => boolean = () => true): void {
  setInterval(() => rows.forEach((r) => r.last_heartbeat_at && Date.now() - new Date(r.last_heartbeat_at).getTime() < 60_000 && alive(r) && (r.last_heartbeat_at = ago(5))), 10_000).unref()
}

/** nodes.ts 的节点存储里服务器与节点池逻辑要读写的字段（结构类型，nodes.ts 的 Node 满足它） */
export interface InfraNode {
  id: string
  node_no: number
  name: string
  display_name: string | null
  deleted: boolean
  serving_status: string
  server_id: string | null
  pool_id: string | null
  node_type: string | null
  server_host: string | null
  server_port: number | null
  traffic_rate: number
  sort_order: number
  created_at: string
  updated_at: string
  routing: { outbounds: Array<{ tag: string; type: string; settings: unknown }>; routes: Json[] }
}

/** 路由组假接口要推进成员节点的行版本（route-groups.ts），nodes.ts 的 Node 满足它 */
export type RoutedNode = InfraNode & { row_version: number }

// ---------------------------------------------------------------------------
// 服务器
// ---------------------------------------------------------------------------
/** 假发布包（在线升级目标，与 Go 的 PdndTarget 对齐） */
const UPGRADE_TARGET = { version: 'v2026.10.21', serial: 1792540800, sha256: 'a'.repeat(64) }

export interface MockIdentity { seq: number; status: 'active' | 'revoked'; issued_at: string; expires_at: string; revoked_at: string | null; revoked_reason: string | null; fingerprint: string }
export interface MockToken { expires_at: string; used: boolean; voided: boolean }

export interface MockServer {
  id: string
  name: string
  status: string
  status_reason: string | null
  row_version: number
  region: string | null
  hostname: string | null
  public_ipv4: string | null
  public_ipv6: string | null
  private_ipv4: string | null
  architecture: string | null
  os_name: string | null
  agent_version: string | null
  last_heartbeat_at: string | null
  cpu_cores: number | null
  memory_mb: number | null
  disk_gb: number | null
  capacity_nodes: number
  notes: string | null
  created_at: string
  updated_at: string
  probe: { cpu_bp: number; mem_used_mb: number; mem_total_mb: number; disk_used_gb: number; disk_total_gb: number } | null
  binary_sha256: string | null
  last_upgrade: { status: string; target_version: string; reason_code: string | null; updated_at: string } | null
  /** 假后端内部：Hello 是否协商 upgrade_v1，不吐给前端 */
  upgradeV1: boolean
  /** 假后端内部：节点二进制发版序号，不吐给前端 */
  buildSerial: number
  /** 假后端内部：服务器的接入身份（照 server_identities 的最新一条），不吐给前端 */
  identity: MockIdentity | null
  /** 假后端内部：签发过的安装令牌，不吐给前端 */
  tokens: MockToken[]
}

function server(name: string, region: string, ip: string, opts: Partial<MockServer> & { cpu?: number; mem?: number; disk?: number } = {}): MockServer {
  const { cpu, mem, disk, ...rest } = opts
  const created = ago(86400 * 30)
  const defaultIdentity: MockIdentity = {
    seq: 3,
    status: 'active',
    issued_at: ago(86400 * 20),
    expires_at: new Date(Date.now() + 70 * 86400 * 1000).toISOString(),
    revoked_at: null,
    revoked_reason: null,
    fingerprint: createHash('sha256').update(name).digest('hex'),
  }
  return {
    id: randomUUID(),
    name,
    status: 'ready',
    status_reason: null,
    row_version: 3,
    region,
    hostname: `${name}.pandora.internal`,
    public_ipv4: ip,
    public_ipv6: null,
    private_ipv4: null,
    architecture: 'amd64',
    os_name: 'Debian GNU/Linux 12',
    agent_version: 'core r53',
    last_heartbeat_at: ago(6),
    cpu_cores: 4,
    memory_mb: 8192,
    disk_gb: 80,
    capacity_nodes: 8,
    notes: null,
    created_at: created,
    updated_at: created,
    probe: cpu === undefined ? null : { cpu_bp: cpu * 100, mem_used_mb: Math.round(((mem ?? 0) / 100) * 8192), mem_total_mb: 8192, disk_used_gb: Math.round(((disk ?? 0) / 100) * 80), disk_total_gb: 80 },
    binary_sha256: 'b'.repeat(64),
    last_upgrade: null,
    upgradeV1: true,
    buildSerial: 1790000000,
    identity: defaultIdentity,
    tokens: [],
    ...rest,
  }
}

export const servers: MockServer[] = []
servers.push(
  server('hk-hkg-edge-1', '香港', '192.0.2.8', {
    cpu: 58,
    mem: 64,
    disk: 31,
    binary_sha256: UPGRADE_TARGET.sha256,
    agent_version: 'v2026.10.21',
    buildSerial: UPGRADE_TARGET.serial,
  }),
  server('jp-tyo-edge-2', '东京', '192.0.2.33', { cpu: 88, mem: 52, disk: 44, upgradeV1: false }),
  // 「心跳超时仍下发」演示：这台服务器失联，其上的在役节点一起离线
  server('sg-sin-edge-1', '新加坡', '192.0.2.7', { cpu: 47, mem: 55, disk: 38, last_heartbeat_at: ago(900) }),
  server('us-lax-edge-1', '洛杉矶', '192.0.2.190', {
    cpu: 34,
    mem: 40,
    disk: 22,
    agent_version: 'core r52',
    last_upgrade: { status: 'rolled_back', target_version: 'v2026.10.21', reason_code: 'confirm_timeout', updated_at: ago(3600 * 2) },
  }),
  server('de-fra-edge-1', '法兰克福', '192.0.2.2', { status: 'maintenance', status_reason: '更换网卡', cpu: 22, mem: 30, disk: 18, notes: 'Hetzner FSN1，工单 #88213' }),
  server('tw-tpe-edge-1', '台北', '192.0.2.19', { last_heartbeat_at: ago(3600 * 5), cpu: 0, mem: 12, disk: 40 }),
  server('kr-sel-edge-1', '首尔', '', {
    status: 'draft',
    public_ipv4: null,
    hostname: null,
    architecture: null,
    os_name: null,
    agent_version: null,
    last_heartbeat_at: null,
    cpu_cores: null,
    memory_mb: null,
    disk_gb: null,
    capacity_nodes: 32,
    binary_sha256: null,
    upgradeV1: false,
    buildSerial: 0,
    identity: null,
    tokens: [{ expires_at: new Date(Date.now() + 15 * 60 * 1000).toISOString(), used: false, voided: false }],
  }),
  // R108：刚装好、还在草稿的新服务器，上面有一个接入完成待上线的节点（nodes.ts 的「大阪 01（待上线）」）
  server('jp-osa-edge-1', '大阪', '203.0.113.30', {
    status: 'draft',
    agent_version: 'core r53',
    capacity_nodes: 16,
    identity: {
      seq: 1,
      status: 'active',
      issued_at: ago(3600),
      expires_at: new Date(Date.now() + 70 * 86400 * 1000).toISOString(),
      revoked_at: null,
      revoked_reason: null,
      fingerprint: createHash('sha256').update('jp-osa-edge-1').digest('hex'),
    },
  }),
)
// 列表按 created_at 倒序：让第一台最新，顺序与设计稿一致
servers.forEach((s, i) => (s.created_at = s.updated_at = ago(86400 * (10 + i))))

/** Go NodeStaleAfter：没有心跳或距今 ≥ 90 秒算超时 */
export const heartbeatStale = (at: string | null): boolean => !at || Date.now() - new Date(at).getTime() >= 90_000
/** 按 id 找服务器（已删除的不在数组里） */
export const serverById = (id: string | null | undefined): MockServer | undefined => (id ? servers.find((s) => s.id === id) : undefined)
/** Go HasActiveIdentity：身份 active 且没过期 */
export function serverEnrolled(s: MockServer | undefined): boolean {
  if (!s?.identity || s.identity.status !== 'active') return false
  return Date.now() < new Date(s.identity.expires_at).getTime()
}
/** Go ActiveIdentitySeqs：有效身份的 seq，没有为 null */
export function activeIdentitySeq(s: MockServer | undefined): number | null {
  if (!serverEnrolled(s) || !s?.identity) return null
  return s.identity.seq
}
/** Go revokeServerIdentities：有效身份改 revoked（revoked_at=现在、revoked_reason=reason），未用且未过期的令牌全部 voided；返回是否真吊销了一张有效身份 */
export function revokeServerIdentity(s: MockServer, reason: string): boolean {
  const id = s.identity
  if (!id || id.status !== 'active' || Date.now() >= new Date(id.expires_at).getTime()) return false
  const now = new Date().toISOString()
  id.status = 'revoked'
  id.revoked_at = now
  id.revoked_reason = reason
  const nowMs = Date.now()
  for (const t of s.tokens) {
    if (!t.used && !t.voided && new Date(t.expires_at).getTime() > nowMs) t.voided = true
  }
  return true
}
/** GET v1/nodes/:id/identity 的响应体（Go NodeIdentity）：s 为 undefined（节点没绑服务器）回 { identity: null, bootstrap_tokens_pending: 0 } */
export function identityView(s: MockServer | undefined): { identity: Json | null; bootstrap_tokens_pending: number } {
  const nowMs = Date.now()
  if (!s) return { identity: null, bootstrap_tokens_pending: 0 }
  const bootstrap_tokens_pending = s.tokens.filter((t) => !t.used && !t.voided && new Date(t.expires_at).getTime() > nowMs).length
  if (!s.identity) return { identity: null, bootstrap_tokens_pending }
  const id = s.identity
  let status: string = id.status
  if (status === 'active' && nowMs >= new Date(id.expires_at).getTime()) status = 'expired'
  const out: Json = {
    serial: id.seq,
    status,
    spiffe_id: `spiffe://pandora/server/${s.id}`,
    fingerprint_sha256: id.fingerprint,
    issued_at: id.issued_at,
    expires_at: id.expires_at,
  }
  if (id.revoked_at) {
    out.revoked_at = id.revoked_at
    if (id.revoked_reason) out.revoked_reason = id.revoked_reason
  }
  return { identity: out, bootstrap_tokens_pending }
}
/** Go LatestServerMetrics：服务器最近一点指标（不限年龄）；没有探针或从没心跳为 null */
export function serverMetricsLatest(s: MockServer | undefined): { cpu_percent: number; mem_percent: number; at: string } | null {
  if (!s?.probe || !s.last_heartbeat_at) return null
  return {
    cpu_percent: s.probe.cpu_bp / 100,
    mem_percent: Math.round((s.probe.mem_used_mb / s.probe.mem_total_mb) * 1000) / 10,
    at: s.last_heartbeat_at,
  }
}
/** 服务器状态的中文名（与前端 infra.ts 的 SERVER_STATUS 一致），给 409 文案用 */
export const SERVER_STATUS_LABEL: Readonly<Record<string, string>> = { draft: '草稿', ready: '服务中', draining: '维护中', maintenance: '停机维护', retired: '已退役' }

keepAlive(servers, (s) => s.status !== 'retired' && serverEnrolled(s))

function upgradeOf(s: MockServer, online: boolean): { available: boolean; target_version: string | null; reason: string | null } {
  const target_version = UPGRADE_TARGET.version
  const st = s.last_upgrade?.status
  if (st === 'sent' || st === 'staged') return { available: false, target_version, reason: 'in_progress' }
  if (s.status !== 'ready') return { available: false, target_version, reason: 'not_ready' }
  if (!online) return { available: false, target_version, reason: 'not_connected' }
  if (!s.upgradeV1) return { available: false, target_version, reason: 'no_upgrade_support' }
  const arch = (s.architecture ?? '').trim().toLowerCase()
  if (arch !== 'amd64' && arch !== 'arm64') return { available: false, target_version, reason: 'arch_unsupported' }
  if (s.binary_sha256 === UPGRADE_TARGET.sha256) return { available: false, target_version, reason: 'up_to_date' }
  if (s.buildSerial >= UPGRADE_TARGET.serial) return { available: false, target_version, reason: 'serial_not_newer' }
  return { available: true, target_version, reason: null }
}

function serverOut(s: MockServer, nodes: readonly InfraNode[]) {
  const mine = nodes.filter((n) => n.server_id === s.id)
  const active = mine.filter((n) => n.serving_status === 'active')
  const heartbeat_online = s.status === 'ready' && !!s.last_heartbeat_at && Date.now() - new Date(s.last_heartbeat_at).getTime() < 90_000
  const { probe } = s
  // 假后端内部字段，不吐给前端
  const rest: Partial<MockServer> = { ...s }
  delete rest.probe
  delete rest.upgradeV1
  delete rest.buildSerial
  delete rest.identity
  delete rest.tokens
  const hasHeartbeat = !!s.last_heartbeat_at
  return {
    ...rest,
    heartbeat_online,
    enrolled: serverEnrolled(s),
    upgrade: upgradeOf(s, heartbeat_online),
    node_count: mine.length,
    active_node_count: active.length,
    serving_node_count: hasHeartbeat ? active.length : 0,
    never_seen_node_count: hasHeartbeat ? 0 : active.length,
    cpu_bp: probe?.cpu_bp ?? null,
    mem_used_mb: probe?.mem_used_mb ?? null,
    mem_total_mb: probe?.mem_total_mb ?? null,
    disk_used_gb: probe?.disk_used_gb ?? null,
    disk_total_gb: probe?.disk_total_gb ?? null,
    metrics_at: serverMetricsLatest(s)?.at ?? null,
  }
}

const SERVER_EDGES: Readonly<Record<string, readonly string[]>> = {
  draft: ['ready', 'maintenance', 'retired'],
  ready: ['draining', 'maintenance'],
  draining: ['ready', 'maintenance', 'retired'],
  maintenance: ['ready', 'draining', 'retired'],
  retired: [],
}

const SERVER_TEXT: ReadonlyArray<readonly [string, number]> = [
  ['region', 64],
  ['hostname', 253],
  ['architecture', 32],
  ['os_name', 120],
  ['notes', 2000],
]
/** validateServerTextLimits + validateServerFields（name / IP / capacity） */
function serverFieldErrors(body: Json, creating: boolean): Record<string, string> {
  const f: Record<string, string> = {}
  for (const [k, n] of SERVER_TEXT) if (typeof body[k] === 'string' && tooLong(text(body[k]), n)) f[k] = `内容过长，最多允许 ${n} 个字符`
  if (Object.keys(f).length) return f
  // 编辑时传了 name 就必须非空（ValidatePatchServerInput），省略才是不改
  if (creating || body.name !== undefined) {
    const name = text(body.name).trim()
    if (!name || [...name].length > 120) f.name = '名称必须为 1 到 120 个字符'
  }
  for (const k of ['public_ipv4', 'private_ipv4']) if (text(body[k]).trim() && !IPV4.test(text(body[k]).trim())) f[k] = 'IP 地址格式不正确'
  const v6 = text(body.public_ipv6).trim()
  if (v6 && (!v6.includes(':') || IPV4.test(v6))) f.public_ipv6 = 'IP 地址格式不正确'
  if (body.capacity_nodes !== undefined && !(Number.isInteger(body.capacity_nodes) && (body.capacity_nodes as number) > 0)) f.capacity_nodes = '必须大于 0'
  return f
}
const SERVER_FIELDS = ['name', 'region', 'hostname', 'public_ipv4', 'public_ipv6', 'private_ipv4', 'architecture', 'os_name', 'capacity_nodes', 'notes']
const serverConflict = (s: MockServer) => err(409, 'conflict', '服务器已被其他管理员修改，请刷新后重试', { row_version: `current=${s.row_version}` })
const findServer = (id: string | undefined) => (id && UUID.test(id) ? servers.find((s) => s.id === id) : undefined)
const touchServer = (s: MockServer) => {
  s.row_version += 1
  s.updated_at = new Date().toISOString()
}

// ---------------------------------------------------------------------------
// 节点池：plans 是绑定的套餐版本（假数据写死），pendingTokens 模拟只有后端知道的删除阻碍
// ---------------------------------------------------------------------------
interface MockPool {
  id: string
  code: string
  name: string
  region: string | null
  status: string
  plans: string[]
  pendingTokens: number
  /** R104「仅限用户组」名单（用户组 id，排好序）；空 = 不限定 */
  groups: string[]
}
const pool = (code: string, name: string, region: string | null, plans: string[], pendingTokens = 0, groups: string[] = []): MockPool => ({ id: randomUUID(), code, name, region, status: 'active', plans, pendingTokens, groups })
// 种子：企业专线只给「企业客户」，灰度池只给「VIP」（用户组 id 取 users.ts 的 GROUPS）
export const pools: MockPool[] = [
  pool('asia', '亚太精选', 'APAC', ['专业版', '团队版']),
  pool('global', '全部线路', null, ['标准版', '专业版', '家庭版']),
  pool('beta', '灰度池', null, [], 0, [GROUPS[0]!.id]),
  pool('enterprise', '企业专线', 'CN', [], 1, [GROUPS[1]!.id]),
  // 首次搭建：新建的池还没绑套餐，上线大阪 02 会带「所在节点池没有绑定任何套餐」（R113）
  pool('kansai', '关西新线路', 'JP', []),
]

// ---------------------------------------------------------------------------
// R104 名单：带了 allowed_user_group_ids 就要 reauth（字段级，改名改状态照旧不用）；
// 校验格式、重复、上限 100、存在性（跨租户同样按不存在回）；同一份名单重复提交不算变化
// ---------------------------------------------------------------------------
const groupRef = (id: string) => ({ id, name: GROUPS.find((g) => g.id === id)?.name ?? id })
setPoolSource((groupId) => pools.filter((p) => p.groups.includes(groupId)).map((p) => ({ id: p.id, name: p.name })))

/** 返回规范化名单（排序去重后），或 422；不存在的组与格式问题同一个字段键 */
function poolGroupIds(raw: unknown): string[] | MockResult {
  const bad = (msg: string) => invalid({ allowed_user_group_ids: msg })
  if (!Array.isArray(raw)) return bad('必须是无重复的用户组 UUID 列表')
  if (raw.length > 100) return bad('一个节点池最多限定 100 个用户组')
  if (raw.some((x) => typeof x !== 'string' || !UUID.test(x))) return bad('必须是无重复的用户组 UUID 列表')
  const ids = (raw as string[]).map((x) => x.toLowerCase()).sort()
  if (new Set(ids).size !== ids.length) return bad('必须是无重复的用户组 UUID 列表')
  if (ids.some((id) => !GROUPS.some((g) => g.id === id))) return bad('包含不存在的用户组')
  return ids
}

function slugify(name: string): string {
  const s = name
    .toLowerCase()
    .replace(/[ _-]/g, '-')
    .replace(/[^a-z0-9-]/g, '')
    .replace(/^-+|-+$/g, '')
  return s || `pool-${randomBytes(4).toString('hex')}`
}

// ---------------------------------------------------------------------------
// 全局路由与共用的路由校验
// ---------------------------------------------------------------------------
export const globalRouting: { outbounds: Array<{ tag: string; type: string; settings: unknown }>; routes: Json[] } = {
  outbounds: [
    { tag: 'US-LAX-01', type: 'trojan', settings: { server: 'us1.example.test', server_port: 443 } },
    { tag: 'HK-RELAY', type: 'shadowsocks', settings: { server: 'hk-relay.example.test', server_port: 8388 } },
  ],
  routes: [
    { priority: 10, matcher: { domain_suffix: ['doubleclick.net', 'googlesyndication.com'] }, outbound_tag: 'block', enabled: true, note: '广告' },
    { priority: 20, matcher: { domain_suffix: ['cn', 'baidu.com', 'qq.com'] }, outbound_tag: 'direct', enabled: true, note: '国内直连' },
    { priority: 30, matcher: { ip_cidr: ['10.0.0.0/8', '172.16.0.0/12', '192.168.0.0/16'] }, outbound_tag: 'direct', enabled: true, note: '私有地址' },
    { priority: 40, matcher: { domain_suffix: ['netflix.com', 'nflxvideo.net'] }, outbound_tag: 'US-LAX-01', enabled: true, note: '流媒体' },
    { priority: 50, matcher: { port: [25, 465, 587] }, outbound_tag: 'block', enabled: true, note: '禁止发信' },
    { priority: 60, matcher: {}, outbound_tag: 'HK-RELAY', enabled: true, note: '兜底' },
  ],
}

const OUTBOUND_TYPES = ['direct', 'block', 'socks', 'http', 'shadowsocks', 'vmess', 'vless', 'trojan', 'hysteria', 'hysteria2', 'tuic', 'anytls', 'shadowtls', 'wireguard']
const MATCHER_KEYS = ['domain', 'domains', 'domain_suffix', 'domain_suffixes', 'ip', 'ip_cidr', 'ip_cidrs', 'port', 'ports', 'network', 'networks', 'source', 'source_ip_cidr', 'source_cidrs', 'source_port', 'source_ports']

/**
 * validateRoutingPayload + 出站存在性：先校验出站（tag / type、重名、内置名），再校验每条规则的匹配器
 * 与兜底位置，最后看规则指向的出站是否存在（extraTags 为可额外引用的 tag，单节点传全局出站）。
 * 返回 422 或 null
 */
export function validateRouting(outbounds: ReadonlyArray<{ tag?: unknown; type?: unknown }>, routes: readonly Json[], extraTags: readonly string[] = []): MockResult | null {
  const tags = new Set(['direct', 'block'])
  for (const o of outbounds) {
    const tag = text(o.tag).trim()
    const type = text(o.type).trim().toLowerCase()
    if (!tag || !type) return invalid({ outbounds: '每条出站都要有 tag 和 type' })
    if (tags.has(tag.toLowerCase())) return invalid({ outbounds: `出站 tag 重复或占用内置名称：${tag}` })
    if (!OUTBOUND_TYPES.includes(type) || [...tag].length > 64) return invalid({ outbounds: `出站 ${tag} 的类型或标签长度不合法` })
    tags.add(tag.toLowerCase())
  }
  const lastEnabled = routes.map((r, i) => (r.enabled ? i : -1)).filter((i) => i >= 0).at(-1)
  for (const [i, r] of routes.entries()) {
    const m = (r.matcher ?? {}) as Json
    if (typeof m !== 'object' || Array.isArray(m) || Object.keys(m).some((k) => !MATCHER_KEYS.includes(k))) return invalid({ routes: `第 ${i + 1} 条规则无法跨内核下发：不支持的匹配类型` })
    if (Object.keys(m).length === 0 && r.enabled && i !== lastEnabled) return invalid({ routes: `第 ${i + 1} 条空匹配兜底规则必须放在最后` })
  }
  // 引用按 tag 原样精确比较（与 Go 的 checkRouteRefs、下发和 pdnd 查表一致），内置 direct / block 不分大小写
  const exact = new Set([...outbounds.map((o) => text(o.tag).trim()), ...extraTags])
  for (const [i, r] of routes.entries()) if (!isBuiltin(r.outbound_tag) && !exact.has(text(r.outbound_tag))) return invalid({ routes: `第 ${i + 1} 条规则指向不存在的出站 "${text(r.outbound_tag)}"` })
  return null
}

/**
 * 全租户悬空引用（组与节点规则指向看不见的出站）。route-groups.ts 经 setDanglingSource 登记带组的口径；
 * 没登记时只看节点私有规则对全局与私有出站，与 00096 之前一致。不 import route-groups.ts，避免循环依赖
 */
let danglingOf: (nodes: readonly InfraNode[]) => Set<string> = (nodes) => {
  const out = new Set<string>()
  const global = new Set(globalRouting.outbounds.map((o) => o.tag))
  for (const n of nodes) {
    if (n.deleted) continue
    for (const r of n.routing.routes) {
      const tag = text(r.outbound_tag)
      if (!isBuiltin(tag) && !global.has(tag) && !n.routing.outbounds.some((o) => o.tag === tag)) out.add(`节点 ${n.display_name ?? n.name} → ${tag}`)
    }
  }
  return out
}
export const setDanglingSource = (fn: () => Set<string>) => {
  danglingOf = () => fn()
}

/** 规范 JSON 的 sha256（存储顺序，空集也有值） */
const revisionOf = () => createHash('sha256').update(JSON.stringify({ outbounds: globalRouting.outbounds, routes: globalRouting.routes })).digest('hex')

// ---------------------------------------------------------------------------
// 路由表
// ---------------------------------------------------------------------------
// 节点存储由 nodes.ts 经 infraRoutes 交进来；按池统计的在线节点数给套餐假后端用，
// 与 GET v1/node-pools 的 active_nodes 同一口径（生命周期 active 且已定协议）
let nodeStore: readonly InfraNode[] = []
export const activeNodesInPool = (poolId: string): number => nodeStore.filter((n) => n.pool_id === poolId && n.serving_status === 'active' && n.node_type).length

export function infraRoutes(nodes: InfraNode[]): Record<string, MockRoute> {
  nodeStore = nodes
  const live = () => nodes.filter((n) => !n.deleted)
  return {
    // ---- 全局路由（静态段 routing 先于 /nodes/:id 系列）----
    'GET /v1/nodes/routing': (ctx) => {
      const online = live().filter((n) => n.serving_status === 'active' && !heartbeatStale(serverById(n.server_id)?.last_heartbeat_at ?? null)).length
      ctx.send(200, { revision: revisionOf(), outbounds: globalRouting.outbounds, routes: globalRouting.routes, online_nodes: online })
    },
    'PUT /v1/nodes/routing': async (ctx) => {
      if (!ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('node_routing_global_publish', () => {
        const extra = unknownField(body, ['expected_revision', 'outbounds', 'routes'])
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        const outbounds = (Array.isArray(body.outbounds) ? body.outbounds : []) as Array<{ tag: string; type: string; settings?: unknown }>
        const routes = (Array.isArray(body.routes) ? body.routes : []) as Json[]
        const bad = validateRouting(outbounds, routes)
        if (bad) return bad
        const current = revisionOf()
        if (body.expected_revision !== current) return err(409, 'conflict', '全局路由已被其他管理员修改，请刷新后重试', { expected_revision: `current=${current}` })
        // 写之前与之后各取一次悬空引用，只拒这次新造成的（routing_refs.go），组规则与节点规则都算
        const before = danglingOf(nodes)
        const prev = { outbounds: globalRouting.outbounds, routes: globalRouting.routes }
        globalRouting.outbounds = outbounds.map((o) => ({ tag: o.tag.trim(), type: o.type.trim().toLowerCase(), settings: o.settings ?? {} }))
        globalRouting.routes = routes.map((r, i) => ({ priority: Number(r.priority) || (i + 1) * 10, matcher: (r.matcher as Json) ?? {}, outbound_tag: canonicalTag(r.outbound_tag), enabled: r.enabled === true, note: text(r.note) }))
        const fresh = [...danglingOf(nodes)].filter((d) => !before.has(d)).sort()
        if (fresh.length) {
          Object.assign(globalRouting, prev)
          return err(409, 'conflict', `要删除的全局出站仍被规则引用：${fresh.join('、')}`)
        }
        const affected = live().filter((n) => n.serving_status !== 'retired').length
        return { status: 200, body: { ok: true, revision: revisionOf(), affected_nodes: affected } }
      })
    },

    // ---- 服务器 ----
    'GET /v1/servers': (ctx) => {
      const status = (ctx.query.get('status') ?? '').trim()
      const q = (ctx.query.get('q') ?? '').trim().toLowerCase()
      if (status && !(status in SERVER_EDGES)) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { status: '不支持的服务器状态' })
      if ([...q].length > 120) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { q: '搜索词不能超过 120 个字符' })
      const rows = servers
        .filter((s) => (!status || s.status === status) && (!q || s.name.toLowerCase().includes(q) || (s.hostname ?? '').toLowerCase().includes(q)))
        .sort((a, b) => b.created_at.localeCompare(a.created_at))
        .map((s) => serverOut(s, nodes))
      ctx.send(200, { servers: rows, total: rows.length })
    },
    'POST /v1/servers': async (ctx) => {
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const extra = unknownField(body, SERVER_FIELDS)
      if (extra) return ctx.fail(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
      if (body.capacity_nodes === undefined || body.capacity_nodes === 0) body.capacity_nodes = 32
      const fields = serverFieldErrors(body, true)
      if (Object.keys(fields).length) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', fields)
      const name = text(body.name).trim()
      if (servers.some((s) => s.name === name)) return ctx.fail(409, 'conflict', '服务器名称已存在')
      const opt = (k: string) => text(body[k]).trim() || null
      const s = server(name, '', '', {
        status: 'draft',
        region: opt('region'),
        hostname: opt('hostname'),
        public_ipv4: opt('public_ipv4'),
        public_ipv6: opt('public_ipv6'),
        private_ipv4: opt('private_ipv4'),
        architecture: opt('architecture'),
        os_name: opt('os_name'),
        notes: opt('notes'),
        capacity_nodes: body.capacity_nodes as number,
        agent_version: null,
        last_heartbeat_at: null,
        cpu_cores: null,
        memory_mb: null,
        disk_gb: null,
        row_version: 1,
        binary_sha256: null,
        upgradeV1: false,
        buildSerial: 0,
        identity: null,
        tokens: [],
      })
      s.created_at = s.updated_at = new Date().toISOString()
      servers.push(s)
      ctx.send(201, serverOut(s, nodes))
    },
    'POST /v1/servers/upgrade': async (ctx) => {
      if (!ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('server_upgrade', async () => {
        const extra = unknownField(body, ['server_ids'])
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        // Go 侧 server_ids 缺省为空切片（走 1..50 校验）；类型不符在解码时就是 400
        const raw = body.server_ids ?? []
        if (!Array.isArray(raw) || !raw.every((id) => typeof id === 'string')) return err(400, 'bad_request', '请求体不是合法的 JSON')
        const ids = raw as string[]
        if (!ids.every((id) => UUID.test(id))) return invalid({ server_ids: '服务器 ID 格式不正确' })
        const seen = new Set<string>()
        const unique: string[] = []
        for (const id of ids) {
          if (!seen.has(id)) {
            seen.add(id)
            unique.push(id)
          }
        }
        if (unique.length < 1 || unique.length > 50) return invalid({ server_ids: '一次选 1 到 50 台服务器' })
        const results: Array<{ server_id: string; ok: boolean; reason: string | null }> = []
        for (const id of unique) {
          const s = findServer(id)
          if (!s) {
            results.push({ server_id: id, ok: false, reason: 'not_found' })
            continue
          }
          const heartbeat_online = s.status === 'ready' && !!s.last_heartbeat_at && Date.now() - new Date(s.last_heartbeat_at).getTime() < 90_000
          const block = upgradeOf(s, heartbeat_online).reason
          if (block) {
            results.push({ server_id: id, ok: false, reason: block })
            continue
          }
          const now = new Date().toISOString()
          s.last_upgrade = { status: 'sent', target_version: UPGRADE_TARGET.version, reason_code: null, updated_at: now }
          results.push({ server_id: id, ok: true, reason: null })
          setTimeout(() => {
            if (s.last_upgrade?.status === 'sent') {
              s.last_upgrade = { ...s.last_upgrade, status: 'staged', updated_at: new Date().toISOString() }
            }
          }, 3000).unref()
          setTimeout(() => {
            const lu = s.last_upgrade
            if (lu && (lu.status === 'staged' || lu.status === 'sent')) {
              s.last_upgrade = { ...lu, status: 'succeeded', updated_at: new Date().toISOString() }
              s.binary_sha256 = UPGRADE_TARGET.sha256
              s.agent_version = UPGRADE_TARGET.version
              s.buildSerial = UPGRADE_TARGET.serial
            }
          }, 8000).unref()
        }
        return { status: 200, body: { target_version: UPGRADE_TARGET.version, results } }
      })
    },
    'GET /v1/servers/:id': (ctx) => {
      const s = findServer(ctx.params.id)
      if (!s) return reply(ctx, notFound())
      ctx.send(200, serverOut(s, nodes))
    },
    'PATCH /v1/servers/:id': async (ctx) => {
      const s = findServer(ctx.params.id)
      if (!s) return reply(ctx, notFound())
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const extra = unknownField(body, ['row_version', ...SERVER_FIELDS])
      if (extra) return ctx.fail(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
      if (!(Number.isInteger(body.row_version) && (body.row_version as number) > 0)) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { row_version: '必须提供正整数版本号' })
      const fields = serverFieldErrors(body, false)
      if (Object.keys(fields).length) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', fields)
      if (body.row_version !== s.row_version) return reply(ctx, serverConflict(s))
      const used = nodes.filter((n) => n.server_id === s.id && n.serving_status !== 'retired').length
      if (body.capacity_nodes !== undefined && (body.capacity_nodes as number) < used) return ctx.fail(409, 'conflict', '服务器容量不能低于当前节点占用', { capacity_nodes: `minimum=${used}` })
      const name = text(body.name).trim()
      if (name && servers.some((x) => x !== s && x.name === name)) return ctx.fail(409, 'conflict', '服务器名称已存在')
      if (name) s.name = name
      for (const k of ['region', 'hostname', 'public_ipv4', 'public_ipv6', 'private_ipv4', 'architecture', 'os_name', 'notes'] as const) if (typeof body[k] === 'string') s[k] = text(body[k]).trim() || null
      if (body.capacity_nodes !== undefined) s.capacity_nodes = body.capacity_nodes as number
      touchServer(s)
      ctx.send(200, serverOut(s, nodes))
    },
    'POST /v1/servers/:id/status': async (ctx) => {
      const s = findServer(ctx.params.id)
      if (!s) return reply(ctx, notFound())
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const extra = unknownField(body, ['status', 'reason', 'row_version'])
      if (extra) return ctx.fail(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
      if (tooLong(text(body.reason), 500)) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { reason: '内容过长，最多允许 500 个字符' })
      const to = text(body.status)
      const f: Record<string, string> = {}
      if (!(Number.isInteger(body.row_version) && (body.row_version as number) > 0)) f.row_version = '必须提供正整数版本号'
      if (!(to in SERVER_EDGES)) f.status = '不支持的服务器状态'
      if (Object.keys(f).length) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', f)
      if (body.row_version !== s.row_version) return reply(ctx, serverConflict(s))
      if (!SERVER_EDGES[s.status]!.includes(to)) return ctx.fail(409, 'conflict', '不允许的服务器状态转换', { status: `${s.status} -> ${to}` })
      if (to === 'ready' && !nodes.some((n) => n.server_id === s.id && n.serving_status === 'active' && !n.deleted)) {
        return ctx.fail(409, 'conflict', '服务器还没有在役节点，不能投入服务：先新建节点或把节点绑到这台，再在节点上用「上线」（上线时这台会自动投入服务）', { nodes: 'requires_valid_active_node' })
      }
      s.status = to
      s.status_reason = text(body.reason).trim() || null
      if (to === 'retired') revokeServerIdentity(s, '服务器已退役')
      touchServer(s)
      ctx.send(200, serverOut(s, nodes))
    },
    'DELETE /v1/servers/:id': async (ctx) => {
      if (!ctx.requireReauth()) return
      const s = findServer(ctx.params.id)
      if (!s) return reply(ctx, notFound())
      const body = await ctx.body()
      if (!body || emptyBody(ctx)) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const extra = unknownField(body, ['row_version'])
      if (extra) return ctx.fail(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
      if (!(Number.isInteger(body.row_version) && (body.row_version as number) > 0)) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { row_version: '必须提供正整数版本号' })
      if (body.row_version !== s.row_version) return reply(ctx, serverConflict(s))
      if (s.status !== 'draft' && s.status !== 'retired') return ctx.fail(409, 'conflict', '服务器仍在服务，请先在「状态」里退役', { status: s.status })
      // 同 Go DetachServerNodes：在役的退役并吊销身份；草稿与已停用的只解绑、留作配置；全部摘掉 server_id
      for (const n of nodes.filter((x) => x.server_id === s.id)) {
        if (n.serving_status === 'active') n.serving_status = 'retired'
        n.server_id = null
      }
      revokeServerIdentity(s, '服务器已删除')
      servers.splice(servers.indexOf(s), 1)
      ctx.send(200, { ok: true, id: s.id })
    },
    'POST /v1/servers/:id/bootstrap-token': async (ctx) => {
      if (!ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('server_bootstrap_token', () => {
        const extra = unknownField(body, ['ttl_minutes'])
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        const s = findServer(ctx.params.id)
        if (!s) return notFound()
        if (s.status === 'retired') return err(409, 'conflict', '服务器已删除或已退役，不能签发安装令牌')
        let minutes = 20
        if (body.ttl_minutes !== undefined && body.ttl_minutes !== 0) {
          const ttl = Number(body.ttl_minutes)
          if (!(Number.isInteger(ttl) && ttl >= 1 && ttl <= 60)) return invalid({ ttl_minutes: '有效期须在 1–60 分钟' })
          minutes = ttl
        }
        const token = randomBytes(32).toString('base64url')
        const expires_at = new Date(Date.now() + minutes * 60_000).toISOString()
        const mockToken: MockToken = { expires_at, used: false, voided: false }
        s.tokens.push(mockToken)
        const install_command = `curl --proto '=https' -fsSL 'https://panel.example.test/pdnd/install.sh' | sudo bash -s -- --panel 'panel.example.test:9443' --ca-sha256 '${MOCK_CA_SHA256}' --token '${token}'`
        // 模拟管理员在机器上执行了安装命令（假后端特有）：真后端由节点端拿令牌来接入
        setTimeout(() => {
          if (s.status === 'retired') return
          if (mockToken.used || mockToken.voided || Date.now() >= new Date(mockToken.expires_at).getTime()) return
          mockToken.used = true
          const now = new Date().toISOString()
          const seq = (s.identity?.seq ?? 0) + 1
          s.identity = {
            seq,
            status: 'active',
            issued_at: now,
            expires_at: new Date(Date.now() + 90 * 86400 * 1000).toISOString(),
            revoked_at: null,
            revoked_reason: null,
            fingerprint: createHash('sha256').update(s.id + seq).digest('hex'),
          }
          s.hostname ??= `${s.name}.pandora.internal`
          s.architecture ??= 'amd64'
          s.os_name ??= 'Debian GNU/Linux 12'
          s.agent_version ??= 'core r53'
          s.cpu_cores ??= 4
          s.memory_mb ??= 8192
          s.disk_gb ??= 80
          s.probe ??= { cpu_bp: 1200, mem_used_mb: 900, mem_total_mb: 8192, disk_used_gb: 6, disk_total_gb: 80 }
          // 真后端按 gRPC 对端地址回填地区与公网 IP（只填空的）
          s.region ??= '新加坡'
          if (!s.public_ipv4 && !s.public_ipv6) s.public_ipv4 = `203.0.113.${(seq % 200) + 10}`
          s.last_heartbeat_at = now
          touchServer(s)
        }, 15_000).unref()
        return {
          status: 201,
          unreplayable: true,
          body: { token, expires_at, install_command },
        }
      })
    },

    // ---- 节点池 ----
    'GET /v1/node-pools': (ctx) => {
      const rows = [...pools]
        .sort((a, b) => a.name.localeCompare(b.name, 'zh'))
        .map((p) => {
          const mine = nodes.filter((n) => n.pool_id === p.id)
          const members = mine
            .filter((n) => !n.deleted)
            .sort((a, b) => a.sort_order - b.sort_order || a.node_no - b.node_no)
            .map((n) => ({ id: n.id, name: n.name, node_no: n.node_no }))
          return {
            id: p.id,
            code: p.code,
            name: p.name,
            region: p.region ?? '',
            status: p.status,
            nodes: mine.length,
            active_nodes: activeNodesInPool(p.id),
            plans: p.plans.length,
            members,
            plan_names: [...new Set(p.plans)].sort(),
            allowed_user_groups: p.groups.map(groupRef),
          }
        })
      ctx.send(200, { pools: rows })
    },
    'POST /v1/node-pools': async (ctx) => {
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const extra = unknownField(body, ['code', 'name', 'region', 'status', 'allowed_user_group_ids'])
      if (extra) return ctx.fail(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
      // 字段级 reauth 在一切校验与写入之前（被拒的请求什么都不建）
      const hasGroups = body.allowed_user_group_ids !== undefined && body.allowed_user_group_ids !== null
      if (hasGroups && !ctx.requireReauth()) return
      const name = text(body.name).trim()
      if (!name) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { name: '分组名必填' })
      const groups = hasGroups ? poolGroupIds(body.allowed_user_group_ids) : []
      if (!Array.isArray(groups)) return reply(ctx, groups)
      const code = text(body.code).trim() || slugify(name)
      if (pools.some((p) => p.code === code)) return ctx.fail(409, 'conflict', '这个分组标识已存在')
      const p = pool(code, name, text(body.region) || null, [], 0, groups)
      pools.push(p)
      ctx.send(200, { id: p.id })
    },
    'POST /v1/node-pools/:id': async (ctx) => {
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const extra = unknownField(body, ['code', 'name', 'region', 'status', 'allowed_user_group_ids'])
      if (extra) return ctx.fail(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
      // 省略或 null = 不改名单，[] = 取消限定；带了就要 reauth
      const hasGroups = body.allowed_user_group_ids !== undefined && body.allowed_user_group_ids !== null
      if (hasGroups && !ctx.requireReauth()) return
      const status = text(body.status)
      if (status && !['active', 'draining', 'disabled'].includes(status)) return ctx.fail(422, 'validation_failed', '状态只能是 active / draining / disabled')
      const p = pools.find((x) => x.id === ctx.params.id)
      if (!p) return reply(ctx, notFound())
      const groups = hasGroups ? poolGroupIds(body.allowed_user_group_ids) : null
      if (groups && !Array.isArray(groups)) return reply(ctx, groups)
      if (groups) p.groups = groups
      if (text(body.name).trim()) p.name = text(body.name).trim()
      if (text(body.region)) p.region = text(body.region)
      if (status) p.status = status
      ctx.send(200, { ok: true })
    },
    'DELETE /v1/node-pools/:id': (ctx) => {
      const p = pools.find((x) => x.id === ctx.params.id)
      if (!p) return reply(ctx, notFound())
      if (nodes.some((n) => n.pool_id === p.id)) return ctx.fail(409, 'conflict', '这个分组下还有节点，先把节点移到别的分组再删')
      if (p.plans.length) return ctx.fail(409, 'conflict', '还有套餐绑定着这个分组，先解除绑定再删')
      if (p.pendingTokens) return ctx.fail(409, 'conflict', '还有未使用的引导令牌绑定这个分组，请等待令牌过期后再删除')
      pools.splice(pools.indexOf(p), 1)
      ctx.send(200, { ok: true })
    },
  }
}
