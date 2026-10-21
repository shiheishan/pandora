import { formatCount, relativeTime } from '../../../core/format'
import type { MetricPoint, NodeDetail, NodeRow, ProtocolSchema, Route, RoutingSource, ServerStatus, ServingStatus } from './schemas'

export type Tone = 'ok' | 'warn' | 'danger' | 'info' | 'neutral'

// ---------------------------------------------------------------------------
// 状态：设计只有「在线 / 离线 / 已停用 / 已退役」，后端是 serving_status + stale（契约映射）
// ---------------------------------------------------------------------------
export type NodeFilter = 'all' | 'online' | 'offline' | 'disabled' | 'retired' | 'cert'

interface NodeState {
  filter: Exclude<NodeFilter, 'all' | 'cert'>
  label: string
  tone: Tone
  /** 状态旁的小字：草稿 */
  note: string | null
}

export function nodeState(n: Pick<NodeRow, 'serving_status' | 'stale'>): NodeState {
  switch (n.serving_status) {
    case 'active':
      return n.stale ? { filter: 'offline', label: '离线', tone: 'danger', note: null } : { filter: 'online', label: '在线', tone: 'ok', note: null }
    case 'draft':
      return { filter: 'disabled', label: '已停用', tone: 'neutral', note: '草稿' }
    case 'disabled':
      return { filter: 'disabled', label: '已停用', tone: 'neutral', note: null }
    case 'retired':
      return { filter: 'retired', label: '已退役', tone: 'neutral', note: null }
  }
}

/**
 * 「在线」一格的显示。online_users 是节点在设备识别窗口内（缺省 5 分钟）见过的用户数，节点离线后会在窗口过后降到 0：
 * 在线节点照常显示人数（0 显示「—」）；离线节点人数还大于 0 时 value「—」、note「离线前 N 人在线」，降到 0 后不再提示
 */
export function onlineUsersView(n: Pick<NodeRow, 'serving_status' | 'stale' | 'online_users'>): { value: string; note: string | null } {
  const state = nodeState(n)
  const count = n.online_users ?? 0
  if (state.filter === 'offline') {
    if (count > 0) {
      return { value: '—', note: `离线前 ${formatCount(count)} 人在线` }
    }
    return { value: '—', note: null }
  }
  return {
    value: count > 0 ? formatCount(count) : '—',
    note: null,
  }
}

/**
 * 证书要处理：在役、报过证书状态，且申请失败（error_code 非空）或快到期 / 已过期（IP 证书 2 天内、其余 14 天内）。
 * 与 Go node/repo 的 CertAttentionBaseCond / CertFailingCond / CertExpiringCond 同口径
 */
export function certAttention(n: Pick<NodeRow, 'serving_status' | 'cert'>, now: number): boolean {
  if (n.serving_status !== 'active' || !n.cert) return false
  if (n.cert.error_code) return true
  if (!n.cert.not_after) return false
  const notAfterMs = new Date(n.cert.not_after).getTime()
  const id = n.cert.identifier ?? ''
  const isIP = id !== '' && (/^[0-9.]+$/.test(id) || id.includes(':'))
  const deadline = now + (isIP ? 2 : 14) * 86400_000
  return notAfterMs < deadline
}

/**
 * 前端筛选与搜索：节点总数不超过一页（1000）时在本地做；超过时同一组条件交给服务端
 * （GET v1/nodes 的 state 与 q、tag，node/repo 的 nodeListFilterSQL 与这里同一映射、同一组字段）。
 * 「全部」不含已退役；搜名称、展示名、服务器、国家、协议、地址、编号
 */
export function filterNodes(rows: readonly NodeRow[], filter: NodeFilter, query: string, tag = '', now = Date.now()): NodeRow[] {
  const q = query.trim().toLowerCase()
  return rows.filter((n) => {
    if (filter === 'cert') {
      if (!certAttention(n, now)) return false
    } else {
      const state = nodeState(n).filter
      if (filter === 'all' ? state === 'retired' : state !== filter) return false
    }
    if (tag && !n.tags.includes(tag)) return false
    if (!q) return true
    const hay = [n.name, n.display_name, n.server_name, n.country_code, n.node_type, n.server_host, String(n.node_no)].filter(Boolean).join(' ').toLowerCase()
    return hay.includes(q)
  })
}

/**
 * 列表所有行的标签去重（区分大小写，与筛选口径一致）后按中文习惯排序
 */
