import { randomBytes, randomUUID } from 'node:crypto'
import type { Json, MockContext, MockModule, MockResult } from '../types.ts'
import {
  activeIdentitySeq,
  canonicalTag,
  emptyBody,
  heartbeatStale,
  identityView,
  infraRoutes,
  pools,
  revokeServerIdentity,
  serverById,
  serverEnrolled,
  serverMetricsLatest,
  SERVER_STATUS_LABEL,
  servers,
  validateRouting,
} from './nodes-infra.ts'
import { NODE_PROTOCOL_SCHEMAS } from './node-schemas.ts'
import { copyMemberships, groupRefsOfNode, routeGroupRoutes, visibleTagsForNode } from './route-groups.ts'
import {
  applyXB,
  GI,
  TI,
  type XBHost,
  type XBState,
  XB_BODY_KEYS,
  XB_EXCEEDED_NOTE,
  xbAdmin,
  xbDefaults,
  xbErrors,
  xbRoutes,
  xbRow,
} from './nodes-xb.ts'

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
  deleted: boolean
  serving_status: string
  server_id: string | null
  pool_id: string | null
  node_type: string | null
  server_host: string | null
  server_port: number | null
  traffic_rate: number
  display_name: string | null
  country_code: string | null
  protocol_config: Json
  protocol_schema_version: number
  sort_order: number
  online_users: number
  online_ips: number
  traffic_bytes_24h: number
  created_at: string
  updated_at: string
  routing: { outbounds: Array<{ tag: string; type: string; settings: unknown }>; routes: Array<Json> }
  xb: XBState
  /** 入站证书心跳；null = 未报过 */
  cert: CertSnapshot | null
}

/** 节点心跳就是所在服务器的心跳，同 Go row.ServerLastHeartbeatAt */
const hbOf = (n: Node) => serverById(n.server_id)?.last_heartbeat_at ?? null

interface CertSnapshot {
  mode: 'acme' | 'file'
  identifier: string | null
  identifier_kind: 'domain' | 'ip' | null
  not_after: string | null
  error_code: string | null
  error_detail: string | null
  next_attempt_at: string | null
  reported_at: string
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
    deleted: false,
    server_id: serverAt(server).id,
    pool_id: pools[0]!.id,
    server_host: `n${nodeNo}.example.test`,
    server_port: 443,
    traffic_rate: 1,
    display_name: null,
    country_code: null,
    protocol_config: {},
    protocol_schema_version: 1,
    sort_order: (nodeNo - 100) * 10,
    online_users: 0,
    online_ips: 0,
    traffic_bytes_24h: 0,
    created_at: created,
    updated_at: created,
    routing: { outbounds: [], routes: [] },
    xb: xbDefaults(),
    cert: null,
    ...p,
  }
}
const fromNow = (ms: number) => new Date(Date.now() + ms).toISOString()

const store: Node[] = [
  node(
    {
      name: '香港 01 · 原生',
      node_type: 'vless',
      serving_status: 'active',
      country_code: 'HK',
      online_users: 412,
      online_ips: 530,
      traffic_bytes_24h: Math.round(1.8 * 1024 * GB),
      protocol_config: { network: 'tcp', tls: 2, flow: 'xtls-rprx-vision', reality_settings: { dest: 'www.microsoft.com:443', server_name: 'www.microsoft.com', public_key: 'Hx3mQe9Z2y5rTq8WkVbN1cXyL0pOaSdFgHjK7uIoP4A', short_id: '6ba85179' } },
      xb: { ...xbDefaults(), tags: ['香港', '原生'], limit_bytes: 2 * TI, used_bytes: Math.round(1.2 * TI), reset_day: 1 },
    },
    0,
  ),
  node(
    {
      name: '香港 02 · IPLC',
      node_type: 'hysteria2',
      serving_status: 'active',
      country_code: 'HK',
      server_port: 8443,
      online_users: 288,
      online_ips: 301,
      traffic_bytes_24h: Math.round(1.12 * 1024 * GB),
      protocol_config: { cert_path: '/etc/pandora/tls.crt', key_path: '/etc/pandora/tls.key', bandwidth: { up: 200, down: 1000 } },
      cert: {
        mode: 'file',
        identifier: null,
        identifier_kind: null,
        not_after: fromNow(10 * 86400000),
        error_code: null,
        error_detail: null,
        next_attempt_at: null,
        reported_at: ago(300),
      },
      xb: {
        ...xbDefaults(),
        tags: ['香港', 'IPLC'],
        schedule: [{ start: '00:00', end: '08:00', rate: 0.5 }],
        relays: [
          {
            id: 'a1000001-0000-4000-8000-000000000001',
            name: '香港 02 · 中转 A',
            server_host: 'relay-hk-a.example.test',
            server_port: 443,
            sni: null,
            host_header: null,
            enabled: true,
            sort_order: 0,
            row_version: 1,
            created_at: ago(86400 * 14),
            updated_at: ago(86400 * 14),
          },
          {
            id: 'a1000001-0000-4000-8000-000000000002',
            name: '香港 02 · 中转 B',
            server_host: 'relay-hk-b.example.test',
            server_port: 8443,
            sni: 'n102.example.test',
            host_header: null,
            enabled: false,
            sort_order: 10,
            row_version: 1,
            created_at: ago(86400 * 13),
            updated_at: ago(86400 * 13),
          },
        ],
      },
    },
    0,
  ),
  node(
    {
      name: '东京 03',
      node_type: 'trojan',
      serving_status: 'active',
      country_code: 'JP',
      online_users: 356,
      online_ips: 402,
      traffic_bytes_24h: Math.round(1.49 * 1024 * GB),
      protocol_config: {
        network: 'tcp',
        tls: 2,
        reality_settings: {
          dest: 'www.apple.com:443',
          server_name: 'www.apple.com',
          // 读接口会抹掉私钥，假数据只要有值即可；别放像真密钥的串（gitleaks 会拦）
          private_key: 'mock-reality-private-key',
          public_key: 'Qw2Er4Ty6Ui8Op0As1Df3Gh5Jk7Lz9Xc2Vb4Nm6Qp8R',
          short_id: '3f9a7c21',
        },
      },
    },
    1,
  ),
  // 「心跳超时仍下发」演示——所在服务器 15 分钟没心跳，列表显示离线、仍在下发
  node(
    {
      name: '新加坡 02',
      node_type: 'shadowsocks',
      serving_status: 'active',
      country_code: 'SG',
      server_port: 8388,
      online_users: 0,
      traffic_bytes_24h: 864 * GB,
      protocol_config: { cipher: 'aes-256-gcm' },
    },
    2,
  ),
  node({ name: '洛杉矶 01', node_type: 'vless', serving_status: 'active', country_code: 'US', pool_id: pools[1]!.id, online_users: 96, online_ips: 110, traffic_bytes_24h: 540 * GB, protocol_config: { network: 'ws', tls: 0, ws_path: '/ws' }, xb: { ...xbDefaults(), tags: ['流媒体'], limit_bytes: 500 * GI, used_bytes: 540 * GI, reset_day: 20, exceeded_at: ago(86400) } }, 3),
  node(
    {
      name: '首尔 01 · 灰度',
      node_type: 'tuic',
      serving_status: 'disabled',
      country_code: 'KR',
      pool_id: pools[2]!.id,
      protocol_config: { cert_mode: 'acme' },
      // IP 证书申请失败：80 端口没放行
      cert: {
        mode: 'acme',
        identifier: '192.0.2.10',
        identifier_kind: 'ip',
        not_after: null,
        error_code: 'validation_failed',
        error_detail: 'Connection refused during HTTP-01 challenge',
        next_attempt_at: fromNow(15 * 60 * 1000),
        reported_at: ago(30),
      },
    },
    1,
  ),
  node({ name: '新加坡 03（草稿）', node_type: 'anytls', serving_status: 'draft', country_code: 'SG', protocol_config: {} }, 2),
  node({ name: '台北 01', node_type: 'vmess', serving_status: 'retired', country_code: 'TW', protocol_config: { network: 'ws', tls: 0 } }, 5),
  // R105：在役但没划进节点池，不服务任何用户
  node({ name: '香港 03（未入池）', node_type: 'shadowsocks', serving_status: 'active', country_code: 'HK', server_port: 8389, pool_id: null, protocol_config: { cipher: 'aes-256-gcm' } }, 0),
  // R108：新服务器（大阪，草稿）上刚接入完的草稿节点，等「上线」一步推到 active
  node({ name: '大阪 01（待上线）', node_type: 'shadowsocks', serving_status: 'draft', country_code: 'JP', server_port: 8388, pool_id: pools[1]!.id, protocol_config: { cipher: 'aes-256-gcm' } }, 7),
  // R113：同一台新服务器上、划进还没绑套餐的新池的节点——首次搭建的常态，上线成功但带 warnings
  node({ name: '大阪 02（新池待上线）', node_type: 'shadowsocks', serving_status: 'draft', country_code: 'JP', server_port: 8389, pool_id: pools[4]!.id, protocol_config: { cipher: 'aes-256-gcm' } }, 7),
  node(
    {
      name: '法兰克福 01 · ECH',
      node_type: 'trojan',
      serving_status: 'active',
      country_code: 'DE',
      protocol_config: { network: 'tcp', tls: 1, cert_mode: 'acme' },
      cert: {
        mode: 'acme',
        identifier: 'fra1.example.com',
        identifier_kind: 'domain',
        not_after: fromNow(5 * 86400000),
        error_code: 'rate_limited',
        error_detail: null,
        next_attempt_at: fromNow(3600000),
        reported_at: ago(90),
      },
      xb: {
        ...xbDefaults(),
        tags: ['欧洲'],
        ech: { public_name: 'ech.example.test', config_list: 'YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXowMTIzNDU2Nzg5QUJDREVGR0g=', created_at: ago(86400 * 3) },
      },
    },
    4,
  ),
  // 没绑服务器的草稿：节点先作为配置存着（不占端口、不下发），详情「身份与操作」里绑定后才能上线
  node({ name: '伦敦 01', node_type: 'trojan', serving_status: 'draft', server_id: null, country_code: 'GB', protocol_config: { network: 'tcp', tls: 1, cert_mode: 'acme' } }),
]
store[0]!.routing = { outbounds: [], routes: [{ priority: 10, matcher: { domain_suffix: ['openai.com'] }, outbound_tag: 'US-LAX-01', enabled: true, note: '' }] }

