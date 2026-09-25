/**
 * [INPUT]: 依赖 ./logic 的 Tone 与 protocolLabel，依赖 ./schemas 的 Server / ServerStatus / Pool / PoolStatus / NodeRow 类型
 * [OUTPUT]: 对外提供服务器与节点池的纯函数：服务器圆点与状态文字、CPU / 内存 / 磁盘三条占用、卡片上的快捷状态切换、合法状态边、删除资格与后果文案、按服务器分组节点、服务器表单模型（校验、新建体、PATCH 差量、容量冲突解析）；节点池状态文字、删除资格、绑定套餐文字、表单新建体与编辑差量
 * [POS]: admin/screens/nodes 第 ③ 步的逻辑层（logic.ts 管节点与路由，这里管服务器与节点池）：映射全部取自 api-contract.md 后台-07 · 服务器 / 节点池 两节的「设计 / 映射」行与 Go 的 server_admin.go、pools.go 校验器，infra.test.ts 逐条守住
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { protocolLabel, type Tone } from './logic'
import type { NodeRow, Pool, PoolStatus, Server, ServerStatus } from './schemas'

// ---------------------------------------------------------------------------
// 服务器状态
// ---------------------------------------------------------------------------
/** 契约：「标记维护」发 draining，卡片显示「维护中」；maintenance 是进一步停机 */
export const SERVER_STATUS: Readonly<Record<ServerStatus, { label: string; tone: Tone }>> = {
  draft: { label: '草稿', tone: 'neutral' },
  ready: { label: '服务中', tone: 'ok' },
  draining: { label: '维护中', tone: 'warn' },
  maintenance: { label: '停机维护', tone: 'warn' },
  unhealthy: { label: '异常', tone: 'danger' },
  quarantined: { label: '已隔离', tone: 'danger' },
  retired: { label: '已退役', tone: 'neutral' },
}

/** 圆点：ready 且 90 秒内有心跳绿，ready 但心跳断了红（设计 down），其余灰 */
export function serverDot(s: Pick<Server, 'status' | 'heartbeat_online'>): Tone {
  if (s.status !== 'ready') return 'neutral'
  return s.heartbeat_online ? 'ok' : 'danger'
}

/** nodefabric.serverStatusTransitions 原样 */
const SERVER_EDGES: Readonly<Record<ServerStatus, readonly ServerStatus[]>> = {
  draft: ['ready', 'maintenance', 'retired'],
  ready: ['draining', 'unhealthy', 'quarantined'],
  draining: ['ready', 'maintenance', 'retired'],
  maintenance: ['ready', 'retired'],
  unhealthy: ['draining', 'maintenance', 'quarantined', 'retired'],
  quarantined: ['draining', 'maintenance', 'retired'],
  retired: [],
}
export const nextServerStatuses = (from: ServerStatus): readonly ServerStatus[] => SERVER_EDGES[from]

export interface QuickToggle {
  to: ServerStatus
  label: string
  done: string
}

/** 卡片上的一个快捷按钮：服务中 → 标记维护（draining）；维护中 / 停机维护 → 恢复服务；草稿 → 投入服务；其余走详情里的完整状态 */
export function quickToggle(status: ServerStatus): QuickToggle | null {
  switch (status) {
    case 'ready':
      return { to: 'draining', label: '标记维护', done: '已标记维护：停止分配新连接，已有连接保持' }
    case 'draining':
    case 'maintenance':
      return { to: 'ready', label: '恢复服务', done: '已恢复服务' }
    case 'draft':
      return { to: 'ready', label: '投入服务', done: '服务器已投入服务' }
    default:
      return null
  }
}

/** 只有草稿或已退役能删（后端 409「服务器仍在服务，请先在『状态』里退役」） */
export const canDeleteServer = (s: Pick<Server, 'status'>) => s.status === 'draft' || s.status === 'retired'

/** 删除后果：后端不拒绝名下节点，而是级联静默（契约改写了设计的「先迁移或删除」）；资格另见 canDeleteServer */
export function deleteServerNotice(s: Pick<Server, 'node_count'>): string {
  return s.node_count > 0 ? `删除后不能恢复。名下 ${s.node_count} 个节点会一起下线，身份立即吊销；同名重新安装可以恢复。` : '删除后不能恢复。名下没有节点。'
}

