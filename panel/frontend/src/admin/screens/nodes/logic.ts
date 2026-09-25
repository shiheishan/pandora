/**
 * [INPUT]: 依赖 ../../../core/format 的 relativeTime，依赖 ./schemas 的类型
 * [OUTPUT]: 对外提供节点页的纯函数：状态映射与筛选搜索、心跳与地址文案（流量用 core/format 的 formatBytes）、迁移资格（保留规则 5）、状态转换合法边与批量取舍、排序提交项、schema 驱动的协议表单模型（字段推导、拍平 / 还原、敏感字段、REALITY）、PATCH 差量、路由规则行与 matcher 互转、带宽分桶
 * [POS]: admin/screens/nodes 的逻辑层：映射全部取自 api-contract.md 后台-07 · 节点条目的「设计 / 映射」行与 Go 校验器，nodes.test.ts 逐条守住；组件只负责渲染与交互
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { relativeTime } from '../../../core/format'
import type { MetricPoint, NodeRow, ProtocolSchema, Route, ServingStatus } from './schemas'

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

/** 前端筛选与搜索（契约：列表无服务端筛选）；「全部」不含已退役；搜名称、展示名、服务器、国家、协议、地址、编号 */
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
export type FieldKind = 'enum' | 'number' | 'boolean' | 'json' | 'text'
export interface ProtocolField {
  /** 点号路径，如 reality_settings.private_key */
  path: string
  kind: FieldKind
  options: string[]
  required: boolean
  sensitive: boolean
}

export const isStable = (s: ProtocolSchema) => s.status === 'stable'

/** 读接口按名字在任意深度抹掉的键（nodefabric.sensitiveProtocolKey）：这些字段永远读不回来，只能写 */
const REDACTED_KEYS = new Set(['password', 'passwd', 'secret', 'token', 'private_key', 'private-key', 'psk', 'obfs_password', 'obfs-password'])

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
    const kind: FieldKind = options.length ? 'enum' : type === 'number' ? 'number' : type === 'boolean' ? 'boolean' : type === 'json' ? 'json' : 'text'
    return { path, kind, options, required: s.required.includes(path), sensitive: sensitiveOf(s, path) }
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
    else if (f.kind === 'json' || (typeof v === 'object' && v !== null)) out[f.path] = JSON.stringify(v, null, 2)
    else out[f.path] = String(v)
  }
  return out
}

/** 表单 → protocol_config（点号路径展开为嵌套对象）；空串不写入；错误键为字段路径 */
export function toProtocolConfig(fields: readonly ProtocolField[], values: FormValues, types?: ProtocolSchema['property_types']): { config: Record<string, unknown>; errors: Record<string, string> } {
  const config: Record<string, unknown> = {}
  const errors: Record<string, string> = {}
  for (const f of fields) {
    const raw = (values[f.path] ?? '').trim()
    if (raw === '') {
      if (f.required) errors[f.path] = '必填'
      continue
    }
    let value: unknown = raw
    const numeric = f.kind === 'number' || types?.[f.path] === 'number'
    if (f.kind === 'boolean') value = raw === 'true'
    else if (numeric) {
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

/** 内核（nodefabric.validateNewNodeProtocol 接受的值） */
export const KERNELS = [
  ['auto', '自动'],
  ['pandora-native', 'Pandora Native'],
  ['sing-box', 'sing-box'],
  ['xray-core', 'Xray-core'],
] as const

export const emptyBasic = (): BasicForm => ({ name: '', displayName: '', serverId: '', poolId: '', nodeType: '', host: '', port: '', kernel: 'auto', rate: '1', country: '' })

export function basicFromRow(n: NodeRow): BasicForm {
  return {
    name: n.name,
    displayName: n.display_name ?? '',
    serverId: n.server_id ?? '',
    poolId: n.pool_id ?? '',
    nodeType: n.node_type ?? '',
    host: n.server_host ?? '',
    port: n.server_port ? String(n.server_port) : '',
    kernel: n.kernel || 'auto',
    rate: String(n.traffic_rate),
    country: n.country_code ?? '',
  }
}

export function validateBasic(b: BasicForm, creating: boolean): Record<string, string> {
  const e: Record<string, string> = {}
  if (!b.name.trim() || [...b.name.trim()].length > 120) e.name = '名称必填，不超过 120 字'
  if (creating && !b.serverId) e.server_id = '选择承载的服务器'
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
 * PATCH 差量：省略 = 不改。protocol_config 只在协议字段真的改了（或换了协议）才带——
 * 后端整体替换已存配置，而读接口抹掉了敏感值，没改协议却回写会把私钥清掉。
 */
export function patchBody(row: NodeRow, b: BasicForm, protocol: { changed: boolean; config: Record<string, unknown> }): Record<string, unknown> {
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
  return body
}

/** 协议是否改动：非敏感字段与初值不同，或填了任何敏感字段 */
export function protocolChanged(fields: readonly ProtocolField[], initial: FormValues, current: FormValues): boolean {
  return fields.some((f) => (f.sensitive ? (current[f.path] ?? '') !== '' : (current[f.path] ?? '') !== (initial[f.path] ?? '')))
}

/** 保存会整体替换协议：这些敏感字段留空就会被清掉 */
export const blankSensitive = (fields: readonly ProtocolField[], current: FormValues) => fields.filter((f) => f.sensitive && !(current[f.path] ?? '').trim()).map((f) => f.path)

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

/** 规则的简短文字（抽屉只读列表） */
export function ruleSummary(r: Route): string {
  const row = routeToRow(r)
  return row.kind === 'fallback' ? '兜底（全部）' : `${MATCH_KINDS.find(([k]) => k === row.kind)?.[1]} ${row.value}`
}

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
