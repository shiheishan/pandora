/**
 * [INPUT]: 依赖 ../../../core/format 的 formatMoney / relativeTime，依赖 ../../modules 的 ModuleKey / Permissions / canRead / canReadModule，依赖 ./api 的响应类型
 * [OUTPUT]: 对外提供 formatBytes、formatCount、formatPercent、formatDuration、formatLatency、formatDateTime、percentChange、dashboardAccess、reachable、taskCards、kpiRevenueDelta、revenueSummary、activitySummary、backupSummary、systemRows、trafficRows、trafficNotes 及其类型
 * [POS]: admin/screens/dash 的纯逻辑层：把接口数据映射成卡片、行与文案（契约后台-01 的「映射」一行），不碰 React 与网络；界面组件只负责摆放，model.test.ts 覆盖这里的全部分支
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { formatMoney, relativeTime } from '../../../core/format'
import { canRead, canReadModule, type ModuleKey, type Permissions } from '../../modules'
import type { ActivityPoint, Backlog, BackupStatus, ComponentState, NodeTraffic, RevenuePoint, SystemComponent, SystemStatus, TaskItem, UserTraffic } from './api'

// ===========================================================================
// 格式化
// ===========================================================================

const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB', 'EB'] as const

/**
 * 字节 → 「18.6 TB / 1.80 TB / 540 GB」，三位有效数字、1024 进制。
 * DASH-01 要求 BigInt 安全：十进制字符串不先转 number，全程整数运算再拼小数。
 */
export function formatBytes(value: string | number | bigint): string {
  let n = BigInt(value)
  const sign = n < 0n ? '-' : ''
  if (n < 0n) n = -n
  let unit = 1n
  let i = 0
  while (i < BYTE_UNITS.length - 1 && n >= unit * 1024n) {
    unit *= 1024n
    i++
  }
  if (i === 0) return `${sign}${n} B`
  const whole = n / unit
  const decimals = whole < 10n ? 2 : whole < 100n ? 1 : 0
  const scale = 10n ** BigInt(decimals)
  const scaled = (n * scale + unit / 2n) / unit
  const intPart = scaled / scale
  const frac = decimals ? `.${String(scaled % scale).padStart(decimals, '0')}` : ''
  return `${sign}${intPart}${frac} ${BYTE_UNITS[i]}`
}

/** 整数千分位：12408 → 12,408 */
export function formatCount(n: number): string {
  const sign = n < 0 ? '-' : ''
  return sign + String(Math.abs(Math.trunc(n))).replace(/\B(?=(\d{3})+(?!\d))/g, ',')
}

/** 百分比变化：+12.4% / −3.1%（设计稿用数学减号）/ 0.0% */
export function formatPercent(ratio: number): string {
  const pct = Math.round(ratio * 1000) / 10
  if (pct === 0) return '0.0%'
  return `${pct > 0 ? '+' : '−'}${Math.abs(pct).toFixed(1)}%`
}

/** 相对变化；上一期缺失（待补·后端字段未上）或为 0 时无意义，返回 null */
export function percentChange(current: number, previous: number | undefined): number | null {
  if (previous === undefined || previous === 0) return null
  return (current - previous) / Math.abs(previous)
}