// ---------------------------------------------------------------------------
// 占用条：CPU → cpu_bp/100，内存 → used/total，磁盘 → used/total；没有探针显示「—」
// ---------------------------------------------------------------------------
export type MeterLevel = 'normal' | 'warm' | 'hot'
export interface Meter {
  label: string
  percent: number | null
  level: MeterLevel
}
/** 设计稿三档：> 85 红、> 65 橙、其余墨色 */
export const meterLevel = (p: number): MeterLevel => (p > 85 ? 'hot' : p > 65 ? 'warm' : 'normal')

const ratio = (used: number | null, total: number | null) => (used === null || !total ? null : Math.min(100, Math.round((used / total) * 100)))

export function serverMeters(s: Pick<Server, 'cpu_bp' | 'mem_used_mb' | 'mem_total_mb' | 'disk_used_gb' | 'disk_total_gb'>): Meter[] {
  const cpu = s.cpu_bp === null ? null : Math.min(100, Math.round(s.cpu_bp / 100))
  return [
    ['CPU', cpu],
    ['内存', ratio(s.mem_used_mb, s.mem_total_mb)],
    ['磁盘', ratio(s.disk_used_gb, s.disk_total_gb)],
  ].map(([label, percent]) => ({ label: label as string, percent: percent as number | null, level: percent === null ? 'normal' : meterLevel(percent as number) }))
}

/** 卡片节点标签：GET v1/nodes 按 server_id 分组（契约：不逐台调 /servers/{id}/nodes），已退役的不列 */
export function nodesByServer(nodes: readonly NodeRow[]): Map<string, string[]> {
  const out = new Map<string, string[]>()
  for (const n of nodes) {
    if (!n.server_id || n.serving_status === 'retired') continue
    const list = out.get(n.server_id) ?? []
    list.push(`${n.name} · ${protocolLabel(n.node_type)}`)
    out.set(n.server_id, list)
  }
  return out
}

// ---------------------------------------------------------------------------
// 服务器表单：新建只需名称（其余接入后由 agent 回填），编辑是后端可改字段全集
// ---------------------------------------------------------------------------
export interface ServerForm {
  name: string
  region: string
  hostname: string
  publicIpv4: string
  publicIpv6: string
  privateIpv4: string
  architecture: string
  osName: string
  capacity: string
  notes: string
}

/** 表单键 → 请求字段；除 name、capacity 外都是可清空的文本（"" = 清空） */
const TEXT_FIELDS = [
  ['region', 'region', 64],
  ['hostname', 'hostname', 253],
  ['publicIpv4', 'public_ipv4', 0],
  ['publicIpv6', 'public_ipv6', 0],
  ['privateIpv4', 'private_ipv4', 0],
  ['architecture', 'architecture', 32],
  ['osName', 'os_name', 120],
  ['notes', 'notes', 2000],
] as const satisfies ReadonlyArray<readonly [keyof ServerForm, string, number]>

export const emptyServerForm = (): ServerForm => ({ name: '', region: '', hostname: '', publicIpv4: '', publicIpv6: '', privateIpv4: '', architecture: '', osName: '', capacity: '32', notes: '' })

export function serverFormFrom(s: Server): ServerForm {
  return {
    name: s.name,
    region: s.region ?? '',
    hostname: s.hostname ?? '',
    publicIpv4: s.public_ipv4 ?? '',
    publicIpv6: s.public_ipv6 ?? '',
    privateIpv4: s.private_ipv4 ?? '',
    architecture: s.architecture ?? '',
    osName: s.os_name ?? '',
    capacity: String(s.capacity_nodes),
    notes: s.notes ?? '',
  }
}

const IPV4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/
function isIpv6(v: string): boolean {
  if (!v.includes(':') || IPV4.test(v)) return false
  try {
    return new URL(`http://[${v}]/`).hostname.length > 2
  } catch {
    return false
  }
}

/** 与 validateServerFields / validateServerTextLimits 同口径；错误键用请求字段名，422 的 fields 可直接落回 */
export function validateServerForm(f: ServerForm): Record<string, string> {
  const errors: Record<string, string> = {}
  const name = f.name.trim()
  if (!name || [...name].length > 120) errors.name = '名称必须为 1 到 120 个字符'
  for (const [key, field, limit] of TEXT_FIELDS) {
    const v = f[key].trim()
    if (limit && [...v].length > limit) errors[field] = `内容过长，最多允许 ${limit} 个字符`
  }
  if (f.publicIpv4.trim() && !IPV4.test(f.publicIpv4.trim())) errors.public_ipv4 = 'IP 地址格式不正确'
  if (f.privateIpv4.trim() && !IPV4.test(f.privateIpv4.trim())) errors.private_ipv4 = 'IP 地址格式不正确'
  if (f.publicIpv6.trim() && !isIpv6(f.publicIpv6.trim())) errors.public_ipv6 = 'IP 地址格式不正确'
  if (!/^\d+$/.test(f.capacity.trim()) || Number(f.capacity) <= 0) errors.capacity_nodes = '必须大于 0'
  return errors
}

