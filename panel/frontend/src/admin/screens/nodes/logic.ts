import { relativeTime } from '../../../core/format'
import type { MetricPoint, NodeDetail, NodeRow, ProtocolSchema, Route, RoutingSource, ServingStatus } from './schemas'

export type Tone = 'ok' | 'warn' | 'danger' | 'info' | 'neutral'

// ---------------------------------------------------------------------------
// 状态：设计只有「在线 / 离线 / 已停用 / 已退役」，后端是 serving_status + stale（契约映射）
// ---------------------------------------------------------------------------
export type NodeFilter = 'all' | 'online' | 'offline' | 'disabled' | 'retired'

export interface NodeState {
  filter: Exclude<NodeFilter, 'all'>
  label: string
  tone: Tone
  /** 状态旁的小字：排空中 / 草稿 */
  note: string | null
}

export function nodeState(n: Pick<NodeRow, 'serving_status' | 'stale'>): NodeState {
  switch (n.serving_status) {
    case 'active':
      return n.stale ? { filter: 'offline', label: '离线', tone: 'danger', note: null } : { filter: 'online', label: '在线', tone: 'ok', note: null }
    case 'draining':
      return { filter: 'online', label: '在线', tone: 'ok', note: '排空中' }
    case 'draft':
      return { filter: 'disabled', label: '已停用', tone: 'neutral', note: '草稿' }
    case 'disabled':
      return { filter: 'disabled', label: '已停用', tone: 'neutral', note: null }
    case 'retired':
      return { filter: 'retired', label: '已退役', tone: 'neutral', note: null }
  }
}

/**
 * 前端筛选与搜索：节点总数不超过一页（1000）时在本地做；超过时同一组条件交给服务端
 * （GET v1/nodes 的 state 与 q，nodefabric.adminNodeFilterSQL 与这里同一映射、同一组字段）。
 * 「全部」不含已退役；搜名称、展示名、服务器、国家、协议、地址、编号
 */
export function filterNodes(rows: readonly NodeRow[], filter: NodeFilter, query: string): NodeRow[] {
  const q = query.trim().toLowerCase()
  return rows.filter((n) => {
    const state = nodeState(n).filter
    if (filter === 'all' ? state === 'retired' : state !== filter) return false
    if (!q) return true
    const hay = [n.name, n.display_name, n.server_name, n.country_code, n.node_type, n.server_host, String(n.node_no)].filter(Boolean).join(' ').toLowerCase()
    return hay.includes(q)
  })
}

/**
 * 列表一行显示用到的字段：NodeLine 只拿这些，memo 也只比这些——心跳时刻、版本号等
 * 不在行上显示的字段变了，不重渲染这一行。行上新显示一个字段就加进这里（类型会逼着加）
 */
export const NODE_LINE_FIELDS = [
  'id',
  'name',
  'country_code',
  'server_host',
  'server_port',
  'node_type',
  'server_name',
  'online_users',
  'online_ips',
  'cpu_percent',
  'traffic_bytes_24h',
  'serving_status',
  'stale',
  'delivered_to_users',
  'delivery_note',
] as const satisfies ReadonlyArray<keyof NodeRow>
export type NodeLineData = Pick<NodeRow, (typeof NODE_LINE_FIELDS)[number]>

export const sameNodeLine = (a: NodeLineData, b: NodeLineData): boolean => a === b || NODE_LINE_FIELDS.every((k) => Object.is(a[k], b[k]))

// ---------------------------------------------------------------------------
// 文案
// ---------------------------------------------------------------------------
/** 心跳：从未 / 刚刚 / 3 分钟前 / 09-23 */
export function heartbeatLabel(at: string | null, now?: Date): string {
  if (!at) return '从未'
  const rel = relativeTime(at, now)
  return /分钟|小时/.test(rel) ? `${rel}前` : rel
}

export const addressLabel = (n: Pick<NodeRow, 'server_host' | 'server_port'>) => (n.server_host ? `${n.server_host}${n.server_port ? `:${n.server_port}` : ''}` : '未设置地址')

/** 协议显示名：node_type 小写存储，界面按惯用写法 */
const PROTOCOL_LABELS: Readonly<Record<string, string>> = {
  vless: 'VLESS',
  vmess: 'VMess',
  trojan: 'Trojan',
  shadowsocks: 'Shadowsocks',
  hysteria2: 'Hysteria2',
  tuic: 'TUIC',
  juicity: 'Juicity',
  naive: 'Naive',
  anytls: 'AnyTLS',
  shadowtls: 'ShadowTLS',
  mieru: 'Mieru',
  socks: 'SOCKS',
  http: 'HTTP',
  v2ray: 'V2Ray（旧）',
  hysteria: 'Hysteria（旧）',
}
export const protocolLabel = (t: string | null) => (t ? (PROTOCOL_LABELS[t] ?? t) : '未设置协议')