export function tagOptions(rows: readonly NodeRow[]): string[] {
  const set = new Set<string>()
  for (const r of rows) {
    for (const t of r.tags) {
      set.add(t)
    }
  }
  return Array.from(set).sort((a, b) => a.localeCompare(b, 'zh-Hans-CN'))
}

/**
 * 构造批量修改节点的请求体：items 带 row_version，reason 去首尾空白
 */
export function batchEditBody(rows: readonly NodeRow[], set: Record<string, unknown>, reason: string) {
  return {
    items: rows.map((n) => ({ id: n.id, row_version: n.row_version })),
    set,
    reason: reason.trim(),
  }
}

/**
 * 列表一行显示用到的字段：NodeLine 只拿这些，memo 也只比这些——心跳时刻、版本号等
 * 不在行上显示的字段变了，不重渲染这一行。行上新显示一个字段就加进这里（类型会逼着加）。
 * tags 数组和 traffic 对象靠 react-query 的结构共享，没变时引用不变，sameNodeLine 的 Object.is 照样成立
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
  'tags',
  'traffic',
  'relay_count',
  'ech_enabled',
  'cert',
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
}
export const protocolLabel = (t: string | null) => (t ? (PROTOCOL_LABELS[t] ?? t) : '未设置协议')

/** 协议卡片顺序（新建向导与下拉） */
export const PROTOCOL_ORDER = Object.keys(PROTOCOL_LABELS)

/** 新建节点「地区」下拉：国家两位代码 · 中文名；选「其他地区」时手动填两位代码 */
export const REGIONS: ReadonlyArray<readonly [string, string]> = [
  ['HK', 'HK · 香港'],
  ['TW', 'TW · 台湾'],
  ['JP', 'JP · 日本'],
  ['KR', 'KR · 韩国'],
  ['SG', 'SG · 新加坡'],
  ['US', 'US · 美国'],
  ['GB', 'GB · 英国'],
  ['DE', 'DE · 德国'],
  ['FR', 'FR · 法国'],
  ['NL', 'NL · 荷兰'],
  ['CA', 'CA · 加拿大'],
  ['AU', 'AU · 澳大利亚'],
  ['IN', 'IN · 印度'],
  ['TR', 'TR · 土耳其'],
  ['RU', 'RU · 俄罗斯'],
]

/** 协议卡片右侧小字：单 UDP 传输标 UDP，否则 TCP */
export const protocolKind = (s: ProtocolSchema) => (s.enums?.network?.length === 1 && s.enums.network[0] === 'udp' ? 'UDP' : 'TCP')

/**
 * 「上线」给还没上过线的草稿节点用：后端一步推到 active，并把所在服务器置为就绪（服务器还没就绪时也能用）；停用后再恢复用「启用」。
 */
export const canActivate = (n: Pick<NodeRow, 'serving_status'>) => n.serving_status === 'draft'

/** 下拉「暂不绑定」的说明：新建向导的服务器下拉与提示同一句 */
export const UNBOUND_HINT = '暂不绑定：节点先作为配置存着，不占端口、不下发；要上线时在详情「身份与操作」里绑定服务器'

/** 上线按钮置灰的原因：没绑服务器时返回提示，能上线（就状态而言）时返回 null */
export function activateBlock(n: Pick<NodeRow, 'server_id'>): string | null {
  return n.server_id == null ? '先在「身份与操作」里绑定服务器再上线' : null
}

/** 绑定 / 改绑 / 解绑置灰的原因；能操作时返回 null。退役单独说，其余只看后端给的 movable */
export function bindBlock(n: Pick<NodeRow, 'serving_status' | 'movable'>): string | null {
  if (n.serving_status === 'retired') return '节点已退役'
  if (!n.movable) return '节点在役，先停用再改绑 / 解绑'
  return null
}

/** 服务器下拉里能不能选：与后端 lockServerForNodes 一致，新建、绑定、复制都只拒已退役（容量满由后端 409 标到表单） */
export const serverAcceptsNodes = (s: { status: ServerStatus }) => s.status !== 'retired'

/** 绑定弹窗的校验：服务器必选，端口 1–65535 必填 */
export function validateBind(serverId: string, port: string): Record<string, string> {
  const e: Record<string, string> = {}
  if (!serverId) e.server_id = '选择要绑定的服务器'
  const p = Number(port)
  if (!Number.isInteger(p) || p < 1 || p > 65535) e.server_port = '1–65535'
  return e
}