/** POST v1/servers：只带填了的字段（DisallowUnknownFields，字段集与 CreateServerInput 一致） */
export function createServerBody(f: ServerForm): Record<string, unknown> {
  const body: Record<string, unknown> = { name: f.name.trim(), capacity_nodes: Number(f.capacity) }
  for (const [key, field] of TEXT_FIELDS) {
    const v = f[key].trim()
    if (v) body[field] = v
  }
  return body
}

/** PATCH v1/servers/{id}：只带改了的字段；省略 = 不改，"" = 清空，name 不能清空。没改动返回 null */
export function patchServerBody(s: Server, f: ServerForm): Record<string, unknown> | null {
  const before = serverFormFrom(s)
  const body: Record<string, unknown> = {}
  if (f.name.trim() !== before.name) body.name = f.name.trim()
  for (const [key, field] of TEXT_FIELDS) if (f[key].trim() !== before[key].trim()) body[field] = f[key].trim()
  if (Number(f.capacity) !== s.capacity_nodes) body.capacity_nodes = Number(f.capacity)
  return Object.keys(body).length ? { row_version: s.row_version, ...body } : null
}

/** 409「服务器容量不能低于当前节点占用」的 fields.capacity_nodes = "minimum=N" */
export function capacityMinimum(fields: Readonly<Record<string, string>>): number | null {
  const m = /^minimum=(\d+)$/.exec(fields.capacity_nodes ?? '')
  return m ? Number(m[1]) : null
}

// ---------------------------------------------------------------------------
// 节点池
// ---------------------------------------------------------------------------
export const POOL_STATUS: Readonly<Record<PoolStatus, { label: string; tone: Tone }>> = {
  active: { label: '启用', tone: 'ok' },
  draining: { label: '排空中', tone: 'warn' },
  disabled: { label: '已停用', tone: 'neutral' },
}

/** 「绑定套餐」：套餐名用「、」连接，没有显示「—」（「仅用户组」是待决 D-B-3，未决前不显示） */
export const planNamesLabel = (p: Pick<Pool, 'plan_names'>) => (p.plan_names.length ? p.plan_names.join('、') : '—')

/** 契约：nodes > 0 或 plans > 0 时删除按钮直接禁用并说明；模板、发布记录、未用令牌这三种只有后端知道，靠 409 的消息 */
export function poolDeleteBlock(p: Pick<Pool, 'nodes' | 'plans'>): string | null {
  if (p.nodes > 0) return `还有 ${p.nodes} 个节点，先把节点移到别的节点池`
  if (p.plans > 0) return `还有 ${p.plans} 个套餐版本绑定着它，先在套餐里解除绑定`
  return null
}

export interface PoolForm {
  name: string
  code: string
  region: string
  status: PoolStatus
}
export const emptyPoolForm = (): PoolForm => ({ name: '', code: '', region: '', status: 'active' })
export const poolFormFrom = (p: Pool): PoolForm => ({ name: p.name, code: p.code, region: p.region, status: p.status })

/** POST v1/node-pools：name 必填，code 留空由后端从名字派生；status 被后端忽略，不传 */
export function createPoolBody(f: PoolForm): Record<string, unknown> {
  return { name: f.name.trim(), ...(f.code.trim() ? { code: f.code.trim() } : {}), ...(f.region.trim() ? { region: f.region.trim() } : {}) }
}

/** POST v1/node-pools/{id}：空串 = 不改，所以只带改了且非空的字段（名称与地区都清不空）；code 被后端忽略，不传。没改动返回 null */
export function patchPoolBody(p: Pool, f: PoolForm): Record<string, unknown> | null {
  const body: Record<string, unknown> = {}
  if (f.name.trim() && f.name.trim() !== p.name) body.name = f.name.trim()
  if (f.region.trim() && f.region.trim() !== p.region) body.region = f.region.trim()
  if (f.status !== p.status) body.status = f.status
  return Object.keys(body).length ? body : null
}