// ---------------------------------------------------------------------------
// 迁移（保留规则 5）：只能迁移从未部署过的草稿节点
// ---------------------------------------------------------------------------
export function canMove(n: Pick<NodeRow, 'serving_status' | 'last_heartbeat_at' | 'identity_serial'>): boolean {
  return (n.serving_status === 'draft' || n.serving_status === 'disabled') && n.last_heartbeat_at === null && n.identity_serial === null
}
/**
 * R108「上线」：生命周期停在接入尾段（attesting 至 canary，00005 的合法边）的节点，
 * 由后端一步推到 active 并把服务器置 ready；接入流程本身只推到 attesting，此外没有别的路径
 */
export const ACTIVATABLE_LIFECYCLES = ['attesting', 'installing', 'validating', 'standby', 'canary'] as const
export const canActivate = (n: Pick<NodeRow, 'status'>) => (ACTIVATABLE_LIFECYCLES as readonly string[]).includes(n.status)

/**
 * R113 上线响应的 warnings → 带下一步的提示（Toast 用普通语气，不是错误）。首次搭建时池还没绑套餐是正常流程：
 * 先上线节点，再去套餐页发布绑了这个池的版本。只认 nodefabric.activationWarnings 的两条原文，其余原样接在后面
 */
export function activationHint(warning: string): string {
  if (warning === '所在节点池没有绑定任何套餐，暂时不服务任何用户') return '已上线。所在节点池还没绑定套餐，暂时不服务用户——下一步到「套餐」页，在套餐版本里选上这个节点池并发布。'
  if (warning === '未划入节点池，不服务任何用户') return '已上线。节点还没划入节点池，不服务任何用户——下一步在「协议参数」里给它选一个资源池。'
  return `已上线。${warning}`
}

export const MOVE_BLOCKED_HINT = '只能迁移从未部署过的草稿节点。已部署节点请用「复制节点」选目标服务器，再退役原节点。'

/** 后端 409 的 fields 逐项计数，只列非 0 的（契约 move 条目） */
const MOVE_BLOCKERS: Readonly<Record<string, string>> = {
  control_node: '控制节点',
  active_identities: '有效身份',
  runtime_instances: '运行实例',
  metrics: '探针数据',
  pending_tasks: '在途任务',
  provisioning: '部署记录',
  bootstrap_tokens: '安装令牌',
  config_applications: '配置下发记录',
  usage_sources: '用量来源',
  usage_batches: '用量批次',
  traffic_reports: '流量上报',
  active_server_token: '服务端令牌',
}
export function moveBlockers(fields: Readonly<Record<string, string>>): string[] {
  return Object.entries(fields)
    .filter(([k, v]) => k in MOVE_BLOCKERS && v !== '0' && v !== '')
    .map(([k, v]) => `${MOVE_BLOCKERS[k]} ${v}`)
}

// ---------------------------------------------------------------------------
// 服务状态转换（契约 status:batch 的合法边；retired 只走 retire 接口）
// ---------------------------------------------------------------------------
const EDGES: Readonly<Record<ServingStatus, readonly ServingStatus[]>> = {
  draft: ['active', 'disabled', 'retired'],
  active: ['draining', 'disabled'],
  draining: ['active', 'disabled', 'retired'],
  disabled: ['draft', 'active', 'retired'],
  retired: [],
}
export const canTransition = (from: ServingStatus, to: ServingStatus) => EDGES[from].includes(to)

/** 批量启用 / 停用：整批有一条非法后端就整批 409，所以只提交能转的，其余计数告诉管理员 */
export function batchPlan(rows: readonly NodeRow[], to: 'active' | 'disabled'): { items: Array<{ id: string; row_version: number }>; skipped: number } {
  const ok = rows.filter((n) => n.serving_status !== to && canTransition(n.serving_status, to))
  return { items: ok.map((n) => ({ id: n.id, row_version: n.row_version })), skipped: rows.length - ok.length }
}

// ---------------------------------------------------------------------------
// 排序：本地上下移，完成时按新顺序给 10、20、30… 只提交变了的（每项 row_version +1，变得少冲突也少）
// ---------------------------------------------------------------------------
export function moveItem<T>(list: readonly T[], index: number, delta: -1 | 1): T[] {
  const j = index + delta
  if (j < 0 || j >= list.length) return [...list]
  const next = [...list]
  ;[next[index], next[j]] = [next[j]!, next[index]!]
  return next
}
export function orderItems(ordered: readonly Pick<NodeRow, 'id' | 'row_version' | 'sort_order'>[]): Array<{ id: string; row_version: number; sort_order: number }> {
  return ordered.map((n, i) => ({ id: n.id, row_version: n.row_version, sort_order: (i + 1) * 10 })).filter((item, i) => item.sort_order !== ordered[i]!.sort_order)
}