const NO_POOL_NOTE = '未划入节点池，不服务任何用户'

// 服务器状态机里能进 ready 的状态（已 ready 的不动）
const SERVER_TO_READY = ['draft', 'draining', 'maintenance']
// 节点还没绑服务器就上线时的文案（activate 与批量改状态同一条）
const UNBOUND_ACTIVATE = '节点还没有绑定服务器，先在节点详情「身份与操作」里绑定服务器再上线'
/** 照 Go serverNotEnrolledMsg */
const SERVER_NOT_ENROLLED = '服务器还没接入（先在服务器页生成安装命令完成接入）'

/** 照 Go nodeProtocolReady：协议类型、对外地址、端口、schema 版本 */
const protocolReady = (n: Node): boolean => Boolean(n.node_type && n.server_host && n.server_port !== null && n.protocol_schema_version > 0)

/** 上线成功但不会服务任何人的提示（与 delivery_note 同口径，R105 / R113；见 node/service 的 nodeNoPoolWarning） */
function activationWarnings(n: Node): string[] {
  if (n.pool_id === null) return [NO_POOL_NOTE]
  return pools.find((p) => p.id === n.pool_id)?.plans.length ? [] : ['所在节点池没有绑定任何套餐，暂时不服务任何用户']
}

// subscription.DeliveryState / NodeDeliveryFacts.Refine 的文案，与 Go 一字不差
const DELIVERY_NOTES = {
  unbound: '未绑定服务器，不下发：先在节点详情「身份与操作」里绑定服务器，再上线',
  notActive: '节点不在服务中（草稿、已停用或已退役）',
  noPool: NO_POOL_NOTE,
  poolInactive: '所在节点池未启用，不下发',
  poolUnbound: '所在节点池没有绑定任何套餐，不会出现在任何用户的订阅里',
  neverSeen: '所在服务器从未上报过心跳，不下发 —— 多半是还没在机器上安装节点端。发出去就是一条必然连不上的线路',
  exceeded: XB_EXCEEDED_NOTE,
  nodeNotReady: '协议、端口或可连地址不完整，不下发',
  serverNotReady: '所属服务器未就绪或已删除，不下发',
  stale: '心跳已超时，但仍在下发 —— 只有当套餐里一个新鲜节点都没有时才会用到它。空订阅会让客户端清空服务器列表，比给一条可能不通的线路更糟',
}

/** repo.DeliverableNodeCond 的镜像：与用户、套餐都无关的节点自身条件 */
function deliverable(n: Node): boolean {
  const srv = serverById(n.server_id)
  return Boolean(n.serving_status === 'active' && !n.xb.exceeded_at && srv && srv.status === 'ready' && !!srv.last_heartbeat_at)
}

/** 套餐页 GET v1/plans/{id}/pools 的 deliverable_nodes：池里节点自身可下发的个数 */
export const deliverableNodesInPool = (poolId: string): number => store.filter((n) => n.pool_id === poolId && deliverable(n)).length