/** POST v1/nodes/:id/bind 的请求体 */
export function bindBody(n: Pick<NodeRow, 'row_version'>, serverId: string, port: string): { row_version: number; server_id: string; server_port: number } {
  return { row_version: n.row_version, server_id: serverId, server_port: Number(port) }
}

/**
 * R113 上线响应的 warnings → 带下一步的提示（Toast 用普通语气，不是错误）。首次搭建时池还没绑套餐是正常流程：
 * 先上线节点，再去套餐页发布绑了这个池的版本。只认 node/service 上线（nodeNoPoolWarning）与未绑套餐的两条提示原文，其余原样接在后面
 */
export function activationHint(warning: string): string {
  if (warning === '所在节点池没有绑定任何套餐，暂时不服务任何用户') return '已上线。所在节点池还没绑定套餐，暂时不服务用户——下一步到「套餐」页，在套餐版本里选上这个节点池并发布。'
  if (warning === '未划入节点池，不服务任何用户') return '已上线。节点还没划入节点池，不服务任何用户——下一步在「设置」页签的「访问」里点「编辑」选一个节点池。'
  return `已上线。${warning}`
}

// ---------------------------------------------------------------------------
// 服务状态转换：与后端 node_status.go 的 nodeServingTransitions 一致（retired 只走 retire 接口）
// ---------------------------------------------------------------------------
const EDGES: Readonly<Record<ServingStatus, readonly ServingStatus[]>> = {
  draft: ['active', 'retired'],
  active: ['disabled', 'retired'],
  disabled: ['active', 'retired'],
  retired: [],
}
export const canTransition = (from: ServingStatus, to: ServingStatus) => EDGES[from].includes(to)