/** 等待时长：26 分钟 / 3 小时 / 2 天 */
export function formatDuration(seconds: number): string {
  if (seconds < 60) return '不到 1 分钟'
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes} 分钟`
  const hours = Math.floor(minutes / 60)
  if (hours < 48) return `${hours} 小时`
  return `${Math.floor(hours / 24)} 天`
}

/** 往返延迟：0.4 ms / 3 ms */
export function formatLatency(ms: number): string {
  return ms < 1 ? `${(Math.round(ms * 10) / 10).toFixed(1)} ms` : `${Math.round(ms)} ms`
}

/** 绝对时间（本地时区）：2026-09-24 08:30；无法解析时原样返回 */
export function formatDateTime(at: string): string {
  const d = new Date(at)
  if (Number.isNaN(d.getTime())) return at
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

// ===========================================================================
// 权限：每张卡按它的接口权限决定发不发请求（契约后台-01 各条目的「权限」行）
// ===========================================================================

export interface DashboardAccess {
  tasks: boolean
  backlog: boolean
  overview: boolean
  nodeTraffic: boolean
  userTraffic: boolean
  system: boolean
  activity: boolean
}

export function dashboardAccess(perms: Permissions): DashboardAccess {
  const metering = perms.has('metering.read')
  return {
    tasks: perms.has('ops.dashboard.read'),
    backlog: perms.has('ops.notification.read'),
    overview: perms.has('billing.ledger.read'),
    nodeTraffic: metering && perms.has('node.read'),
    userTraffic: metering && perms.has('iam.user.read'),
    system: perms.has('security.audit.read'),
    activity: perms.has('security.audit.read'),
  }
}

// ===========================================================================
// 「需要处理」卡片（GET v1/dashboard/tasks + 通知积压）
// ===========================================================================

export type Tone = 'ok' | 'warn' | 'danger' | 'info' | 'neutral'

export interface Target {
  module: ModuleKey
  tab: string | null
}

export interface TaskCard {
  key: string
  title: string
  count: number
  sub: string
  tone: Tone
  action: string
  /** 目标页当前管理员不可读时为 null，卡片不可点 */
  target: Target | null
  /** 这张卡是否真的需要人处理（页头「N 项」只数这些） */
  pending: boolean
  hint?: string
}

/** 目标页当前管理员可读才给链接，否则 null（点进去只会看到「无权限或不存在」） */
export function reachable(target: Target, perms: Permissions): Target | null {
  const ok = target.tab === null ? canReadModule(target.module, perms) : canRead(target.module, target.tab, perms)
  return ok ? target : null
}

const NOTIFY_HINT = '统计含邮件、Telegram、站内信；来自投递记录，未记录 worker 心跳，当前不可观测'

/** 通知积压卡：优先用冻结契约的 backlog 明细，拿不到时退回 tasks 条目里的摘要 */
function notificationCard(backlog: Backlog | undefined, item: Extract<TaskItem, { kind: 'notifications_backlog' }> | undefined, perms: Permissions): TaskCard | null {
  const target = reachable({ module: 'system', tab: 'notify' }, perms)
  const base = { key: 'notifications_backlog', title: '通知投递积压', action: '查看通道', target, hint: NOTIFY_HINT }
  if (backlog) {
    const c = backlog.counts
    const queued = c.ready + c.scheduled
    const retrying = c.ready_retry + c.scheduled_retry
    const backlogged = backlog.backlog_state === 'backlogged'
    const parts = backlogged
      ? [`最久等待 ${formatDuration(backlog.max_ready_lag_seconds)}`, retrying > 0 ? `重试中 ${formatCount(retrying)}` : '', c.failed_total > 0 ? `${formatCount(c.failed_total)} 封失败` : '']
      : ['无超阈值到期积压', c.failed_total > 0 ? `累计失败 ${formatCount(c.failed_total)}` : '']
    return { ...base, count: queued, sub: parts.filter(Boolean).join(' · '), tone: backlogged ? 'warn' : 'ok', pending: backlogged }
  }
  if (item) {
    const backlogged = item.backlog_state === 'backlogged'
    const sub = backlogged ? `积压超过阈值${item.failed_total > 0 ? ` · ${formatCount(item.failed_total)} 封失败` : ''}` : '无超阈值到期积压'
    return { ...base, count: item.queued, sub, tone: backlogged ? 'warn' : 'ok', pending: backlogged }
  }
  return null
}

function taskCard(item: TaskItem, perms: Permissions): TaskCard | null {
  const none = '目前没有'
  switch (item.kind) {
    case 'tickets_open': {
      const parts = [
        item.oldest_wait_seconds !== null && item.count > 0 ? `最久已等 ${formatDuration(item.oldest_wait_seconds)}` : '',
        item.high_priority > 0 ? `${item.high_priority} 个高优先级` : '',
      ].filter(Boolean)
      return {
        key: item.kind,
        title: '待处理工单',
        count: item.count,
        sub: item.count > 0 ? parts.join(' · ') : none,
        tone: item.count > 0 ? 'danger' : 'ok',
        action: '去回复',
        target: reachable({ module: 'tickets', tab: null }, perms),
        pending: item.count > 0,
      }
    }
    case 'withdrawals_pending': {
      const sum = item.amounts.filter((a) => a.amount !== 0).map((a) => formatMoney(a.amount, a.currency))
      return {
        key: item.kind,
        title: '提现待审核',
        count: item.count,
        sub: item.count > 0 && sum.length > 0 ? `合计 ${sum.join(' + ')}` : item.count > 0 ? '' : none,
        tone: item.count > 0 ? 'info' : 'ok',
        action: '去审核',
        target: reachable({ module: 'marketing', tab: 'commission' }, perms),
        pending: item.count > 0,
      }
    }
    case 'nodes_offline': {
      const first = item.sample[0]?.name
      const parts = [
        first ? (item.count > 1 ? `${first} 等` : first) : '',
        item.longest_offline_seconds !== null ? `最长 ${formatDuration(item.longest_offline_seconds)}` : '',
      ].filter(Boolean)
      return {
        key: item.kind,
        title: '离线节点',
        count: item.count,
        sub: item.count > 0 ? parts.join(' · ') : '全部在线',
        tone: item.count > 0 ? 'warn' : 'ok',
        action: '查看节点',
        target: reachable({ module: 'nodes', tab: 'nodes' }, perms),
        pending: item.count > 0,
      }
    }
    case 'orders_pending_stale':
      return {
        key: item.kind,
        title: '超时未支付订单',
        count: item.count,
        sub: item.count > 0 ? `超过 ${Math.round(item.threshold_seconds / 60)} 分钟，可能是回调丢失` : none,
        tone: item.count > 0 ? 'warn' : 'ok',
        action: '去核对',
        target: reachable({ module: 'billing', tab: 'orders' }, perms),
        pending: item.count > 0,
      }
    case 'ledger_drift':
      // 待补·前端：设计没有，但它是必须立刻查的严重信号——只在大于 0 时出现
      if (item.count === 0) return null
      return {
        key: item.kind,
        title: '账本漂移',
        count: item.count,
        sub: '余额与账本流水对不上的账户',
        tone: 'danger',
        action: '去核对',
        target: reachable({ module: 'billing', tab: null }, perms),
        pending: true,
      }
    case 'notifications_backlog':
      return null // 由 notificationCard 合并 backlog 明细后产出
  }
}

// 设计稿的卡片顺序；账本漂移排最前（最严重）
const TASK_ORDER = ['ledger_drift', 'tickets_open', 'withdrawals_pending', 'nodes_offline', 'orders_pending_stale', 'notifications_backlog']

export function taskCards(items: readonly TaskItem[] | undefined, backlog: Backlog | undefined, perms: Permissions): TaskCard[] {
  const cards: TaskCard[] = []
  for (const item of items ?? []) {
    const card = taskCard(item, perms)
    if (card) cards.push(card)
  }
  const notifyItem = items?.find((i): i is Extract<TaskItem, { kind: 'notifications_backlog' }> => i.kind === 'notifications_backlog')
  const notify = notificationCard(backlog, notifyItem, perms)
  if (notify) cards.push(notify)
  return cards.sort((a, b) => TASK_ORDER.indexOf(a.key) - TASK_ORDER.indexOf(b.key))
}

// ===========================================================================
// 经营 KPI
// ===========================================================================

/** 「较昨日」：yesterday 是待补·后端字段，缺失时不瞎算 */
export function kpiRevenueDelta(today: number, yesterday: number | undefined): { text: string; tone: Tone } {
  if (yesterday === undefined) return { text: '较昨日 —', tone: 'neutral' }
  if (yesterday === 0) return { text: today === 0 ? '昨日今日均无收入' : '昨日无收入', tone: 'neutral' }
  const change = percentChange(today, yesterday)!
  return { text: `较昨日 ${formatPercent(change)}`, tone: change > 0 ? 'ok' : change < 0 ? 'danger' : 'neutral' }
}

// ===========================================================================
// 收入趋势
// ===========================================================================

export interface Bar {
  key: string
  /** 0–100 的高度百分比；负值与 0 画成贴底细线 */
  height: number
  tip: string
  today: boolean
}

export interface RevenueSummary {
  total: number
  average: number
  /** 较上一区间，previous_total 缺失或为 0 时 null */
  delta: number | null
  empty: boolean
  bars: Bar[]
}

export function revenueSummary(points: readonly RevenuePoint[], currency: string, previousTotal: number | undefined): RevenueSummary {
  const total = points.reduce((sum, p) => sum + p.displayed_net, 0)
  const max = Math.max(1, ...points.map((p) => p.displayed_net))
  const bars = points.map((p, i) => {
    const actual = p.actual_credit - p.actual_debit
    const split = p.adjustment !== 0 ? ` · 实收 ${formatMoney(actual, currency)} · 调整 ${formatMoney(p.adjustment, currency)}` : ''
    return {
      key: p.date,
      height: p.displayed_net > 0 ? Math.round((p.displayed_net / max) * 100) : 0,
      tip: `${p.date.slice(5)} · ${formatMoney(p.displayed_net, currency)}${split}`,
      today: i === points.length - 1,
    }
  })
  return {
    total,
    average: points.length ? Math.round(total / points.length) : 0,
    delta: percentChange(total, previousTotal),
    empty: points.every((p) => p.displayed_net === 0 && p.actual_credit === 0 && p.actual_debit === 0 && p.adjustment === 0),
    bars,
  }
}

// ===========================================================================
// 注册与活跃
// ===========================================================================

export interface ActivityBar {
  key: string
  registered: number
  /** active_users 是待补·后端字段，缺失时为 null，不画活跃柱 */
  active: number | null
  tip: string
}

export interface ActivitySummary {
  registeredTotal: number
  /** 日活均值；后端未补 active_users 时 null */
  activeAverage: number | null
  empty: boolean
  bars: ActivityBar[]
}

export function activitySummary(points: readonly ActivityPoint[]): ActivitySummary {
  const regMax = Math.max(1, ...points.map((p) => p.registered))
  const hasActive = points.length > 0 && points.every((p) => p.active_users !== undefined)
  const actMax = Math.max(1, ...points.map((p) => p.active_users ?? 0))
  const registeredTotal = points.reduce((s, p) => s + p.registered, 0)
  const activeTotal = points.reduce((s, p) => s + (p.active_users ?? 0), 0)
  return {
    registeredTotal,
    activeAverage: hasActive ? Math.round(activeTotal / points.length) : null,
    empty: points.every((p) => p.registered === 0 && (p.active_users ?? 0) === 0 && p.logins === 0 && p.orders === 0),
    bars: points.map((p) => ({
      key: p.day,
      // 设计稿：注册柱最高 70%，活跃柱最高 100%，两者量级不同各自归一
      registered: Math.round((p.registered / regMax) * 70),
      active: hasActive ? Math.round(((p.active_users ?? 0) / actMax) * 100) : null,
      tip: [
        p.day,
        `注册 ${formatCount(p.registered)}`,
        p.active_users !== undefined ? `活跃 ${formatCount(p.active_users)}` : '',
        `登录 ${formatCount(p.logins)}`,
        `订单 ${formatCount(p.orders)}`,
        `独立 IP ${formatCount(p.unique_ips)}`,
      ]
        .filter(Boolean)
        .join(' · '),
    })),
  }
}

// ===========================================================================
// 系统状态（7 行组件 + 待补·前端的第 8 行「数据库备份」）
// ===========================================================================

export interface BackupSummary {
  state: ComponentState
  meta: string
}

function ageText(hours: number): string {
  return hours < 48 ? `${hours} 小时前` : `${Math.floor(hours / 24)} 天前`
}

export function backupSummary(b: BackupStatus): BackupSummary {
  // 读不到不等于没备份（后端注释：权限或 systemd 沙箱都可能挡住），状态记为未知
  if (!b.readable) return { state: 'unknown', meta: '读不到备份目录' }
  if (!b.latest) return { state: 'warn', meta: '还没有备份' }
  const problems = [
    b.stale ? '过期' : '',
    b.identity_configured === false ? '未配解密私钥' : '',
    b.offsite_configured === false ? '未配异地' : '',
    (b.missing_checksum ?? 0) > 0 ? `${b.missing_checksum} 份缺校验` : '',
  ].filter(Boolean)
  const age = b.latest_age_hours !== undefined ? `最近一份 ${ageText(b.latest_age_hours)}` : '最近一份时间未知'
  return problems.length ? { state: 'warn', meta: problems.join(' · ') } : { state: 'ok', meta: age }
}

export interface SystemRow {
  key: string
  name: string
  state: ComponentState
  meta: string
  /** 行 tooltip：后端给的 message 原文 */
  hint?: string
  /** 点击行为：跳模块或打开备份抽屉 */
  action?: { kind: 'go'; target: Target } | { kind: 'backup' }
}

export interface SystemView {
  rows: SystemRow[]
  degraded: number
  label: string
  tone: 'ok' | 'warn'
}

const COMPONENT_NAMES: Record<SystemComponent['key'], string> = {
  postgres: 'PostgreSQL 主库',
  // 契约映射：设计的「Redis」即 valkey
  valkey: 'Valkey 缓存',
  node_fabric: 'NativeCore 调度',
  payment_callbacks: '支付回调队列',
  mail: '邮件投递 · SMTP',
  telegram: 'Telegram',
  sse: 'SSE 推送',
  backup: '数据库备份',
}

function queueMeta(m: { queued: number; retrying: number; failed_total: number }): string {
  return [`${formatCount(m.queued)} 排队`, m.retrying > 0 ? `${formatCount(m.retrying)} 重试` : '', m.failed_total > 0 ? `${formatCount(m.failed_total)} 失败` : '']
    .filter(Boolean)
    .join(' · ')
}

function componentMeta(c: SystemComponent): string {
  const latency = c.latency_ms !== undefined ? formatLatency(c.latency_ms) : ''
  switch (c.key) {
    case 'postgres':
      return [latency, formatBytes(c.metrics.size_bytes), `连接 ${c.metrics.connections}/${c.metrics.max_connections}`].filter(Boolean).join(' · ')
    case 'valkey':
      return latency || '—'
    case 'node_fabric':
      return [`在线 ${c.metrics.online}/${c.metrics.total}`, c.metrics.config_lagging > 0 ? `${c.metrics.config_lagging} 个节点配置未同步` : ''].filter(Boolean).join(' · ')
    case 'payment_callbacks':
      return `${formatCount(c.metrics.pending)} 积压`
    case 'mail':
    case 'telegram':
      return queueMeta(c.metrics)
    case 'sse':
      return `${formatCount(c.metrics.connections)} 连接`
    case 'backup':
      return ''
  }
}

const ROW_ACTIONS: Partial<Record<SystemComponent['key'], Target>> = {
  node_fabric: { module: 'nodes', tab: 'nodes' },
  mail: { module: 'system', tab: 'notify' },
  telegram: { module: 'system', tab: 'notify' },
}

export function systemRows(status: SystemStatus, perms: Permissions): SystemView {
  const backup = backupSummary(status.backup)
  const backupRow: SystemRow = { key: 'backup', name: COMPONENT_NAMES.backup, state: backup.state, meta: backup.meta, action: { kind: 'backup' } }
  let rows: SystemRow[]
  if (status.components) {
    rows = status.components
      .filter((c) => c.key !== 'backup')
      .map((c) => {
        const target = ROW_ACTIONS[c.key]
        const go = target ? reachable(target, perms) : null
        return { key: c.key, name: COMPONENT_NAMES[c.key], state: c.state, meta: componentMeta(c), hint: c.message, action: go ? { kind: 'go' as const, target: go } : undefined }
      })
    // 备份行的状态以后端组件为准（它还看 missing_checksum 等），文案由完整的 backup 段给出
    const fromServer = status.components.find((c) => c.key === 'backup')
    rows.push(fromServer ? { ...backupRow, state: fromServer.state, hint: fromServer.message } : backupRow)
  } else {
    // 后端未补 components 前：只有数据库与备份两行来自现有字段
    const db = status.database
    rows = [
      'error' in db
        ? { key: 'postgres', name: COMPONENT_NAMES.postgres, state: 'down', meta: db.error }
        : { key: 'postgres', name: COMPONENT_NAMES.postgres, state: 'ok', meta: `${formatBytes(db.size_bytes)} · 连接 ${db.connections}/${db.max_connections}` },
      backupRow,
    ]
  }
  const degraded = rows.filter((r) => r.state === 'warn' || r.state === 'down').length
  const bad = status.state === 'degraded' || degraded > 0
  return { rows, degraded, label: bad ? `${Math.max(degraded, 1)} 项降级` : '全部正常', tone: bad ? 'warn' : 'ok' }
}

// ===========================================================================
// 流量排行
// ===========================================================================

export interface TrafficRow {
  key: string
  rank: number
  name: string
  /** 条宽 0–100，以第一名为 100 */
  width: number
  value: string
  tip: string
  /** 用户行点进用户详情（#/users/list/<id>），节点行没有单节点深链 */
  userId?: string
}

function ratio(part: string, whole: string): number {
  const w = BigInt(whole)
  if (w === 0n) return 0
  return Number((BigInt(part) * 100n) / w)
}

export function trafficRows(data: NodeTraffic | UserTraffic, now: Date = new Date()): TrafficRow[] {
  const top = data.items[0]?.total_bytes ?? '0'
  return data.items.map((item, i) => {
    const isNode = 'node_id' in item
    const name = isNode ? (item.display_name ?? item.name) : item.email_masked
    const tip = [
      `上行 ${formatBytes(item.upload_bytes)}`,
      `下行 ${formatBytes(item.download_bytes)}`,
      isNode ? `${item.report_count} 次上报` : `${item.subscription_count} 个订阅`,
      `最近 ${relativeTime(item.last_report_at, now)}`,
    ].join(' · ')
    return {
      key: isNode ? item.node_id : item.user_id,
      rank: i + 1,
      name,
      width: Math.max(2, ratio(item.total_bytes, top)),
      value: formatBytes(item.total_bytes),
      tip,
      userId: isNode ? undefined : item.user_id,
    }
  })
}

/** 卡片底部的小字：未归属流量、质量计数；全部未归属时给告警（DASH-01 的 all unattributed 状态） */
export function trafficNotes(data: NodeTraffic | UserTraffic): { notes: string[]; allUnattributed: boolean } {
  const notes: string[] = []
  const t = data.totals
  if (t.unattributed_bytes !== '0') notes.push(`未归属 ${formatBytes(t.unattributed_bytes)}`)
  const q = data.quality
  if (q.duplicate_report_count > 0) notes.push(`重复上报 ${q.duplicate_report_count}`)
  if (q.invalid_report_count > 0) notes.push(`无效上报 ${q.invalid_report_count}`)
  if (q.invalid_entry_count > 0) notes.push(`无效条目 ${q.invalid_entry_count}`)
  const allUnattributed = data.items.length === 0 && t.reported_bytes !== '0' && t.attributed_bytes === '0'
  return { notes, allUnattributed }
}