/** 后台节点列表的 delivered_to_users / delivery_note：按 Go nodeDeliveryVerdict 顺序判定 */
function deliveryOf(n: Node): { delivered: boolean; note: string } {
  // 删服务器时退役的节点也没有服务器，但已不能再绑，照「未在役」说（同 Go nodeDeliveryVerdict）
  if (!n.server_id && n.serving_status !== 'retired') return { delivered: false, note: DELIVERY_NOTES.unbound }
  if (n.serving_status !== 'active') return { delivered: false, note: DELIVERY_NOTES.notActive }
  if (n.pool_id === null) return { delivered: false, note: DELIVERY_NOTES.noPool }
  const pool = pools.find((p) => p.id === n.pool_id)
  if (pool?.status !== 'active') return { delivered: false, note: DELIVERY_NOTES.poolInactive }
  if (!pool?.plans.length) return { delivered: false, note: DELIVERY_NOTES.poolUnbound }
  const hb = hbOf(n)
  if (!hb) return { delivered: false, note: DELIVERY_NOTES.neverSeen }
  if (n.xb.exceeded_at) return { delivered: false, note: DELIVERY_NOTES.exceeded }
  if (!deliverable(n)) {
    const srv = serverById(n.server_id)
    return { delivered: false, note: srv?.status === 'ready' ? DELIVERY_NOTES.nodeNotReady : DELIVERY_NOTES.serverNotReady }
  }
  const fresh = Date.now() - new Date(hb).getTime() < 600_000
  return { delivered: true, note: fresh ? '' : DELIVERY_NOTES.stale }
}

function listRow(n: Node) {
  const srv = serverById(n.server_id)
  const pool = pools.find((p) => p.id === n.pool_id)
  const hb = hbOf(n)
  const stale = heartbeatStale(hb)
  const { delivered, note } = deliveryOf(n)
  const m = serverMetricsLatest(srv)
  return {
    id: n.id,
    node_no: n.node_no,
    row_version: n.row_version,
    name: n.name,
    serving_status: n.serving_status,
    server_id: n.server_id,
    server_name: srv?.name ?? null,
    pool_id: n.pool_id,
    pool_name: pool?.name ?? null,
    agent_version: srv?.agent_version ?? null,
    hostname: srv?.hostname ?? null,
    public_ipv4: srv?.public_ipv4 ?? null,
    cpu_cores: srv?.cpu_cores ?? null,
    memory_mb: srv?.memory_mb ?? null,
    disk_gb: srv?.disk_gb ?? null,
    last_heartbeat_at: hb,
    stale,
    delivered_to_users: delivered,
    delivery_note: note,
    identity_serial: activeIdentitySeq(srv),
    // 与 service.nodeMovableState 同口径：草稿或已停用能改绑 / 解绑
    movable: n.serving_status === 'draft' || n.serving_status === 'disabled',
    created_at: n.created_at,
    node_type: n.node_type,
    server_host: n.server_host,
    server_port: n.server_port,
    display_name: n.display_name,
    country_code: n.country_code,
    sort_order: n.sort_order,
    online_users: n.online_users,
    online_ips: n.online_ips,
    traffic_bytes_24h: n.traffic_bytes_24h,
    cpu_percent: m?.cpu_percent ?? null,
    mem_percent: m?.mem_percent ?? null,
    metrics_at: m?.at ?? null,
    granted_plans: pool?.plans.length ? [...pool.plans].sort() : [],
    cert: n.cert,
    ...xbRow(n.xb),
  }
}

/** GET v1/nodes?id= 的一行：列表行加上编辑表单才用的字段（对应 node/handler 的 nodeDetailResponse） */
function detailRow(n: Node) {
  return {
    ...listRow(n),
    traffic_rate: n.traffic_rate,
    protocol_config: redact(n.protocol_config),
    protocol_schema_version: n.protocol_schema_version,
    config_validated_at: n.node_type ? n.updated_at : null,
  }
}

const ipIdentifier = (id: string | null): boolean => Boolean(id && (/^[0-9.]+$/.test(id) || id.includes(':')))

/** 照 Go CertExpiringCond：证书快到期（IP 2 天、域名 14 天内） */
function certExpiring(n: Node): boolean {
  if (!n.cert || n.cert.not_after === null) return false
  const horizon = (ipIdentifier(n.cert.identifier) ? 2 : 14) * 86400_000
  return new Date(n.cert.not_after).getTime() < Date.now() + horizon
}

/** 照 Go CertAttentionNodes（指向 node/repo 的 CertAttentionBaseCond / CertFailingCond / CertExpiringCond） */
export function certAttention(n: Node): boolean {
  return Boolean(n.serving_status === 'active' && n.cert !== null && (n.cert.error_code !== null || certExpiring(n)))
}

/** node/repo 的 nodeListFilterSQL：后台列表的状态筛选（「离线」= 在役且心跳超过 90 秒） */
const NODE_STATES = ['all', 'online', 'offline', 'disabled', 'retired', 'cert']
function matchesState(n: Node, state: string): boolean {
  const stale = heartbeatStale(hbOf(n))
  switch (state) {
    case 'all':
      return n.serving_status !== 'retired'
    case 'online':
      return n.serving_status === 'active' && !stale
    case 'offline':
      return n.serving_status === 'active' && stale
    case 'disabled':
      return n.serving_status === 'draft' || n.serving_status === 'disabled'
    case 'retired':
      return n.serving_status === 'retired'
    case 'cert':
      return certAttention(n)
    default:
      return true
  }
}
/** 服务端搜索：名称、展示名、服务器名、国家、协议、地址、编号里按子串找，不分大小写 */
function matchesSearch(n: Node, q: string): boolean {
  if (!q) return true
  const srv = serverById(n.server_id)
  const hay = [n.name, n.display_name, srv?.name, n.country_code, n.node_type, n.server_host, String(n.node_no)].filter(Boolean).join(' ').toLowerCase()
  return hay.includes(q.toLowerCase())
}

/** 按名字在任意深度删掉敏感键（node/protocol 的 Redact 与 sensitiveProtocolKey） */
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
    ...pick('id', 'row_version', 'name', 'server_id', 'pool_id', 'serving_status', 'node_type', 'server_host', 'server_port', 'traffic_rate', 'display_name', 'country_code', 'protocol_schema_version', 'sort_order', 'created_at', 'updated_at'),
    protocol_config: redact(n.protocol_config),
    config_validated_at: n.updated_at,
    ...(warnings?.length ? { warnings } : {}),
    ...xbAdmin(n.xb),
  }
}