/** 批量启用 / 停用：整批有一条非法后端就整批 409，所以只提交能转的（启用还要已绑服务器），其余计数告诉管理员 */
export function batchPlan(rows: readonly NodeRow[], to: 'active' | 'disabled'): { items: Array<{ id: string; row_version: number }>; skipped: number } {
  const ok = rows.filter((n) => n.serving_status !== to && canTransition(n.serving_status, to) && (to !== 'active' || n.server_id != null))
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
type FieldKind = 'enum' | 'number' | 'boolean' | 'json' | 'list' | 'text'
export interface ProtocolField {
  /** 点号路径，如 reality_settings.private_key */
  path: string
  kind: FieldKind
  options: string[]
  required: boolean
  sensitive: boolean
  /** 后端 schema 给的字段说明（hints），没有则 undefined */
  hint?: string
  /** 显示名：来自后端表单元数据 fields[].label，没有 fields 时退回路径 */
  label: string
  /** 高级参数：默认折叠在「显示高级参数」后面 */
  advanced: boolean
  /** 条件显示：键是另一字段的路径，值是让本字段显示的表单取值；null 为无条件显示 */
  showIf: Record<string, string[]> | null
  /** 新建时预填（default 元数据转成表单字符串），没有默认为 '' */
  defaultValue: string
  /** 输入框占位文字 */
  placeholder?: string
  /** 枚举值 → 显示文字；键 '' 是选填枚举「不选」那一项 */
  optionLabels: Record<string, string>
}

export const isStable = (s: ProtocolSchema) => s.status === 'stable'

/** 读接口按名字在任意深度抹掉的键（node/protocol 的 sensitiveProtocolKey，R107 补了 mask_password）：这些字段永远读不回来，只能写 */
const REDACTED_KEYS = new Set(['password', 'passwd', 'secret', 'token', 'private_key', 'private-key', 'psk', 'obfs_password', 'obfs-password', 'mask_password'])

/** 敏感判断按整条路径或叶子名：schema 给的是 private_key，字段路径是 reality_settings.private_key */
function sensitiveOf(s: ProtocolSchema, path: string): boolean {
  const leaf = path.split('.').pop()!
  return REDACTED_KEYS.has(leaf.toLowerCase()) || (s.sensitive_properties ?? []).some((p) => p === path || p === leaf)
}

/** default 元数据（JSON 值）→ 表单字符串（String(v)）；缺席为 '' */
const defaultToString = (v: unknown): string => (v === undefined || v === null ? '' : String(v))

export function protocolFields(s: ProtocolSchema): ProtocolField[] {
  // kind / options / required / sensitive / hint 的算法对两条路一致，都按 path 取
  const fieldOf = (path: string): Omit<ProtocolField, 'label' | 'advanced' | 'showIf' | 'defaultValue' | 'optionLabels'> => {
    const type = s.property_types?.[path]
    // methods 是 Shadowsocks 加密方式（字段名 cipher）的可选值
    // 枚举按整条路径找，找不到再按叶子名：后端把 network_settings.mode 的可选值登记在 enums.mode 下
    const leaf = path.split('.').pop()!
    const options = s.enums?.[path] ?? (path.includes('.') ? s.enums?.[leaf] : undefined) ?? (s.methods && (path === 'cipher' || path === 'method') ? s.methods : [])
    const kind: FieldKind = options.length ? 'enum' : type === 'number' ? 'number' : type === 'boolean' ? 'boolean' : type === 'json' ? 'json' : type === 'list' ? 'list' : 'text'
    const hint = s.hints?.[path]
    return { path, kind, options, required: s.required.includes(path), sensitive: sensitiveOf(s, path), ...(hint ? { hint } : {}) }
  }
  // 后端给了 fields（10-16 表单元数据）：按它的顺序，label、条件显示、默认值、高级、占位与枚举文案都取自元数据
  if (s.fields?.length) {
    return s.fields.map((m) => ({
      ...fieldOf(m.path),
      label: m.label,
      advanced: m.advanced === true,
      showIf: m.show_if ?? null,
      defaultValue: defaultToString(m.default),
      ...(m.placeholder ? { placeholder: m.placeholder } : {}),
      optionLabels: m.option_labels ?? {},
    }))
  }
  // 没有 fields：退回旧行为——label 就是路径，其余元数据为空，必填在前，其余保持后端顺序
  const fields = s.allowed_properties.map((path) => ({ ...fieldOf(path), label: path, advanced: false, showIf: null, defaultValue: '', optionLabels: {} }))
  return [...fields.filter((f) => f.required), ...fields.filter((f) => !f.required)]
}

export type FormValues = Record<string, string>

/** 条件显示：showIf 的每个键都满足（表单字符串比较，没填是 ''）才显示 */
export function fieldVisible(f: ProtocolField, values: FormValues): boolean {
  if (!f.showIf) return true
  return Object.entries(f.showIf).every(([k, allowed]) => allowed.includes(values[k] ?? ''))
}

export const visibleFields = (fields: readonly ProtocolField[], values: FormValues): ProtocolField[] => fields.filter((f) => fieldVisible(f, values))

/** 新建时的预填值：后端 default 元数据转成表单字符串 */
export function protocolDefaults(fields: readonly ProtocolField[]): FormValues {
  const out: FormValues = {}
  for (const f of fields) if (f.defaultValue !== '') out[f.path] = f.defaultValue
  return out
}

/** 枚举下拉的选项：非必填开头加「不选」项（option_labels['']，缺省「默认」）；其余按 option_labels 配文字 */
export function enumOptions(f: ProtocolField): Array<{ value: string; label: string }> {
  const opts = f.options.map((o) => ({ value: o, label: f.optionLabels[o] ?? o }))
  return f.required ? opts : [{ value: '', label: f.optionLabels[''] ?? '默认' }, ...opts]
}

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
    else if (f.kind === 'boolean') out[f.path] = v === true || v === 'true' ? 'true' : v === false || v === 'false' ? 'false' : ''
    else if (f.kind === 'json' || (typeof v === 'object' && v !== null)) out[f.path] = JSON.stringify(v, null, 2)
    else out[f.path] = String(v)
    // 旧节点没有 cert_mode，后端按 file 处理；留空会让手填路径被 show_if 藏起来且必填拦保存
    if (f.path === 'cert_mode' && (v === undefined || v === null)) out[f.path] = 'file'
  }
  return out
}

/**
 * 编辑时敏感字段的口径（R106 / R107）：PATCH 的 protocol_config 里**缺席**的敏感键后端按原路径补回，
 * 所以留空 = 不改（不带这个键，必填的也不算缺）；要清空选填的敏感键，显式传 null（cleared 里的路径）。
 * keepSecrets 只在编辑同一种协议时为真——换协议后端不补旧密钥，必填照常要填。
 * mask_password 跟着开关 mask 走，关掉掩码时它本来就留空不带，无需特别处理
 */
