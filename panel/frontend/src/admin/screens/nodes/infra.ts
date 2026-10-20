import { heartbeatLabel, protocolLabel, type Tone } from './logic'
import type { NodeRow, Pool, PoolStatus, Server, ServerStatus, ServerUpgradeResponse, UpgradeStatus } from './schemas'

// ---------------------------------------------------------------------------
// 服务器状态
// ---------------------------------------------------------------------------
/** 契约：「标记维护」发 draining，卡片显示「维护中」；maintenance 是进一步停机 */
export const SERVER_STATUS: Readonly<Record<ServerStatus, { label: string; tone: Tone }>> = {
  draft: { label: '草稿', tone: 'neutral' },
  ready: { label: '服务中', tone: 'ok' },
  draining: { label: '维护中', tone: 'warn' },
  maintenance: { label: '停机维护', tone: 'warn' },
  retired: { label: '已退役', tone: 'neutral' },
}

/** 圆点：ready 且 90 秒内有心跳绿，ready 但心跳断了红（设计 down），其余灰 */
export function serverDot(s: Pick<Server, 'status' | 'heartbeat_online'>): Tone {
  if (s.status !== 'ready') return 'neutral'
  return s.heartbeat_online ? 'ok' : 'danger'
}

/** node/service serverStatusTransitions 原样 */
const SERVER_EDGES: Readonly<Record<ServerStatus, readonly ServerStatus[]>> = {
  draft: ['ready', 'maintenance', 'retired'],
  ready: ['draining', 'maintenance'],
  draining: ['ready', 'maintenance', 'retired'],
  maintenance: ['ready', 'draining', 'retired'],
  retired: [],
}
export const nextServerStatuses = (from: ServerStatus): readonly ServerStatus[] => SERVER_EDGES[from]