function flatten(obj: unknown, prefix = ''): Array<[string, unknown]> {
  if (!obj || typeof obj !== 'object' || Array.isArray(obj)) return [[prefix, obj]]
  return Object.entries(obj).flatMap(([k, v]) => (v && typeof v === 'object' && !Array.isArray(v) && !['headers', 'padding_scheme'].includes(k) ? flatten(v, `${prefix}${k}.`) : [[`${prefix}${k}`, v]]))
}

/** 简化的协议校验：协议必须 stable、字段在 allowed 内、必填齐、枚举值合法；vless/trojan REALITY 需要五项；vless 裸 tcp / trojan 缺 tls 校验 */
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
  // 与真后端一致：cert_mode 缺省按 file，不必填；acme 时不要 cert_path / key_path（required 只是表单在显示时的必填标记）
  const acme = flat.get('cert_mode') === 'acme'
  for (const path of s.required ?? []) {
    if (path === 'cert_mode' || (acme && (path === 'cert_path' || path === 'key_path'))) continue
    if (!flat.has(path)) fields[`protocol_config.${path}`] = '必填'
  }
  const tlsVal = flat.get('tls')
  const netVal = flat.get('network')
  if (nodeType === 'vless' && (tlsVal === undefined || tlsVal === 0 || tlsVal === '0') && (netVal === undefined || netVal === '' || netVal === 'tcp')) {
    fields['protocol_config.network'] = 'tcp 不加密就是明文代理（UUID 和目标地址全裸）：请改用 REALITY，或换 ws / httpupgrade / grpc / xhttp 并套 CDN'
  }
  if ((nodeType === 'vless' || nodeType === 'trojan') && String(tlsVal) === '2') {
    // 照 Go Validate：内核键（server_names、short_ids…）报的错会映射回表单路径 protocol_config.reality_settings.<键>
    const realityFields: Record<string, string> = {
      dest: '必填：借用握手的真实站点，如 www.apple.com:443（需支持 TLS 1.3 + X25519）',
      server_name: '必填：至少一个，且要和 dest 的证书对得上',
      private_key: '私钥必填',
      public_key: '公钥必填（客户端要用它，不能省）',
      short_id: '必填：至少一个 short id（不超过 16 位的十六进制，可用「生成」按钮）',
    }
    for (const [key, msg] of Object.entries(realityFields)) {
      if (!flat.get(`reality_settings.${key}`)) fields[`protocol_config.reality_settings.${key}`] = msg
    }
  }
  if (nodeType === 'trojan' && (tlsVal === undefined || tlsVal === 0 || tlsVal === '0')) {
    fields['protocol_config.tls'] = 'Trojan 普通传输必须启用 tls'
  }
  return fields
}

const EDGES: Readonly<Record<string, readonly string[]>> = {
  draft: ['active', 'retired'],
  active: ['disabled', 'retired'],
  disabled: ['active', 'retired'],
  retired: [],
}

function basicErrors(body: Json, creating: boolean): Record<string, string> {
  const f: Record<string, string> = {}
  const name = body.name === undefined && !creating ? null : text(body.name).trim()
  if (name !== null && (!name || [...name].length > 120)) f.name = '名称必填，不超过 120 字'
  // 新建时服务器可以不传（= 暂不绑定）；给了才要求存在
  const serverId = text(body.server_id)
  const serverExists = !serverId || servers.some((s) => s.id === serverId)
  if (creating && !serverExists) f.server_id = '服务器不存在'
  if (body.pool_id && !pools.some((p) => p.id === body.pool_id)) f.pool_id = '资源池不存在'
  // 地址任何时候都可以空，给了才校验格式
  const host = text(body.server_host)
  if ((creating || body.server_host !== undefined) && host !== '' && !/^[A-Za-z0-9.-]+$/.test(host)) f.server_host = '必须是 IP 或 ASCII 主机名'
  const portOk = int(body.server_port) !== null && int(body.server_port)! >= 1 && int(body.server_port)! <= 65535
  if (creating) {
    // 新建：端口给了就要合法；绑了服务器时端口必填，不绑时可以空
    if (body.server_port !== undefined && body.server_port !== null && body.server_port !== '') {
      if (!portOk) f.server_port = '端口 1–65535'
    } else if (serverId && serverExists) f.server_port = '绑定了服务器就要填端口（1 到 65535）'
  } else if (body.server_port !== undefined && !portOk) {
    // 编辑：给了就必须 1–65535，不能清空
    f.server_port = '端口 1–65535'
  }
  if (body.traffic_rate !== undefined && !(Number(body.traffic_rate) > 0)) f.traffic_rate = '必须大于 0'
  if (body.country_code != null && body.country_code !== '' && !/^[A-Za-z]{2}$/.test(text(body.country_code))) f.country_code = '必须是两位字母国家代码'
  return f
}

const findNode = (id: string | undefined) => store.find((n) => n.id === id && !n.deleted)

/** 只给 tests/mock-admin-nodes.test.ts 核对 R106 / R107 的补回：接口永远抹掉敏感键，测试只能从库里看 */
/** 没删除的节点 id（实时事件模拟取真实行用，见 events.ts） */
export const liveNodeIds = () => store.filter((n) => !n.deleted).map((n) => n.id)
export const storedProtocolConfig = (id: string) => structuredClone(findNode(id)?.protocol_config ?? null)
const touch = (n: Node) => {
  n.row_version += 1
  n.updated_at = new Date().toISOString()
}
const conflict = (n: Node) => err(409, 'conflict', '节点已被其他人修改，请刷新后重试', { row_version: `current=${n.row_version}` })

function validateListTag(raw: string): { ok: true; tag: string } | { ok: false; fields: Record<string, string> } {
  const tag = raw.trim()
  if (!tag) return { ok: true, tag: '' }
  for (const ch of tag) {
    const c = ch.codePointAt(0)!
    if (c <= 0x1f || c === 0x7f) return { ok: false, fields: { tag: '标签最多 16 个字、不能含控制字符' } }
  }
  if ([...tag].length > 16) return { ok: false, fields: { tag: '标签最多 16 个字、不能含控制字符' } }
  return { ok: true, tag }
}

/** 批量/PATCH 共用的可改字段校验（pool_id、traffic_rate、XB 字段） */
function patchErrors(n: Node, set: Json): Record<string, string> | null {
  const f: Record<string, string> = {}
  if ('pool_id' in set) {
    const pid = text(set.pool_id)
    if (pid && !pools.some((p) => p.id === pid)) f.pool_id = '资源池不存在'
    if (!pid && n.serving_status === 'active') f.pool_id = '在役节点必须属于一个节点池；不选池则不服务任何用户，先停用节点再移出'
  }
  if ('traffic_rate' in set && !(Number(set.traffic_rate) > 0)) f.traffic_rate = '必须大于 0'
  const xbBody: Json = {}
  for (const k of XB_BODY_KEYS) if (k in set) xbBody[k] = set[k]
  Object.assign(f, xbErrors(xbBody))
  return Object.keys(f).length ? f : null
}