// ---------------------------------------------------------------------------
// 协议表单：字段来自 GET v1/node-protocol-schemas
// ---------------------------------------------------------------------------
export type FieldKind = 'enum' | 'number' | 'boolean' | 'json' | 'list' | 'text'
export interface ProtocolField {
  /** 点号路径，如 reality_settings.private_key */
  path: string
  kind: FieldKind
  options: string[]
  required: boolean
  sensitive: boolean
  /** 后端 schema 给的字段说明（hints），没有则 undefined */
  hint?: string
}

export const isStable = (s: ProtocolSchema) => s.status === 'stable'

/** 读接口按名字在任意深度抹掉的键（nodefabric.sensitiveProtocolKey，R107 补了 mask_password）：这些字段永远读不回来，只能写 */
const REDACTED_KEYS = new Set(['password', 'passwd', 'secret', 'token', 'private_key', 'private-key', 'psk', 'obfs_password', 'obfs-password', 'mask_password'])

/** 敏感判断按整条路径或叶子名：schema 给的是 private_key，字段路径是 reality_settings.private_key */
function sensitiveOf(s: ProtocolSchema, path: string): boolean {
  const leaf = path.split('.').pop()!
  return REDACTED_KEYS.has(leaf.toLowerCase()) || (s.sensitive_properties ?? []).some((p) => p === path || p === leaf)
}

export function protocolFields(s: ProtocolSchema): ProtocolField[] {
  const fields = s.allowed_properties.map((path): ProtocolField => {
    const type = s.property_types?.[path]
    // methods 是 Shadowsocks 加密方式（字段名 cipher）的可选值
    // 枚举按整条路径找，找不到再按叶子名：后端把 network_settings.mode 的可选值登记在 enums.mode 下
    const leaf = path.split('.').pop()!
    const options = s.enums?.[path] ?? (path.includes('.') ? s.enums?.[leaf] : undefined) ?? (s.methods && (path === 'cipher' || path === 'method') ? s.methods : [])
    const kind: FieldKind = options.length ? 'enum' : type === 'number' ? 'number' : type === 'boolean' ? 'boolean' : type === 'json' ? 'json' : type === 'list' ? 'list' : 'text'
    const hint = s.hints?.[path]
    return { path, kind, options, required: s.required.includes(path), sensitive: sensitiveOf(s, path), ...(hint ? { hint } : {}) }
  })
  // 必填在前，其余保持后端顺序
  return [...fields.filter((f) => f.required), ...fields.filter((f) => !f.required)]
}

export type FormValues = Record<string, string>

function pick(obj: unknown, path: string): unknown {
  return path.split('.').reduce<unknown>((cur, key) => (cur && typeof cur === 'object' ? (cur as Record<string, unknown>)[key] : undefined), obj)
}

/** 已存配置 → 表单字符串；敏感字段读接口已抹掉，一律空 */
export function toFormValues(fields: readonly ProtocolField[], config: unknown): FormValues {
  const out: FormValues = {}
  for (const f of fields) {
    const v = pick(config, f.path)
    if (v === undefined || v === null || f.sensitive) out[f.path] = ''
    else if (f.kind === 'list' && Array.isArray(v)) out[f.path] = v.map(String).join(', ')
    else if (f.kind === 'json' || (typeof v === 'object' && v !== null)) out[f.path] = JSON.stringify(v, null, 2)
    else out[f.path] = String(v)
  }
  return out
}

/**
 * 编辑时敏感字段的口径（R106 / R107）：PATCH 的 protocol_config 里**缺席**的敏感键后端按原路径补回，
 * 所以留空 = 不改（不带这个键，必填的也不算缺）；要清空选填的敏感键，显式传 null（cleared 里的路径）。
 * keepSecrets 只在编辑同一种协议时为真——换协议后端不补旧密钥，必填照常要填。
 * mask_password 跟着开关 mask 走，关掉掩码时它本来就留空不带，无需特别处理
 */
export interface SecretOptions {
  keepSecrets?: boolean
  cleared?: ReadonlySet<string>
}

/** 编辑时可以「清空」的敏感字段：选填的才行（必填的清空会被后端 422） */
export const clearableSecret = (f: ProtocolField) => f.sensitive && !f.required