interface QuickToggle {
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

/** 删除按钮禁用时的说明：能删返回 null，否则「只有草稿或已退役的服务器能删除」 */
export function serverDeleteBlock(s: Pick<Server, 'status'>): string | null {
  return canDeleteServer(s) ? null : '只有草稿或已退役的服务器能删除'
}

/** 卡片与详情副行：「地区 · IP · agent 版本」；没有 IP 省掉那一段；没有 agent 写「未连接」，有则「agent 1.8.2」 */
export function serverSubtitle(s: Pick<Server, 'region' | 'public_ipv4' | 'agent_version'>): string {
  const region = s.region?.trim() || '地区待识别'
  const parts = [region]
  if (s.public_ipv4?.trim()) parts.push(s.public_ipv4.trim())
  parts.push(s.agent_version?.trim() ? `agent ${s.agent_version.trim()}` : '未连接')
  return parts.join(' · ')
}

/**
 * 卡片与详情的提示行（按顺序）：
 * status_reason 非空 → `原因：${status_reason}`；
 * status 为 ready 且 heartbeat_online 为 false → 有 last_heartbeat_at 时 `心跳 ${heartbeatLabel(at, now)}，可能已断开`，没有时「还没有收到心跳」；
 * never_seen_node_count > 0 → `${n} 个在役节点从未心跳（多半没装 agent）`
 */
export function serverNotes(
  s: Pick<Server, 'status' | 'status_reason' | 'heartbeat_online' | 'last_heartbeat_at' | 'never_seen_node_count'>,
  now?: Date,
): string[] {
  const out: string[] = []
  if (s.status_reason?.trim()) out.push(`原因：${s.status_reason.trim()}`)
  if (s.status === 'ready' && !s.heartbeat_online) {
    out.push(s.last_heartbeat_at ? `心跳 ${heartbeatLabel(s.last_heartbeat_at, now)}，可能已断开` : '还没有收到心跳')
  }
  if (s.never_seen_node_count > 0) out.push(`${s.never_seen_node_count} 个在役节点从未心跳（多半没装 agent）`)
  return out
}

/** 服务器详情的节点列表：server_id 等于 serverId 且未退役，保持原顺序 */
export function serverNodeLines<T extends Pick<NodeRow, 'server_id' | 'serving_status'>>(nodes: readonly T[], serverId: string): T[] {
  return nodes.filter((n) => n.server_id === serverId && n.serving_status !== 'retired')
}

/** 删除后果：后端不拒绝名下节点，而是级联静默（契约改写了设计的「先迁移或删除」）；资格另见 canDeleteServer */
export function deleteServerNotice(s: Pick<Server, 'node_count'>): string {
  return s.node_count > 0 ? `删除后不能恢复，服务器身份立即吊销。名下 ${s.node_count} 个节点里，在役的退役；草稿和已停用的解除绑定、留作配置，可以再绑到别的服务器。` : '删除后不能恢复。名下没有节点。'
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

/** 「绑定套餐」：套餐名用「、」连接，没有显示「—」 */
export const planNamesLabel = (p: Pick<Pool, 'plan_names'>) => (p.plan_names.length ? p.plan_names.join('、') : '—')

/** 设计稿显示「仅限 A、B」；没有限定为 null */
export const poolGroupsLabel = (p: Pick<Pool, 'allowed_user_groups'>) => (p.allowed_user_groups.length ? `仅限 ${p.allowed_user_groups.map((g) => g.name).join('、')}` : null)


/** 契约：nodes > 0 或 plans > 0 时删除按钮直接禁用并说明；模板、发布记录、未用令牌这三种只有后端知道，靠 409 的消息 */
export function poolDeleteBlock(p: Pick<Pool, 'nodes' | 'plans'>): string | null {
  if (p.nodes > 0) return `池里还有 ${p.nodes} 个节点，先把它们改到别的池`
  if (p.plans > 0) return `还有 ${p.plans} 个在售或草稿套餐绑定了它，先在套餐里解绑（已归档的不挡，删池时一起解绑）`
  return null
}

export interface PoolForm {
  name: string
  code: string
  region: string
  status: PoolStatus
  /** R104「仅限用户组」名单（用户组 id）；空 = 不限定 */
  groupIds: string[]
}
export const emptyPoolForm = (): PoolForm => ({ name: '', code: '', region: '', status: 'active', groupIds: [] })
export const poolFormFrom = (p: Pool): PoolForm => ({ name: p.name, code: p.code, region: p.region, status: p.status, groupIds: p.allowed_user_groups.map((g) => g.id) })

const sameIds = (a: readonly string[], b: readonly string[]) => a.length === b.length && [...a].sort().join() === [...b].sort().join()

/**
 * POST v1/node-pools：name 必填，code 留空由后端从名字派生；status 被后端忽略，不传。
 * R104：请求里带了 allowed_user_group_ids 就要 reauth，所以只在真的限定了用户组时才带
 */
export function createPoolBody(f: PoolForm): Record<string, unknown> {
  return {
    name: f.name.trim(),
    ...(f.code.trim() ? { code: f.code.trim() } : {}),
    ...(f.region.trim() ? { region: f.region.trim() } : {}),
    ...(f.groupIds.length ? { allowed_user_group_ids: f.groupIds } : {}),
  }
}

/** 编辑后这次保存是否会改动「仅限用户组」名单（会就要 reauth，弹窗里提前说） */
export const poolGroupsChanged = (p: Pick<Pool, 'allowed_user_groups'>, f: Pick<PoolForm, 'groupIds'>) =>
  !sameIds(
    p.allowed_user_groups.map((g) => g.id),
    f.groupIds,
  )

/**
 * POST v1/node-pools/{id}：空串 = 不改，所以只带改了且非空的字段（名称与地区都清不空）；code 被后端忽略，不传。
 * R104 名单省略 = 不改、[] = 取消限定，只在名单真的变了才带（带了就要 reauth）。没改动返回 null
 */
export function patchPoolBody(p: Pool, f: PoolForm): Record<string, unknown> | null {
  const body: Record<string, unknown> = {}
  if (poolGroupsChanged(p, f)) body.allowed_user_group_ids = f.groupIds
  if (f.name.trim() && f.name.trim() !== p.name) body.name = f.name.trim()
  if (f.region.trim() && f.region.trim() !== p.region) body.region = f.region.trim()
  if (f.status !== p.status) body.status = f.status
  return Object.keys(body).length ? body : null
}

// ---------------------------------------------------------------------------
// 在线升级
// ---------------------------------------------------------------------------
export const UPGRADE_REASON_TEXT: Readonly<Record<string, string>> = {
  manifest_missing: '面板上没有节点端发布清单',
  signature_missing: '面板上没有发布清单的签名',
  manifest_invalid: '发布清单格式不对',
  binary_mismatch: '面板托管的节点端程序与发布清单不符',
  in_progress: '正在升级',
  not_ready: '服务器不在服务中',
  not_connected: '节点端没有连着面板',
  no_upgrade_support: '节点端版本太旧或没装升级助手，重装一次后才能在线升级',
  arch_unsupported: '发布清单里没有这台机器的架构',
  up_to_date: '已是最新版本',
  serial_not_newer: '目标版本不比这台上的新',
  not_found: '服务器不存在或已删除',
  download_failed: '下载新版本失败',
  sha256_mismatch: '下载的文件与发布清单的摘要不符',
  signature_invalid: '发布清单的签名验不过',
  busy: '这台已有一次升级在进行',
  apply_failed: '替换程序或重启失败，已保持旧版',
  confirm_timeout: '新版本没能连上面板，已自动装回旧版',
  rollback_failed: '装回旧版失败，需要登录服务器处理',
  unknown: '节点端没有给出原因',
}

export function upgradeReasonText(code: string | null | undefined): string {
  if (code === null || code === undefined || code === '') return '节点端没有给出原因'
  const text = UPGRADE_REASON_TEXT[code]
  return text ?? `原因码 ${code}`
}

export const UPGRADE_STATUS: Readonly<Record<UpgradeStatus, { label: string; tone: Tone }>> = {
  sent: { label: '升级中', tone: 'info' },
  staged: { label: '升级中', tone: 'info' },
  succeeded: { label: '已升级', tone: 'ok' },
  failed: { label: '失败', tone: 'danger' },
  rolled_back: { label: '已回退', tone: 'warn' },
  timed_out: { label: '超时', tone: 'warn' },
}

export function upgradeOffer(s: Pick<Server, 'upgrade'>): string | null {
  if (s.upgrade.available && s.upgrade.target_version) return `可升级到 ${s.upgrade.target_version}`
  return null
}

const UPGRADE_BLOCK_SILENT = new Set(['not_ready', 'not_connected', 'up_to_date', 'in_progress'])

export function upgradeBlockNote(s: Pick<Server, 'upgrade'>): string | null {
  const { available, reason } = s.upgrade
  if (available || reason === null || UPGRADE_BLOCK_SILENT.has(reason)) return null
  return `不能在线升级：${upgradeReasonText(reason)}`
}

export function upgradeTarget(list: readonly Pick<Server, 'upgrade'>[]): string | null {
  for (const s of list) {
    if (s.upgrade.available && s.upgrade.target_version) return s.upgrade.target_version
  }
  return null
}

export function upgradeSelectable(s: Pick<Server, 'upgrade'>): boolean {
  return s.upgrade.available
}

export function pruneUpgradePicks(picked: ReadonlySet<string>, list: readonly Pick<Server, 'id' | 'upgrade'>[]): Set<string> {
  const out = new Set<string>()
  const byId = new Map(list.map((s) => [s.id, s]))
  for (const id of picked) {
    const s = byId.get(id)
    if (s && s.upgrade.available) out.add(id)
  }
  return out
}

const MS_24H = 24 * 60 * 60 * 1000

function lastUpgradePresentation(
  lu: NonNullable<Server['last_upgrade']>,
  hideSucceededAfter24h: boolean,
  now: Date,
): { label: string; tone: Tone; text: string } | null {
  const { status, target_version, reason_code, updated_at } = lu
  if (hideSucceededAfter24h && status === 'succeeded' && now.getTime() - new Date(updated_at).getTime() >= MS_24H) return null
  const { label, tone } = UPGRADE_STATUS[status]
  let text: string
  if (status === 'sent' || status === 'staged') text = `正在升级到 ${target_version}`
  else if (status === 'succeeded') text = `已升级到 ${target_version}`
  else if (status === 'timed_out' && reason_code === null) text = `升级到 ${target_version}超时：十几分钟内没有结果，请查看节点端日志`
  else text = `升级到 ${target_version}${UPGRADE_STATUS[status].label}：${upgradeReasonText(reason_code)}`
  return { label, tone, text }
}

export function lastUpgradeLine(s: Pick<Server, 'last_upgrade'>, now: Date = new Date()): { label: string; tone: Tone; text: string } | null {
  if (s.last_upgrade === null) return null
  return lastUpgradePresentation(s.last_upgrade, true, now)
}

export function lastUpgradeDetail(s: Pick<Server, 'last_upgrade'>): { label: string; tone: Tone; text: string } | null {
  if (s.last_upgrade === null) return null
  return lastUpgradePresentation(s.last_upgrade, false, new Date())
}

export function upgradeConfirmBody(count: number, version: string): string {
  return `会重启这 ${count} 台服务器上的全部节点，正在连接的用户会断开后自动重连。新版本 3 分钟内连不上面板时，会自动装回旧版。整个过程约几分钟，结果显示在各台的卡片上。目标版本 ${version}。`
}

export function upgradeResultToast(r: ServerUpgradeResponse, names: ReadonlyMap<string, string>): { text: string; tone: 'ok' | 'danger' } {
  const failed = r.results.filter((x) => !x.ok)
  const n = r.results.length - failed.length
  const m = failed.length
  const listPart = (items: typeof failed) => {
    const shown = items.slice(0, 3).map((x) => `${names.get(x.server_id) ?? x.server_id}（${upgradeReasonText(x.reason)}）`)
    if (items.length <= 3) return shown.join('、')
    return `${shown.join('、')}等 ${m} 台`
  }
  if (m === 0) return { text: `已向 ${n} 台服务器下发升级到 ${r.target_version}`, tone: 'ok' }
  if (n === 0) return { text: `没有发起升级：${listPart(failed)}`, tone: 'danger' }
  return { text: `已向 ${n} 台下发升级，${m} 台没有发起：${listPart(failed)}`, tone: 'ok' }
}