function patchApply(n: Node, set: Json): void {
  if ('pool_id' in set) n.pool_id = set.pool_id === null || set.pool_id === '' ? null : text(set.pool_id)
  if ('traffic_rate' in set) n.traffic_rate = Number(set.traffic_rate)
  applyXB(n.xb, set)
}

/** 与 POST /v1/nodes/status:batch 相同的状态边校验；null = 通过（此时不改状态） */
function batchStatusCheck(rows: Node[], to: string): MockResult | null {
  if (!(to in EDGES)) return invalid({ serving_status: '不支持的服务状态' })
  for (const n of rows) {
    if (!EDGES[n.serving_status]!.includes(to)) return err(409, 'conflict', '不允许的节点服务状态转换', { serving_status: `${n.serving_status} -> ${to}` })
    if (to === 'active') {
      if (!n.server_id) return err(409, 'conflict', UNBOUND_ACTIVATE, { server_id: UNBOUND_ACTIVATE, id: n.id })
      const srv = serverById(n.server_id)
      if (!protocolReady(n) || !srv || srv.status !== 'ready') {
        return err(409, 'conflict', '节点协议未配好或所在服务器未就绪，不能启用；新服务器上的节点请用「上线」', { id: n.id })
      }
      if (!serverEnrolled(srv)) {
        return err(409, 'conflict', SERVER_NOT_ENROLLED, { server_id: SERVER_NOT_ENROLLED, id: n.id })
      }
    }
  }
  return null
}

function batchApplyStatus(rows: Node[], to: string): void {
  for (const n of rows) {
    n.serving_status = to
    touch(n)
  }
}