/** 表单 → protocol_config（点号路径展开为嵌套对象）；空串不写入；cleared 的敏感字段写 null；错误键为字段路径 */
export function toProtocolConfig(
  fields: readonly ProtocolField[],
  values: FormValues,
  types?: ProtocolSchema['property_types'],
  secrets: SecretOptions = {},
): { config: Record<string, unknown>; errors: Record<string, string> } {
  const config: Record<string, unknown> = {}
  const errors: Record<string, string> = {}
  for (const f of fields) {
    const raw = (values[f.path] ?? '').trim()
    const cleared = f.sensitive && raw === '' && secrets.cleared?.has(f.path) === true
    if (raw === '' && !cleared) {
      if (f.required && !(f.sensitive && secrets.keepSecrets)) errors[f.path] = '必填'
      continue
    }
    let value: unknown = cleared ? null : raw
    const numeric = f.kind === 'number' || types?.[f.path] === 'number'
    if (cleared) {
      // 显式 null：后端以请求为准清空（R106）
    } else if (f.kind === 'boolean') value = raw === 'true'
    else if (f.kind === 'list') {
      // 逗号或空白分隔；一个值存字符串（与 xboard 同形），多个存数组
      const items = splitList(raw)
      value = items.length === 1 ? items[0] : items
    } else if (numeric) {
      value = Number(raw)
      if (!Number.isFinite(value)) errors[f.path] = '填数字'
    } else if (f.kind === 'json') {
      try {
        value = JSON.parse(raw)
      } catch {
        errors[f.path] = '不是合法的 JSON'
      }
    }
    const keys = f.path.split('.')
    let cur = config
    keys.slice(0, -1).forEach((k) => {
      cur = (cur[k] ??= {}) as Record<string, unknown>
    })
    cur[keys[keys.length - 1]!] = value
  }
  return { config, errors }
}

/** list 字段的录入：逗号、中文逗号或空白分隔，去掉空项 */
export const splitList = (raw: string): string[] => raw.split(/[\s,，]+/).filter(Boolean)

/** 后端 422 的 fields 键形如 protocol_config.<字段>（内核名可能与表单路径不同），按路径再按叶子名落到表单项 */
export function mapProtocolErrors(fields: readonly ProtocolField[], errors: Readonly<Record<string, string>>): { byField: Record<string, string>; rest: Record<string, string> } {
  const byField: Record<string, string> = {}
  const rest: Record<string, string> = {}
  for (const [key, msg] of Object.entries(errors)) {
    const bare = key.replace(/^protocol_config\./, '')
    const hit = fields.find((f) => f.path === bare) ?? fields.find((f) => f.path.split('.').pop() === bare.split('.').pop())
    if (hit && key.startsWith('protocol_config')) byField[hit.path] = msg
    else rest[key] = msg
  }
  return { byField, rest }
}

/** REALITY：只在 vless 且 tls=2 时可用（契约 reality-keypair 条目） */
export const realityEnabled = (nodeType: string, values: FormValues) => nodeType === 'vless' && values.tls === '2'
export const REALITY_KEYS = { private_key: 'reality_settings.private_key', public_key: 'reality_settings.public_key', short_id: 'reality_settings.short_id' } as const

/** VLESS 的 Vision 流控：只有 REALITY + tcp 能用（后端 validateVLESSFlow） */
export const VISION_FLOW = 'xtls-rprx-vision'
const visionEligible = (nodeType: string, v: FormValues) => nodeType === 'vless' && v.tls === '2' && (v.network || 'tcp') === 'tcp'

/**
 * 改了一个协议字段之后的联动默认值。只在 tls / network 变化时动 flow，管理员手动清空的 flow 不会被填回去：
 * - 进入 VLESS + REALITY + tcp 且 flow 为空：默认 xtls-rprx-vision（TLS-in-TLS 特征的缓解，主流客户端都支持）；
 * - 离开这个组合且 flow 是 Vision 系：清空，否则后端 422（Vision 挂在 grpc / ws 上客户端直接报错）
 */
export function withProtocolDefaults(nodeType: string, changedPath: string, next: FormValues): FormValues {
  if (nodeType !== 'vless' || (changedPath !== 'tls' && changedPath !== 'network')) return next
  const flow = next.flow ?? ''
  if (visionEligible(nodeType, next)) return flow ? next : { ...next, flow: VISION_FLOW }
  return flow.startsWith(VISION_FLOW) ? { ...next, flow: '' } : next
}

/** 不加密时仍允许的传输：走 HTTP，可以挂 CDN（后端 validatePlaintextStream） */
const CDN_NETWORKS = new Set(['ws', 'httpupgrade', 'grpc', 'xhttp'])