interface SecretOptions {
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
  for (const f of visibleFields(fields, values)) {
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
    } else if (f.kind === 'boolean') {
      if (raw === '') continue
      value = raw === 'true'
    } else if (f.kind === 'list') {
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

/** REALITY：vless 或 trojan 且 tls=2 时可用（契约 reality-keypair 条目） */
export const realityEnabled = (nodeType: string, values: FormValues) => (nodeType === 'vless' || nodeType === 'trojan') && values.tls === '2'
export const REALITY_KEYS = { private_key: 'reality_settings.private_key', public_key: 'reality_settings.public_key', short_id: 'reality_settings.short_id' } as const

/**
 * 「生成密钥对」的结果写回表单：私钥、公钥总是换新；short_id 已填（去掉首尾空白非空）就保留管理员填的，空着才用生成的。
 * keptShortId 为 true 表示保留了原值（提示里要说）。
 */
export function withRealityKeypair(values: FormValues, k: { private_key: string; public_key: string; short_id: string }): { values: FormValues; keptShortId: boolean } {
  const keptShortId = Boolean(values[REALITY_KEYS.short_id]?.trim())
  return {
    values: {
      ...values,
      [REALITY_KEYS.private_key]: k.private_key,
      [REALITY_KEYS.public_key]: k.public_key,
      [REALITY_KEYS.short_id]: keptShortId ? values[REALITY_KEYS.short_id] : k.short_id,
    },
    keptShortId,
  }
}

/** 生成密钥对后的提示：保留了 short_id 时写明 */
export const realityKeypairToast = (keptShortId: boolean) =>
  keptShortId ? '已生成 REALITY 密钥对，short_id 保留你填的值；私钥保存后不再回显' : '已生成 REALITY 密钥对，私钥保存后不再回显'

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
  if (network === 'tcp' && nodeType === 'vmess') return ['VMess 裸 tcp 特征明显、容易被识别封锁：建议改用 VLESS REALITY，或换 ws / grpc 等传输套 CDN']
  if (network === 'tcp') return ['不加密的裸 tcp 就是明文代理，保存会被拒绝：VLESS 请选 2 · REALITY，或换 ws / httpupgrade / grpc / xhttp 并套 CDN']
  if (CDN_NETWORKS.has(network)) return ['不加密：需要套 CDN 或 TLS 反代后再给用户用，否则 UUID 和流量在线路上明文可见']
  return []
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
  rate: string
  country: string
}

/** 节点池必填的说明：下拉的提示与校验文案同一句 */
export const NO_POOL_HINT = '选择节点池：不在任何节点池里的节点不服务任何用户'

export const emptyBasic = (): BasicForm => ({ name: '', displayName: '', serverId: '', poolId: '', nodeType: '', host: '', port: '443', rate: '1', country: '' })

/** 新建向导第 2 步：这些键的校验错误落在「连接信息」 */
export const BASIC_STEP_KEYS = ['name', 'country_code', 'server_host', 'server_port', 'server_id', 'pool_id', 'capacity_nodes'] as const

export function basicFromRow(n: NodeDetail): BasicForm {
  return {
    name: n.name,
    displayName: n.display_name ?? '',
    serverId: n.server_id ?? '',
    poolId: n.pool_id ?? '',
    nodeType: n.node_type ?? '',
    host: n.server_host ?? '',
    port: n.server_port ? String(n.server_port) : '',
    rate: String(n.traffic_rate),
    country: n.country_code ?? '',
  }
}

/** live = 节点在役，地址必填 */
export function validateBasic(b: BasicForm, creating: boolean, live = false): Record<string, string> {
  const e: Record<string, string> = {}
  if (!b.name.trim()) e.name = '必填'
  else if ([...b.name.trim()].length > 120) e.name = '不超过 120 字'
  // 新建必须选节点池（R104）；编辑时节点池在详情『访问』里单独改，协议表单不再校验它
  if (creating && !b.poolId) e.pool_id = NO_POOL_HINT
  if (creating && !b.nodeType) e.node_type = '选择协议'
  if (live && !b.host.trim()) e.server_host = '必填：在役节点要有对外地址'
  const portTrim = b.port.trim()
  if (portTrim === '') {
    if (b.serverId) e.server_port = '选了服务器就要填端口（1–65535）'
  } else {
    const port = Number(portTrim)
    if (!Number.isInteger(port) || port < 1 || port > 65535) e.server_port = '1–65535'
  }
  const rate = Number(b.rate)
  if (!(rate > 0)) e.traffic_rate = '倍率必须大于 0'
  if (creating && !b.country.trim()) e.country_code = '请选择地区'
  else if (b.country && !/^[A-Za-z]{2}$/.test(b.country.trim())) e.country_code = '两位字母国家代码，如 HK'
  return e
}

export function createBody(b: BasicForm, config: Record<string, unknown>): Record<string, unknown> {
  return {
    name: b.name.trim(),
    node_type: b.nodeType,
    server_host: b.host.trim(),
    protocol_config: config,
    traffic_rate: Number(b.rate),
    ...(b.serverId ? { server_id: b.serverId } : {}),
    ...(b.port.trim() ? { server_port: Number(b.port) } : {}),
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
  if (b.port.trim() !== '' && b.port !== before.port) body.server_port = Number(b.port)
  if (Number(b.rate) !== row.traffic_rate) body.traffic_rate = Number(b.rate)
  if (b.country.trim().toUpperCase() !== before.country) body.country_code = b.country.trim() ? b.country.trim().toUpperCase() : null
  if (protocol.changed || b.nodeType !== before.nodeType) body.protocol_config = protocol.config
  return body
}

/** 协议是否改动：两次 toProtocolConfig 结果不同，或可见敏感字段有非空值，或要清空敏感字段 */
export function protocolChanged(
  fields: readonly ProtocolField[],
  initial: FormValues,
  current: FormValues,
  cleared: ReadonlySet<string> = new Set(),
  types?: ProtocolSchema['property_types'],
): boolean {
  if (cleared.size > 0) return true
  if (fields.some((f) => fieldVisible(f, current) && f.sensitive && (current[f.path] ?? '').trim() !== '')) return true
  const a = JSON.stringify(toProtocolConfig(fields, initial, types).config)
  const b = JSON.stringify(toProtocolConfig(fields, current, types).config)
  return a !== b
}

/** 这次保存会显式清空的敏感字段（留空且点了「清空」的），保存前要确认；只看当前可见字段 */
export const clearedSecrets = (fields: readonly ProtocolField[], current: FormValues, cleared: ReadonlySet<string>) =>
  visibleFields(fields, current)
    .filter((f) => clearableSecret(f) && cleared.has(f.path) && !(current[f.path] ?? '').trim())
    .map((f) => f.path)

// ---------------------------------------------------------------------------
// 路由规则行（D-D-1：下拉只放后端支持的匹配类型）
// ---------------------------------------------------------------------------
export const MATCH_KINDS = [
  ['domain_suffix', '域名后缀'],
  ['ip_cidr', 'IP 段'],
  ['fallback', '兜底（全部）'],
] as const
export type MatchKind = (typeof MATCH_KINDS)[number][0]

/** matcher 的别名键归一到下拉值（只认可编辑的三种；其余走只读的旧类型） */
const ALIASES: Readonly<Record<string, MatchKind>> = {
  domain_suffix: 'domain_suffix',
  domain_suffixes: 'domain_suffix',
  ip: 'ip_cidr',
  ip_cidr: 'ip_cidr',
  ip_cidrs: 'ip_cidr',
}

/** 不再提供新建的旧匹配类型：库里已有的规则只读显示、原样保存 */
const LEGACY_LABELS: Readonly<Record<string, string>> = {
  domain: '域名',
  domains: '域名',
  port: '端口',
  ports: '端口',
  network: '网络',
  networks: '网络',
  source: '来源 IP 段',
  source_ip_cidr: '来源 IP 段',
  source_cidrs: '来源 IP 段',
  source_port: '来源端口',
  source_ports: '来源端口',
}

export interface RuleRow {
  kind: MatchKind
  value: string
  outbound: string
  /** 界面不编辑启用与备注，保存时原样带回 */
  enabled: boolean
  note: string
  /** 旧匹配类型（或多个匹配键）：matcher 原样保存，界面只读显示 label 与 value */
  legacy?: { label: string; value: string; matcher: Record<string, unknown> }
}

const listText = (raw: unknown) => (Array.isArray(raw) ? raw : [raw]).map(String).join(', ')

export function routeToRow(r: Route): RuleRow {
  const entries = Object.entries(r.matcher)
  const base = { outbound: r.outbound_tag, enabled: r.enabled, note: r.note }
  if (entries.length === 0) return { ...base, kind: 'fallback', value: '' }
  const [key, raw] = entries[0]!
  const kind = ALIASES[key]
  if (entries.length === 1 && kind) return { ...base, kind, value: listText(raw) }
  return {
    ...base,
    kind: 'domain_suffix',
    value: '',
    legacy: {
      label: entries.map(([k]) => LEGACY_LABELS[k] ?? MATCH_KINDS.find(([m]) => m === ALIASES[k])?.[1] ?? k).join(' + '),
      value: entries.map(([, v]) => listText(v)).join('；'),
      matcher: r.matcher,
    },
  }
}

/** 新加的一条规则：域名后缀走直连 */
export const newRule = (): RuleRow => ({ kind: 'domain_suffix', value: '', outbound: 'direct', enabled: true, note: '' })

/** 规则行 → PUT 的 routes；优先级按序号 ×10；错误以序号为键。旧类型的 matcher 原样带回 */
export function rowsToRoutes(rows: readonly RuleRow[]): { routes: Array<Record<string, unknown>>; errors: Record<number, string> } {
  const errors: Record<number, string> = {}
  const routes = rows.map((r, i) => {
    let matcher: Record<string, unknown> = {}
    if (r.legacy) matcher = r.legacy.matcher
    else if (r.kind !== 'fallback') {
      const parts = r.value
        .split(/[,，\s]+/)
        .map((s) => s.trim())
        .filter(Boolean)
      if (!parts.length) errors[i] = '填匹配值'
      matcher = { [r.kind]: parts }
    }
    if (!r.outbound) errors[i] ??= '选择出站'
    return { priority: (i + 1) * 10, matcher, outbound_tag: r.outbound, enabled: r.enabled, note: r.note.trim() }
  })
  const lastEnabled = rows.map((r, i) => (r.enabled ? i : -1)).filter((i) => i >= 0)
  rows.forEach((r, i) => {
    if (r.enabled && !r.legacy && r.kind === 'fallback' && i !== lastEnabled[lastEnabled.length - 1]) errors[i] ??= '兜底规则必须是最后一条启用的规则'
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

/** 规则的简短文字（只读列表） */
export function ruleSummary(r: Route): string {
  const row = routeToRow(r)
  if (row.legacy) return `${row.legacy.label} ${row.legacy.value}`
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

/** 与 node/service 的 normalizeRouteGroupFields 同口径：名称去空白后 1–64 字、说明 ≤ 500 字、排序为 ±1000000 内的整数 */
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
export type BandwidthBucket = { start: number; mbps: number | null }

export function bandwidthBuckets(points: readonly MetricPoint[], now: Date = new Date()): BandwidthBucket[] {
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
  return sums.map((s, i) => ({ start: start + i * 3600_000, mbps: s.n ? Number((s.total / s.n).toFixed(1)) : null }))
}

/** 带宽曲线标题里的峰值：最大的非空桶；全空或最大值 ≤ 0 时返回 null；并列取更早的 */
export function bandwidthPeak(buckets: readonly BandwidthBucket[]): { mbps: number; start: number } | null {
  let best: { mbps: number; start: number } | null = null
  for (const b of buckets) {
    if (b.mbps === null || b.mbps <= 0) continue
    if (!best || b.mbps > best.mbps || (b.mbps === best.mbps && b.start < best.start)) best = { mbps: b.mbps, start: b.start }
  }
  return best
}

/** 桶开始时刻的日子说法：与 now 同一本地日「今天」，前一天「昨天」，更早「M月D日」（模块内用，不导出） */
function dayWord(start: number, now: Date): string {
  const s = new Date(start)
  const sY = s.getFullYear()
  const sM = s.getMonth()
  const sD = s.getDate()

  const nY = now.getFullYear()
  const nM = now.getMonth()
  const nD = now.getDate()

  if (sY === nY && sM === nM && sD === nD) return '今天'

  const y = new Date(now)
  y.setDate(y.getDate() - 1)
  if (sY === y.getFullYear() && sM === y.getMonth() && sD === y.getDate()) return '昨天'

  return `${sM + 1}月${sD}日`
}

/** 悬停读数：「昨天 23:00–24:00」「今天 14:00–15:00」（结束整点为 0 点时写 24:00） */
export function bucketRangeLabel(start: number, now: Date): string {
  const s = new Date(start)
  const hStart = s.getHours()
  const hStartStr = String(hStart).padStart(2, '0')
  const hEndStr = hStart === 23 ? '24:00' : `${String(hStart + 1).padStart(2, '0')}:00`
  const day = dayWord(start, now)
  return `${day} ${hStartStr}:00–${hEndStr}`
}

/** 标题峰值：「峰值 393.1 Mbps · 昨天 23:00」；没有峰值回 null */
export function bandwidthPeakLabel(peak: { mbps: number; start: number } | null, now: Date): string | null {
  if (!peak) return null
  const s = new Date(peak.start)
  const hStr = `${String(s.getHours()).padStart(2, '0')}:00`
  const day = dayWord(peak.start, now)
  return `峰值 ${peak.mbps} Mbps · ${day} ${hStr}`
}

/** x 轴刻度：第 0、6、12、18、23 桶的开始整点「HH:00」，带桶序号（定位用） */
export function bandwidthAxis(buckets: readonly BandwidthBucket[]): Array<{ index: number; label: string }> {
  const indices = [0, 6, 12, 18, 23]
  const axis: Array<{ index: number; label: string }> = []
  for (const index of indices) {
    const b = buckets[index]
    if (b) {
      const h = new Date(b.start).getHours()
      axis.push({ index, label: `${String(h).padStart(2, '0')}:00` })
    }
  }
  return axis
}

/** 地址查询串 f 的取值 → 状态筛选；认不出的一律 'all' */
export function parseNodeFilter(raw: string | null): NodeFilter {
  if (raw === 'online' || raw === 'offline' || raw === 'disabled' || raw === 'retired' || raw === 'cert') return raw
  return 'all'
}

/** 列表页脚「共 N 个 · M 个在线」：N 不含已退役，M 是 nodeState 为在线的 */
export function nodeCounts(rows: readonly Pick<NodeRow, 'serving_status' | 'stale'>[]): { total: number; online: number } {
  let total = 0
  let online = 0
  for (const n of rows) {
    const state = nodeState(n)
    if (state.filter === 'retired') continue
    total += 1
    if (state.filter === 'online') online += 1
  }
  return { total, online }
}

/** 详情「协议参数」只读摘要的一行 */
export interface SummaryRow {
  label: string
  value: string
  muted?: boolean
}

/**
 * 协议参数只读摘要：按后端协议 schema（中文名、条件显示、选项文案）生成。
 * 前两行固定：协议、监听端口；schema 缺席或与节点协议不同时只给这两行。
 * 其余按 protocolFields 顺序、只取当前可见（visibleFields）的字段：
 * 敏感字段一律「不回显」（muted）；空值的必填 / 非高级字段写「未设置」（muted），空值的高级字段跳过；
 * 枚举取 optionLabels[值] ?? 值；布尔 'true' → 开启、'false' → 关闭；json 压成一行、超过 60 字截断加「…」；其余原样（list 已是逗号分隔）
 */
export function protocolSummary(schema: ProtocolSchema | undefined, node: Pick<NodeDetail, 'node_type' | 'server_port' | 'protocol_config'>): SummaryRow[] {
  const head: SummaryRow[] = [
    { label: '协议', value: protocolLabel(node.node_type) },
    node.server_port ? { label: '监听端口', value: String(node.server_port) } : { label: '监听端口', value: '未设置', muted: true },
  ]
  if (!schema || schema.node_type !== node.node_type) return head
  const fields = protocolFields(schema)
  const values = toFormValues(fields, node.protocol_config)
  const visible = visibleFields(fields, values)
  const rows: SummaryRow[] = [...head]
  for (const f of visible) {
    if (f.sensitive) {
      rows.push({ label: f.label, value: '不回显', muted: true })
      continue
    }
    const raw = (values[f.path] ?? '').trim()
    if (!raw) {
      if (f.advanced) continue
      rows.push({ label: f.label, value: '未设置', muted: true })
      continue
    }
    let value = raw
    if (f.kind === 'enum') value = f.optionLabels[raw] ?? raw
    else if (f.kind === 'boolean') value = raw === 'true' ? '开启' : raw === 'false' ? '关闭' : raw
    else if (f.kind === 'json') {
      try {
        value = JSON.stringify(JSON.parse(raw))
      } catch {
        value = raw.replace(/\s+/g, ' ')
      }
      if (value.length > 60) value = `${value.slice(0, 60)}…`
    }
    rows.push({ label: f.label, value })
  }
  return rows
}