// ---------------------------------------------------------------------------
// 路由
// ---------------------------------------------------------------------------
export const nodes: MockModule = {
  routes: {
    ...infraRoutes(store),
    ...routeGroupRoutes(store),
    ...xbRoutes({
      find: (id) => (id === undefined ? undefined : findNode(id)),
      list: () => store.filter((n) => !n.deleted) as XBHost[],
      batchPatch: (n, set) => patchErrors(n as Node, set),
      batchApply: (n, set) => patchApply(n as Node, set),
      batchStatus: (rows, to) => batchStatusCheck(rows as Node[], to),
      batchApplyStatus: (rows, to) => batchApplyStatus(rows as Node[], to),
    }),
    'GET /v1/node-protocol-schemas': (ctx) => {
      ctx.send(200, NODE_PROTOCOL_SCHEMAS)
    },
    'GET /v1/nodes': (ctx) => {
      // 节点列表查询校验：不认得的状态、过长的搜索词、不是 UUID 的 id 回 422
      const state = ctx.query.get('state') ?? ''
      const q = (ctx.query.get('q') ?? '').trim()
      const id = (ctx.query.get('id') ?? '').trim()
      if (state && !NODE_STATES.includes(state)) return reply(ctx, invalid({ state: '只能是 all、online、offline、disabled、retired、cert 之一' }))
      if ([...q].length > 120) return reply(ctx, invalid({ q: '搜索词太长' }))
      if (id && !UUID.test(id)) return reply(ctx, invalid({ id: '格式不正确' }))
      const tagRaw = ctx.query.get('tag') ?? ''
      const tagParsed = validateListTag(tagRaw)
      if (!tagParsed.ok) return reply(ctx, invalid(tagParsed.fields))
      const tagFilter = tagParsed.tag
      // 给了 state 就由它决定含不含已退役
      const withRetired = state !== '' || ctx.query.get('include_retired') === '1'
      const limit = Number(ctx.query.get('limit'))
      const offset = Number(ctx.query.get('offset')) || 0
      const rows = store
        .filter((n) => !n.deleted && (withRetired || n.serving_status !== 'retired') && matchesState(n, state) && matchesSearch(n, q) && (!id || n.id === id) && (!tagFilter || n.xb.tags.includes(tagFilter)))
        .sort((a, b) => a.sort_order - b.sort_order || a.node_no - b.node_no)
      const size = limit >= 1 && limit <= 1000 ? limit : 500
      const page = rows.slice(offset, offset + size)
      ctx.send(200, { nodes: id ? page.map(detailRow) : page.map(listRow), total: rows.length })
    },
    'POST /v1/nodes': async (ctx) => {
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('node_create', () => {
        const extra = unknownField(body, ['name', 'server_id', 'node_type', 'server_host', 'server_port', 'protocol_config', 'pool_id', 'traffic_rate', 'display_name', 'sort_order', 'country_code', ...XB_BODY_KEYS])
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        const fields = { ...basicErrors(body, true), ...validateProtocol(text(body.node_type), body.protocol_config), ...xbErrors(body) }
        if (Object.keys(fields).length) return invalid(fields)
        // 不绑服务器时不查服务器状态
        const srv = text(body.server_id) ? servers.find((s) => s.id === body.server_id) : undefined
        if (srv && srv.status !== 'ready' && srv.status !== 'draft') return err(409, 'conflict', '目标服务器当前不接受节点')
        if (store.some((n) => n.name === text(body.name).trim() && !n.deleted)) return err(409, 'conflict', '节点名称已存在')
        const n = node({
          name: text(body.name).trim(),
          node_type: text(body.node_type),
          serving_status: 'draft',
          server_host: text(body.server_host),
          server_port: int(body.server_port),
          pool_id: text(body.pool_id) || null,
          traffic_rate: Number(body.traffic_rate) > 0 ? Number(body.traffic_rate) : 1,
          display_name: text(body.display_name) || null,
          country_code: text(body.country_code).toUpperCase() || null,
          protocol_config: body.protocol_config as Json,
        })
        n.server_id = srv?.id ?? null
        n.row_version = 1
        applyXB(n.xb, body)
        store.push(n)
        return { status: 201, body: adminNode(n) }
      })
    },
    'PATCH /v1/nodes/:id': async (ctx) => {
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const extra = unknownField(body, ['row_version', 'name', 'pool_id', 'node_type', 'server_host', 'server_port', 'traffic_rate', 'display_name', 'protocol_config', 'country_code', ...XB_BODY_KEYS])
      if (extra) return ctx.fail(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
      const n = findNode(ctx.params.id)
      if (!n) return reply(ctx, notFound('节点不存在'))
      if (int(body.row_version) === null) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { row_version: '必填' })
      if (body.row_version !== n.row_version) return reply(ctx, conflict(n))
      const nodeType = body.node_type === undefined ? n.node_type ?? '' : text(body.node_type)
      // 换协议类型时不补旧密钥（R106）
      const config = body.protocol_config === undefined ? n.protocol_config : nodeType === n.node_type ? restoreSecrets(n.protocol_config, body.protocol_config) : body.protocol_config
      const touched = ['node_type', 'server_host', 'server_port', 'protocol_config'].some((k) => body[k] !== undefined)
      const fields = { ...basicErrors(body, false), ...(touched ? validateProtocol(nodeType, config) : {}), ...xbErrors(body) }
      // node/service 的 PatchNode：在役节点不许清空池（无池节点不服务任何用户），草稿等仍可无池
      if (body.pool_id !== undefined && !text(body.pool_id) && n.serving_status === 'active') fields.pool_id = '在役节点必须属于一个节点池；不选池则不服务任何用户，先停用节点再移出'
      // 对外地址可以清空，但在役节点上线前必须填
      if (body.server_host !== undefined && !text(body.server_host) && n.serving_status === 'active') fields.server_host = '必填：上线前要填对外地址'
      if (Object.keys(fields).length) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', fields)
      if (body.name !== undefined && store.some((x) => x !== n && x.name === text(body.name).trim())) return ctx.fail(409, 'conflict', '节点名称已存在')
      if (body.name !== undefined) n.name = text(body.name).trim()
      if (body.pool_id !== undefined) n.pool_id = text(body.pool_id) || null
      if (body.node_type !== undefined) n.node_type = nodeType
      if (body.server_host !== undefined) n.server_host = text(body.server_host)
      if (body.server_port !== undefined) n.server_port = int(body.server_port)
      if (body.traffic_rate !== undefined) n.traffic_rate = Number(body.traffic_rate)
      if (body.display_name !== undefined) n.display_name = text(body.display_name) || null
      if (body.country_code !== undefined) n.country_code = text(body.country_code).toUpperCase() || null
      if (body.protocol_config !== undefined) n.protocol_config = config as Json
      applyXB(n.xb, body)
      touch(n)
      ctx.send(200, adminNode(n))
    },
    'POST /v1/nodes/:id/copy': async (ctx) => {
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('node_copy', () => {
        const src = findNode(ctx.params.id)
        if (!src) return notFound('节点不存在')
        if (body.row_version !== src.row_version) return conflict(src)
        const name = text(body.name).trim() || `${src.name}-copy`
        if (store.some((n) => n.name === name && !n.deleted)) return err(409, 'conflict', '节点名称已存在')
        // 源节点没绑服务器、也没指定目标服务器时，复制出来同样不绑，不查服务器状态
        const targetId = text(body.target_server_id) || src.server_id
        const target = targetId ? servers.find((s) => s.id === targetId) : undefined
        if (targetId && !target) return invalid({ target_server_id: '服务器不存在' })
        if (target && target.status !== 'ready') return err(409, 'conflict', '目标服务器当前不接受节点')
        const copy = node({ ...structuredClone({ ...src, routing: body.copy_routing ? src.routing : { outbounds: [], routes: [] } }), name, serving_status: 'draft', online_users: 0, online_ips: 0, traffic_bytes_24h: 0 })
        // node() 先给新 id 与编号，再被源节点的字段覆盖：复制出来的必须是新节点
        copy.id = randomUUID()
        copy.node_no = nodeNo
        copy.server_id = target?.id ?? null
        copy.sort_order = src.sort_order + 1
        copy.row_version = 1
        copy.xb = {
          ...xbDefaults(),
          tags: [...src.xb.tags],
          limit_bytes: src.xb.limit_bytes,
          reset_day: src.xb.reset_day,
          schedule: src.xb.schedule.map((s) => ({ ...s })),
          used_bytes: 0,
          exceeded_at: null,
          reset_at: new Date().toISOString(),
          relays: [],
          ech: null,
          ech_retired: 0,
        }
        if (body.copy_routing) copyMemberships(src.id, copy.id)
        store.push(copy)
        return { status: 201, body: adminNode(copy) }
      })
    },
    // 绑定 / 解绑服务器，照 node/service 的 BindNode / UnbindNode（nodes_binding.go）：幂等、无 reauth；
    // 判断顺序同 Go——版本号非法 422、端口范围 422、服务器 id 必填、节点不存在 404、版本冲突 409、
    // 已退役 / 在役 409、端口为空 422、目标就是当前服务器 200、服务器不存在 422、目标已退役 409、端口被占 409
    'POST /v1/nodes/:id/bind': async (ctx) => {
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('node_bind', () => {
        const extra = Object.keys(body).find((k) => !['row_version', 'server_id', 'server_port', 'reason'].includes(k))
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        if (!(typeof body.row_version === 'number' && body.row_version > 0)) return invalid({ row_version: '必须提供正整数版本号' })
        if (body.server_port !== undefined && !(int(body.server_port)! >= 1 && int(body.server_port)! <= 65535)) return invalid({ server_port: '端口必须在 1 到 65535 之间' })
        if (!text(body.server_id)) return invalid({ server_id: '必填' })
        const n = findNode(ctx.params.id)
        if (!n) return notFound('节点不存在')
        if (body.row_version !== n.row_version) return conflict(n)
        if (n.serving_status === 'retired') return err(409, 'conflict', '节点已退役，不能改绑或解绑服务器')
        if (n.serving_status === 'active') return err(409, 'conflict', '节点在役，先停用再改绑 / 解绑')
        // 端口取请求里的，没给就沿用节点已有的
        const port = int(body.server_port) ?? n.server_port
        if (port === null) return invalid({ server_port: '绑定服务器前要先填端口' })
        const target = servers.find((s) => s.id === body.server_id)
        if (!target) return invalid({ server_id: '服务器不存在或已删除' })
        if (target.id === n.server_id && port === n.server_port) return { status: 200, body: adminNode(n) }
        if (target.id !== n.server_id && target.status === 'retired') return err(409, 'conflict', '目标服务器已退役，不接受节点')
        // 同一服务器上未退役、未删除的节点占用同一端口（假后端不区分 L4，统一按 TCP）
        const holder = store.find((x) => x !== n && !x.deleted && x.serving_status !== 'retired' && x.server_id === target.id && x.server_port === port)
        if (holder) {
          const note = `端口 ${port}/TCP 已被同一服务器上的节点「${holder.name}」占用`
          return err(409, 'conflict', `${note}，换一个端口或先退役占用的节点`, { server_port: note })
        }
        n.server_id = target.id
        n.server_port = port
        touch(n)
        return { status: 200, body: adminNode(n) }
      })
    },
    'POST /v1/nodes/:id/unbind': async (ctx) => {
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('node_unbind', () => {
        const extra = Object.keys(body).find((k) => !['row_version', 'reason'].includes(k))
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        if (!(typeof body.row_version === 'number' && body.row_version > 0)) return invalid({ row_version: '必须提供正整数版本号' })
        const n = findNode(ctx.params.id)
        if (!n) return notFound('节点不存在')
        if (body.row_version !== n.row_version) return conflict(n)
        if (n.serving_status === 'retired') return err(409, 'conflict', '节点已退役，不能改绑或解绑服务器')
        if (n.serving_status === 'active') return err(409, 'conflict', '节点在役，先停用再改绑 / 解绑')
        // 本来就没绑：200 原样返回，不改版本
        if (!n.server_id) return { status: 200, body: adminNode(n) }
        n.server_id = null
        touch(n)
        return { status: 200, body: adminNode(n) }
      })
    },
    'PUT /v1/nodes/order': async (ctx) => {
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
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('node_status_batch', () => {
        const items = Array.isArray(body.items) ? (body.items as Json[]) : []
        const to = text(body.serving_status)
        if (items.length < 1 || items.length > 100) return invalid({ items: '一次 1–100 项' })
        if (!(to in EDGES)) return invalid({ serving_status: '不支持的服务状态' })
        const rows: Node[] = []
        for (const it of items) {
          const n = findNode(text(it.id))
          if (!n) return notFound('节点不存在')
          if (it.row_version !== n.row_version) return conflict(n)
          rows.push(n)
        }
        const st = batchStatusCheck(rows, to)
        if (st) return st
        batchApplyStatus(rows, to)
        return { status: 200, body: { ok: true, updated: items.length, serving_status: to } }
      })
    },
    'POST /v1/nodes/reality-keypair': (ctx) => {
      ctx.send(200, { private_key: randomBytes(32).toString('base64url'), public_key: randomBytes(32).toString('base64url'), short_id: randomBytes(4).toString('hex'), hint: '私钥只在保存时写入，之后读接口不回显' })
    },
    'DELETE /v1/nodes/:id': async (ctx) => {
      if (!ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body || emptyBody(ctx)) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const n = findNode(ctx.params.id)
      if (!n) return reply(ctx, notFound('节点不存在'))
      if (int(body.row_version) && body.row_version !== n.row_version) return reply(ctx, conflict(n))
      if (n.serving_status !== 'draft' && n.serving_status !== 'retired') return ctx.fail(422, 'validation_failed', '节点还在服务或已停用，先退役再删除')
      n.serving_status = 'retired'
      n.deleted = true
      n.name = `${n.name}#deleted-${Date.now()}`
      ctx.send(200, { deleted: true })
    },
    // R108 / R113：一步上线，照 node/service 的 ActivateNode：幂等 node_activate、无 reauth；
    // 判断顺序同 Go——版本号非法 422、节点不存在 404、已是 active 回 200 不改动（先于版本号）、版本冲突 409、
    // retired 409、未绑服务器 409、协议就绪、服务器不存在、节点身份、服务器状态；服务器跟着进 ready；warnings 没有提示时不出现
    'POST /v1/nodes/:id/activate': async (ctx) => {
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('node_activate', () => {
        const extra = Object.keys(body).find((k) => k !== 'row_version')
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        if (!(typeof body.row_version === 'number' && body.row_version > 0)) return invalid({ row_version: '必须提供正整数版本号' })
        const n = findNode(ctx.params.id)
        if (!n) return notFound('节点不存在')
        if (n.serving_status === 'active') return { status: 200, body: adminNode(n, activationWarnings(n)) }
        if (body.row_version !== n.row_version) return conflict(n)
        if (n.serving_status === 'retired') return err(409, 'conflict', '节点已退役，不能上线')
        if (!n.server_id) return err(409, 'conflict', UNBOUND_ACTIVATE, { server_id: UNBOUND_ACTIVATE })
        if (!protocolReady(n)) return err(409, 'conflict', '节点的协议配置还没就绪（协议类型、对外地址、端口），补齐后再上线')
        const srv = serverById(n.server_id)
        if (!srv) return err(409, 'conflict', '节点所在的服务器已删除，不能上线')
        if (!serverEnrolled(srv)) return err(409, 'conflict', SERVER_NOT_ENROLLED, { server_id: SERVER_NOT_ENROLLED })
        if (srv.status !== 'ready' && !SERVER_TO_READY.includes(srv.status)) return err(409, 'conflict', `节点所在的服务器处于「${SERVER_STATUS_LABEL[srv.status] ?? srv.status}」，不能投入服务：把节点换绑到其他服务器后再上线`)
        n.serving_status = 'active'
        srv.status = 'ready'
        touch(n)
        return { status: 200, body: adminNode(n, activationWarnings(n)) }
      })
    },

    'POST /v1/nodes/:id/retire': async (ctx) => {
      if (!ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('node_retire', () => {
        const n = findNode(ctx.params.id)
        if (!n) return notFound('节点不存在')
        if (body.row_version !== n.row_version) return conflict(n)
        if (n.serving_status === 'retired') return err(409, 'conflict', '节点已经退役')
        n.serving_status = 'retired'
        touch(n)
        return { status: 200, body: adminNode(n) }
      })
    },
    'POST /v1/nodes/:id/revoke-identity': (ctx) => {
      if (!ctx.requireReauth()) return
      const n = findNode(ctx.params.id)
      if (!n) return reply(ctx, notFound())
      // 照 Go RevokeNodeIdentity：没绑服务器、服务器没有有效身份都是 409
      if (!n.server_id) return reply(ctx, err(409, 'conflict', '节点还没放到服务器上，没有接入身份'))
      const srv = serverById(n.server_id)
      if (!srv || !revokeServerIdentity(srv, '后台吊销')) {
        return reply(ctx, err(409, 'conflict', '这台服务器没有有效的接入身份'))
      }
      ctx.send(200, { ok: true })
    },
    'POST /v1/nodes/config/publish': async (ctx) => {
      if (!ctx.requireReauth()) return
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
      const n = findNode(ctx.params.id)
      if (!n) return reply(ctx, notFound('节点不存在'))
      const mParam = Number(ctx.query.get('minutes'))
      const minutes = Number.isInteger(mParam) && mParam >= 1 && mParam <= 1440 ? mParam : 60
      const srv = serverById(n.server_id)
      if (!srv || !srv.last_heartbeat_at || !srv.probe) return ctx.send(200, { points: [], latest: null })
      const now = Date.now()
      const end = Date.parse(srv.last_heartbeat_at)
      const start = now - minutes * 60_000
      const timestamps: number[] = []
      for (let t = end; t >= start; t -= 600_000) {
        timestamps.push(t)
      }
      timestamps.reverse()
      if (timestamps.length === 0) return ctx.send(200, { points: [], latest: null })
      const totalOnline = store
        .filter((x) => !x.deleted && x.server_id === srv.id && x.serving_status === 'active')
        .reduce((sum, x) => sum + x.online_users, 0)
      const scale = totalOnline / 400 || 0.05
      const points = timestamps.map((at, idx) => {
        const wave = 0.5 + 0.5 * Math.sin(((at / 3600_000) % 24) / 3.8)
        const isLast = idx === timestamps.length - 1
        return {
          at: new Date(at).toISOString(),
          cpu_percent: isLast ? srv.probe!.cpu_bp / 100 : Math.round(20 + 50 * wave * scale),
          mem_percent: 55,
          load1: 0.8,
          rx_speed: Math.round(40e6 * wave * scale),
          tx_speed: Math.round(8e6 * wave * scale),
          tcp_conns: Math.round(900 * wave * scale),
        }
      })
      const lastPoint = points.at(-1)!
      ctx.send(200, {
        points,
        latest: {
          cpu_percent: srv.probe.cpu_bp / 100,
          mem_used_mb: srv.probe.mem_used_mb,
          mem_total_mb: srv.probe.mem_total_mb,
          disk_used_gb: srv.probe.disk_used_gb,
          disk_total_gb: srv.probe.disk_total_gb,
          load1: 0.8,
          load5: 0.7,
          load15: 0.6,
          tcp_conns: lastPoint.tcp_conns,
          uptime_sec: 86400 * 12,
          rx_total: 9e12,
          tx_total: 2e12,
        },
      })
    },
    'GET /v1/nodes/:id/routing': (ctx) => {
      const n = findNode(ctx.params.id)
      if (!n) return reply(ctx, notFound('节点不存在'))
      ctx.send(200, { row_version: n.row_version, groups: groupRefsOfNode(n.id), ...n.routing })
    },
    'PUT /v1/nodes/:id/routing': async (ctx) => {
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const n = findNode(ctx.params.id)
      if (!n) return reply(ctx, notFound('节点不存在'))
      if (body.row_version !== n.row_version) return reply(ctx, conflict(n))
      const outbounds = (Array.isArray(body.outbounds) ? body.outbounds : []) as Array<{ tag: string; type: string; settings?: unknown }>
      const routes = (Array.isArray(body.routes) ? body.routes : []) as Json[]
      // 规则可以指向全局与所在路由组的出站（routing_refs.go 的 visibleOutboundTagsTx）
      const bad = validateRouting(outbounds, routes, visibleTagsForNode(n.id))
      if (bad) return reply(ctx, bad)
      n.routing = { outbounds: outbounds.map((o) => ({ tag: o.tag, type: o.type, settings: o.settings ?? {} })), routes: routes.map((r, i) => ({ priority: int(r.priority) || (i + 1) * 10, matcher: r.matcher as Json, outbound_tag: canonicalTag(r.outbound_tag), enabled: r.enabled === true, note: text(r.note) })) }
      touch(n)
      ctx.send(200, { ok: true, row_version: n.row_version })
    },
    'GET /v1/nodes/:id/identity': (ctx) => {
      if (!UUID.test(ctx.params.id!)) return reply(ctx, notFound('节点不存在'))
      const n = findNode(ctx.params.id)
      if (!n) return reply(ctx, notFound('节点不存在'))
      // 照 Go NodeIdentity：没绑服务器回空视图（无身份、0 个令牌），身份与令牌都是服务器级的
      ctx.send(200, identityView(serverById(n.server_id)))
    },
  },
}

// ---------------------------------------------------------------------------
// 仪表盘 / 总览：节点统计（对照 node/repo/report_reads.go）
// ---------------------------------------------------------------------------

const nodeDisplayName = (n: Node) => n.display_name || n.name

/** 照 Go ReportNodeCounts：未删除且在役节点数；online 为心跳未超时的在役节点 */
export function reportNodeCounts(): { total: number; online: number } {
  const active = store.filter((n) => !n.deleted && n.serving_status === 'active')
  const online = active.filter((n) => !heartbeatStale(hbOf(n))).length
  return { total: active.length, online }
}

/** 照 Go OfflineNodes：在役且心跳超时，按心跳升序（无心跳排最后）、同序按 id */
export function offlineNodes(limit: number): {
  count: number
  sample: Array<{ id: string; name: string }>
  longest_offline_seconds: number | null
} {
  const rows = store
    .filter((n) => !n.deleted && n.serving_status === 'active' && heartbeatStale(hbOf(n)))
    .sort((a, b) => {
      const ha = hbOf(a)
      const hb = hbOf(b)
      if (!ha && !hb) return a.id.localeCompare(b.id)
      if (!ha) return 1
      if (!hb) return -1
      const cmp = ha.localeCompare(hb)
      return cmp !== 0 ? cmp : a.id.localeCompare(b.id)
    })
  let longest: number | null = null
  const withHb = rows.map((n) => hbOf(n)).filter((t): t is string => t !== null)
  if (withHb.length > 0) {
    const earliest = withHb.reduce((min, t) => (t < min ? t : min))
    longest = Math.floor((Date.now() - new Date(earliest).getTime()) / 1000)
  }
  return {
    count: rows.length,
    sample: rows.slice(0, limit).map((n) => ({ id: n.id, name: nodeDisplayName(n) })),
    longest_offline_seconds: rows.length === 0 ? null : longest,
  }
}

/** 照 Go CertAttentionNodes：需关注的证书汇总（未删除） */
export function certAttentionSummary(limit: number): {
  count: number
  failing: number
  expiring: number
  sample: Array<{ id: string; name: string }>
} {
  const rows = store.filter((n) => !n.deleted && certAttention(n))
  const failing = rows.filter((n) => n.cert!.error_code !== null).length
  const expiring = rows.filter((n) => certExpiring(n)).length
  const sorted = [...rows].sort((a, b) => {
    const na = a.cert!.not_after
    const nb = b.cert!.not_after
    if (na === null && nb === null) return a.id.localeCompare(b.id)
    if (na === null) return -1
    if (nb === null) return 1
    const cmp = na.localeCompare(nb)
    return cmp !== 0 ? cmp : a.id.localeCompare(b.id)
  })
  return {
    count: rows.length,
    failing,
    expiring,
    sample: sorted.slice(0, limit).map((n) => ({ id: n.id, name: nodeDisplayName(n) })),
  }
}

/** 未删除且 24h 流量 > 0，按 traffic_bytes_24h 降序 */
export function nodeTrafficRows(): Array<{ node_id: string; name: string; display_name: string | null; bytes_24h: number }> {
  return store
    .filter((n) => !n.deleted && n.traffic_bytes_24h > 0)
    .sort((a, b) => b.traffic_bytes_24h - a.traffic_bytes_24h)
    .map((n) => ({
      node_id: n.id,
      name: n.name,
      display_name: n.display_name,
      bytes_24h: n.traffic_bytes_24h,
    }))
}