/** 协议表单顶部的提示（不是错误，错误以后端 422 为准） */
export function protocolNotices(nodeType: string, v: FormValues): string[] {
  if (nodeType !== 'vless' && nodeType !== 'vmess') return []
  const plain = (v.tls ?? '') === '' || v.tls === '0'
  if (!plain) return []
  const network = v.network || 'tcp'
  if (network === 'tcp') return ['不加密的裸 tcp 就是明文代理，保存会被拒绝：VLESS 请选 2 · REALITY，或换 ws / httpupgrade / grpc / xhttp 并套 CDN']
  if (CDN_NETWORKS.has(network)) return ['不加密：需要套 CDN 或 TLS 反代后再给用户用，否则 UUID 和流量在线路上明文可见']
  return []
}

/** tls 三态下拉的文字（xboard 口径：0 不加密、1 TLS、2 REALITY） */
export function optionLabel(path: string, value: string): string {
  if (path === 'tls') return value === '0' ? '0 · 不加密' : value === '1' ? '1 · TLS' : value === '2' ? '2 · REALITY' : value
  return value
}

// ---------------------------------------------------------------------------
// 基本信息：新建体与 PATCH 差量
// ---------------------------------------------------------------------------
export interface BasicForm {
  name: string
  displayName: string
  serverId: string
  poolId: string
  nodeType: string
  host: string
  port: string
  kernel: string
  rate: string
  country: string
}

/**
 * 内核（nodefabric.validateKernelChoice 接受的值）。节点端只有 NativeCore：sing-box / xray-core
 * 已不再接受新写入，存量值后端下发时按 auto（EffectiveKernel），表单载入时也显示为自动
 */
export const KERNELS = [
  ['auto', '自动'],
  ['pandora-native', 'Pandora Native'],
] as const

const LEGACY_KERNELS = new Set(['sing-box', 'xray-core'])
const effectiveKernel = (k: string | null | undefined) => (!k || LEGACY_KERNELS.has(k) ? 'auto' : k)

/** 存量节点还留着已停用的内核值时给一句说明，否则 undefined */
export function legacyKernelNote(row: Pick<NodeDetail, 'kernel'> | null): string | undefined {
  return row && LEGACY_KERNELS.has(row.kernel) ? `原值 ${row.kernel} 已不再生效（节点端只有 NativeCore），下次保存协议改动时改为自动` : undefined
}

/** 资源池必填的说明：下拉的提示与校验文案同一句 */
export const NO_POOL_HINT = '选择资源池：不在任何资源池里的节点不服务任何用户'

export const emptyBasic = (): BasicForm => ({ name: '', displayName: '', serverId: '', poolId: '', nodeType: '', host: '', port: '', kernel: 'auto', rate: '1', country: '' })

export function basicFromRow(n: NodeDetail): BasicForm {
  return {
    name: n.name,
    displayName: n.display_name ?? '',
    serverId: n.server_id ?? '',
    poolId: n.pool_id ?? '',
    nodeType: n.node_type ?? '',
    host: n.server_host ?? '',
    port: n.server_port ? String(n.server_port) : '',
    kernel: effectiveKernel(n.kernel),
    rate: String(n.traffic_rate),
    country: n.country_code ?? '',
  }
}

export function validateBasic(b: BasicForm, creating: boolean): Record<string, string> {
  const e: Record<string, string> = {}
  if (!b.name.trim() || [...b.name.trim()].length > 120) e.name = '名称必填，不超过 120 字'
  if (creating && !b.serverId) e.server_id = '选择承载的服务器'
  // 没划进节点池的节点不服务任何用户（R104）；后端只拒绝清空在役节点的池，表单一律要求选
  if (!b.poolId) e.pool_id = NO_POOL_HINT
  if (!b.nodeType) e.node_type = '选择协议'
  if (!b.host.trim()) e.server_host = '填 IP 或主机名'
  const port = Number(b.port)
  if (!Number.isInteger(port) || port < 1 || port > 65535) e.server_port = '端口 1–65535'
  const rate = Number(b.rate)
  if (!(rate > 0)) e.traffic_rate = '倍率必须大于 0'
  if (b.country && !/^[A-Za-z]{2}$/.test(b.country.trim())) e.country_code = '两位字母国家代码，如 HK'
  return e
}

export function createBody(b: BasicForm, config: Record<string, unknown>): Record<string, unknown> {
  return {
    name: b.name.trim(),
    server_id: b.serverId,
    node_type: b.nodeType,
    server_host: b.host.trim(),
    server_port: Number(b.port),
    protocol_config: config,
    kernel: b.kernel || 'auto',
    traffic_rate: Number(b.rate),
    ...(b.poolId ? { pool_id: b.poolId } : {}),
    ...(b.displayName.trim() ? { display_name: b.displayName.trim() } : {}),
    ...(b.country.trim() ? { country_code: b.country.trim().toUpperCase() } : {}),
  }
}

/**
 * PATCH 差量：省略 = 不改。protocol_config 只在协议字段真的改了（或换了协议）才带：
 * 它是整体替换，普通键缺席就是删除；敏感键缺席由后端补回（R106），所以没动的敏感字段不带键即可。
 */
export function patchBody(row: NodeDetail, b: BasicForm, protocol: { changed: boolean; config: Record<string, unknown> }): Record<string, unknown> {
  const before = basicFromRow(row)
  const body: Record<string, unknown> = { row_version: row.row_version }
  if (b.name.trim() !== before.name) body.name = b.name.trim()
  if (b.displayName.trim() !== before.displayName) body.display_name = b.displayName.trim()
  if (b.poolId !== before.poolId) body.pool_id = b.poolId || null
  if (b.nodeType !== before.nodeType) body.node_type = b.nodeType
  if (b.host.trim() !== before.host) body.server_host = b.host.trim()
  if (b.port !== before.port) body.server_port = Number(b.port)
  if (b.kernel !== before.kernel) body.kernel = b.kernel
  if (Number(b.rate) !== row.traffic_rate) body.traffic_rate = Number(b.rate)
  if (b.country.trim().toUpperCase() !== before.country) body.country_code = b.country.trim() ? b.country.trim().toUpperCase() : null
  if (protocol.changed || b.nodeType !== before.nodeType) body.protocol_config = protocol.config
  // 存量的 sing-box / xray-core：只要这次会重新校验协议（改了协议、地址、端口），就把内核一起换成表单值，
  // 否则后端拿库里的旧值校验会 422。只改名字之类不碰协议的保存不带它，库里的值原样留着（读时忽略）
  const touchesProtocol = ['node_type', 'server_host', 'server_port', 'protocol_config'].some((k) => k in body)
  if (LEGACY_KERNELS.has(row.kernel) && touchesProtocol) body.kernel = b.kernel
  return body
}

/** 协议是否改动：非敏感字段与初值不同，或填了任何敏感字段，或要清空某个敏感字段 */
export function protocolChanged(fields: readonly ProtocolField[], initial: FormValues, current: FormValues, cleared: ReadonlySet<string> = new Set()): boolean {
  return fields.some((f) => (f.sensitive ? (current[f.path] ?? '') !== '' || cleared.has(f.path) : (current[f.path] ?? '') !== (initial[f.path] ?? '')))
}

/** 这次保存会显式清空的敏感字段（留空且点了「清空」的），保存前要确认 */
export const clearedSecrets = (fields: readonly ProtocolField[], current: FormValues, cleared: ReadonlySet<string>) =>
  fields.filter((f) => clearableSecret(f) && cleared.has(f.path) && !(current[f.path] ?? '').trim()).map((f) => f.path)

// ---------------------------------------------------------------------------
// 路由规则行（D-D-1：下拉只放后端支持的匹配类型）
// ---------------------------------------------------------------------------
export const MATCH_KINDS = [
  ['domain', '域名'],
  ['domain_suffix', '域名后缀'],
  ['ip_cidr', 'IP 段'],
  ['port', '端口'],
  ['network', '网络（tcp / udp）'],
  ['source', '来源 IP 段'],
  ['source_port', '来源端口'],
  ['fallback', '兜底（全部）'],
] as const
export type MatchKind = (typeof MATCH_KINDS)[number][0]

/** matcher 的别名键归一到下拉值（契约列出的全部写法） */
const ALIASES: Readonly<Record<string, MatchKind>> = {
  domain: 'domain',
  domains: 'domain',
  domain_suffix: 'domain_suffix',
  domain_suffixes: 'domain_suffix',
  ip: 'ip_cidr',
  ip_cidr: 'ip_cidr',
  ip_cidrs: 'ip_cidr',
  port: 'port',
  ports: 'port',
  network: 'network',
  networks: 'network',
  source: 'source',
  source_ip_cidr: 'source',
  source_cidrs: 'source',
  source_port: 'source_port',
  source_ports: 'source_port',
}

export interface RuleRow {
  kind: MatchKind
  value: string
  outbound: string
  enabled: boolean
  note: string
}

export function routeToRow(r: Route): RuleRow {
  const entries = Object.entries(r.matcher)
  const base = { outbound: r.outbound_tag, enabled: r.enabled, note: r.note }
  if (entries.length === 0) return { ...base, kind: 'fallback', value: '' }
  const [key, raw] = entries[0]!
  const values = Array.isArray(raw) ? raw : [raw]
  return { ...base, kind: ALIASES[key] ?? 'domain', value: values.map(String).join(', ') }
}

/** 规则行 → PUT 的 routes；优先级按序号 ×10；错误以序号为键 */
export function rowsToRoutes(rows: readonly RuleRow[]): { routes: Array<Record<string, unknown>>; errors: Record<number, string> } {
  const errors: Record<number, string> = {}
  const routes = rows.map((r, i) => {
    let matcher: Record<string, unknown> = {}
    if (r.kind !== 'fallback') {
      const parts = r.value
        .split(/[,，\s]+/)
        .map((s) => s.trim())
        .filter(Boolean)
      if (!parts.length) errors[i] = '填匹配值'
      const numeric = r.kind === 'port' || r.kind === 'source_port'
      if (numeric && parts.some((p) => !/^\d+(-\d+)?$/.test(p))) errors[i] = '端口填数字或范围，如 443、8000-9000'
      matcher = { [r.kind]: numeric ? parts.map((p) => (/^\d+$/.test(p) ? Number(p) : p)) : parts }
    }
    if (!r.outbound) errors[i] ??= '选择出站'
    return { priority: (i + 1) * 10, matcher, outbound_tag: r.outbound, enabled: r.enabled, note: r.note.trim() }
  })
  const lastEnabled = rows.map((r, i) => (r.enabled ? i : -1)).filter((i) => i >= 0)
  rows.forEach((r, i) => {
    if (r.enabled && r.kind === 'fallback' && i !== lastEnabled[lastEnabled.length - 1]) errors[i] ??= '兜底规则必须是最后一条启用的规则'
  })
  return { routes, errors }
}

/** 加一条规则：末尾是兜底时插到兜底之前（设计稿「规则已添加（兜底规则之前）」），否则追加 */
export function insertRule(rows: readonly RuleRow[], row: RuleRow): RuleRow[] {
  const last = rows[rows.length - 1]
  return last?.kind === 'fallback' ? [...rows.slice(0, -1), row, last] : [...rows, row]
}

// ---------------------------------------------------------------------------
// 出站行（单节点私有出站与全局出站共用；校验与 Go validateRoutingPayload 一致）
// ---------------------------------------------------------------------------
/** 可选的出站类型：node_outbounds.type 的 CHECK 去掉内置的 direct / block（它们是固定的两个 tag） */
export const OUTBOUND_TYPES = ['socks', 'http', 'shadowsocks', 'vmess', 'vless', 'trojan', 'hysteria', 'hysteria2', 'tuic', 'anytls', 'shadowtls', 'wireguard'] as const

export interface OutboundRow {
  tag: string
  type: string
  /** settings 的 JSON 文本 */
  settings: string
}

export const outboundToRow = (o: { tag: string; type: string; settings: unknown }): OutboundRow => ({ tag: o.tag, type: o.type, settings: JSON.stringify(o.settings ?? {}, null, 2) })

/** 出站行 → PUT 的 outbounds；tag 1–64 字、不占 direct / block、大小写不敏感不重复，settings 必须是 JSON 对象；错误以行号为键 */
export function rowsToOutbounds(rows: readonly OutboundRow[]): { outbounds: Array<{ tag: string; type: string; settings: unknown }>; errors: Record<number, string> } {
  const errors: Record<number, string> = {}
  const seen = new Set<string>()
  const outbounds = rows.map((o, i) => {
    const tag = o.tag.trim()
    const key = tag.toLowerCase()
    if (!tag || [...tag].length > 64) errors[i] = '标签 1–64 字'
    else if (key === 'direct' || key === 'block') errors[i] = '不能叫 direct 或 block'
    else if (seen.has(key)) errors[i] = '标签重复'
    seen.add(key)
    let settings: unknown = {}
    try {
      settings = o.settings.trim() ? JSON.parse(o.settings) : {}
    } catch {
      errors[i] ??= 'settings 不是合法的 JSON'
    }
    if (!settings || typeof settings !== 'object' || Array.isArray(settings)) errors[i] ??= 'settings 必须是 JSON 对象'
    return { tag, type: o.type, settings }
  })
  return { outbounds, errors }
}

/**
 * 指向某个出站的规则条数：删出站前先拦，免得保存时才 422。引用按 tag 原样精确比较，
 * 与后端 checkRouteRefs、下发和 pdnd 查表一致（出站 tag 存库前已去空白）
 */
export const rulesUsing = (rows: readonly RuleRow[], tag: string) => rows.filter((r) => r.outbound === tag.trim()).length

/** 出站改名时，把指向旧名的规则一起改过去（原样精确匹配） */
export function renameOutbound(rows: readonly RuleRow[], from: string, to: string): RuleRow[] {
  const key = from.trim()
  if (!key || key === to.trim()) return [...rows]
  return rows.map((r) => (r.outbound === key ? { ...r, outbound: to.trim() } : r))
}

/** 规则的简短文字（抽屉只读列表） */
export function ruleSummary(r: Route): string {
  const row = routeToRow(r)
  return row.kind === 'fallback' ? '兜底（全部）' : `${MATCH_KINDS.find(([k]) => k === row.kind)?.[1]} ${row.value}`
}

// ---------------------------------------------------------------------------
// 路由组（00096）：元信息表单、成员比较、可引用出站、生效来源文案
// ---------------------------------------------------------------------------
export interface GroupForm {
  name: string
  description: string
  /** 输入框里的文本，提交时转整数 */
  sortOrder: string
}

export const groupFormFrom = (g?: { name: string; description: string; sort_order: number }): GroupForm => ({ name: g?.name ?? '', description: g?.description ?? '', sortOrder: String(g?.sort_order ?? 0) })

/** 与 nodefabric.normalizeRouteGroupFields 同口径：名称去空白后 1–64 字、说明 ≤ 500 字、排序为 ±1000000 内的整数 */
export function groupFormErrors(f: GroupForm): Record<string, string> {
  const errors: Record<string, string> = {}
  const n = [...f.name.trim()].length
  if (n < 1 || n > 64) errors.name = '名称为 1 到 64 个字符'
  if ([...f.description.trim()].length > 500) errors.description = '说明最多 500 个字符'
  const order = Number(f.sortOrder.trim())
  if (!f.sortOrder.trim() || !Number.isInteger(order) || Math.abs(order) > 1_000_000) errors.sort_order = '排序是 -1000000 到 1000000 的整数'
  return errors
}

/** 新建体：三个字段都带 */
export const groupCreateBody = (f: GroupForm) => ({ name: f.name.trim(), description: f.description.trim(), sort_order: Number(f.sortOrder.trim()) })

/** PATCH 差量：只带改了的字段（后端 nil = 不改）；没改任何东西返回 null */
export function groupPatchBody(g: { name: string; description: string; sort_order: number; row_version: number }, f: GroupForm): Record<string, unknown> | null {
  const next = groupCreateBody(f)
  const body: Record<string, unknown> = {}
  if (next.name !== g.name) body.name = next.name
  if (next.description !== g.description) body.description = next.description
  if (next.sort_order !== g.sort_order) body.sort_order = next.sort_order
  return Object.keys(body).length ? { row_version: g.row_version, ...body } : null
}

/** 两组 id 是否同一集合（顺序无关）：成员没变就不发请求 */
export const sameIds = (a: readonly string[], b: readonly string[]) => a.length === b.length && [...a].sort().join() === [...b].sort().join()

/**
 * 规则编辑器的「其他范围」出站下拉项：按传入顺序（越具体越先）去重，按 tag 原样、先到先得——
 * 与生效合并里具体范围覆盖宽泛范围同名出站一致（合并与引用校验都区分大小写）
 */
export function referenceOutbounds(scopes: ReadonlyArray<{ label: string; tags: readonly string[] }>, exclude: readonly string[] = []): Array<[string, string]> {
  const seen = new Set(exclude.map((t) => t.trim()))
  const out: Array<[string, string]> = []
  for (const s of scopes) {
    for (const tag of s.tags) {
      if (seen.has(tag)) continue
      seen.add(tag)
      out.push([tag, `${tag}（${s.label}）`])
    }
  }
  return out
}

/** 生效预览里每条出站 / 规则的来源文字 */
export const sourceLabel = (s: RoutingSource) => (s.scope === 'node' ? '本节点' : s.scope === 'global' ? '全局' : `路由组 · ${s.group_name ?? ''}`)
export const sourceTone = (s: RoutingSource): Tone => (s.scope === 'node' ? 'info' : s.scope === 'group' ? 'ok' : 'neutral')

// ---------------------------------------------------------------------------
// 带宽：近 24 小时按整点分桶，每桶取 (rx+tx)×8/1e6 的均值（Mbps）
// ---------------------------------------------------------------------------
export function bandwidthBuckets(points: readonly MetricPoint[], now: Date = new Date()): Array<{ hour: number; mbps: number | null }> {
  const end = new Date(now)
  end.setMinutes(0, 0, 0)
  const start = end.getTime() - 23 * 3600_000
  const sums = Array.from({ length: 24 }, () => ({ total: 0, n: 0 }))
  for (const p of points) {
    const t = new Date(p.at).getTime()
    const i = Math.floor((t - start) / 3600_000)
    if (i < 0 || i > 23) continue
    sums[i]!.total += ((p.rx_speed + p.tx_speed) * 8) / 1e6
    sums[i]!.n += 1
  }
  return sums.map((s, i) => ({ hour: new Date(start + i * 3600_000).getHours(), mbps: s.n ? Number((s.total / s.n).toFixed(1)) : null }))
}
