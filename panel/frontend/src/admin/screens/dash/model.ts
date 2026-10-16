import { formatBytes, formatCount, formatMoney } from '../../../core/format'
import type { RouteQuery } from '../../../core/router'
import type { ModuleKey } from '../../modules'
import type { TaskItem } from '../../tasks'
import type { Backlog, BackupStatus, ComponentState, NodeTraffic, Overview, SystemComponent, SystemStatus } from './api'

// ===========================================================================
// 格式化
// ===========================================================================

/** 百分比变化：+12.4% / −3.1%（设计稿用数学减号）/ 0.0% */
export function formatPercent(ratio: number): string {
  const pct = Math.round(ratio * 1000) / 10
  if (pct === 0) return '0.0%'
  return `${pct > 0 ? '+' : '−'}${Math.abs(pct).toFixed(1)}%`
}

/** 相对变化；上一期为 0 时无意义，返回 null */
export function percentChange(current: number, previous: number): number | null {
  if (previous === 0) return null
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

// ===========================================================================
// 「需要处理」卡片（GET v1/dashboard/tasks + 通知积压）
// ===========================================================================

export type Tone = 'ok' | 'warn' | 'danger' | 'info' | 'neutral'

export interface Target {
  module: ModuleKey
  tab: string | null
  /** 目标页的地址筛选（如订单页 s=pending） */
  query?: RouteQuery
}

export interface TaskCard {
  key: string
  title: string
  count: number
  sub: string
  unit: string
  stats: { label: string; value: string }[]
  tone: Tone
  action: string
  /** 点卡片去处理的页面 */
  target: Target
  /** 这张卡是否真的需要人处理（页头「N 项」只数这些） */
  pending: boolean
  hint?: string
}

const NOTIFY_HINT = '统计含邮件、Telegram、站内信；来自投递记录，未记录 worker 心跳，当前不可观测'

/** 通知积压卡：优先用冻结契约的 backlog 明细，拿不到时退回 tasks 条目里的摘要 */
function notificationCard(backlog: Backlog | undefined, item: Extract<TaskItem, { kind: 'notifications_backlog' }> | undefined): TaskCard | null {
  const target: Target = { module: 'system', tab: 'notify' }
  const base = { key: 'notifications_backlog', title: '通知投递积压', action: '查看通道', target, hint: NOTIFY_HINT, unit: '条待投递' }
  if (backlog) {
    const c = backlog.counts
    const queued = c.ready + c.scheduled
    const retrying = c.ready_retry + c.scheduled_retry
    const backlogged = backlog.backlog_state === 'backlogged'
    const parts = backlogged
      ? [`最久等待 ${formatDuration(backlog.max_ready_lag_seconds)}`, retrying > 0 ? `重试中 ${formatCount(retrying)}` : '', c.failed_total > 0 ? `${formatCount(c.failed_total)} 封失败` : '']
      : ['无超阈值到期积压', c.failed_total > 0 ? `累计失败 ${formatCount(c.failed_total)}` : '']
    const stats = [
      { label: '最久等待', value: formatDuration(backlog.max_ready_lag_seconds) },
      { label: '累计失败', value: `${formatCount(c.failed_total)} 封` },
    ]
    return { ...base, count: queued, sub: parts.filter(Boolean).join(' · '), stats, tone: backlogged ? 'warn' : 'ok', pending: backlogged }
  }
  if (item) {
    const backlogged = item.backlog_state === 'backlogged'
    const sub = backlogged ? `积压超过阈值${item.failed_total > 0 ? ` · ${formatCount(item.failed_total)} 封失败` : ''}` : '无超阈值到期积压'
    const stats = [{ label: '累计失败', value: `${formatCount(item.failed_total)} 封` }]
    return { ...base, count: item.queued, sub, stats, tone: backlogged ? 'warn' : 'ok', pending: backlogged }
  }
  return null
}

function taskCard(item: TaskItem): TaskCard | null {
  const none = '目前没有'
  switch (item.kind) {
    case 'tickets_open': {
      const parts = [
        item.oldest_wait_seconds !== null && item.count > 0 ? `最久已等 ${formatDuration(item.oldest_wait_seconds)}` : '',
        item.high_priority > 0 ? `${item.high_priority} 个高优先级` : '',
      ].filter(Boolean)
      const oldest =
        item.oldest_wait_seconds === null || item.count === 0 ? '—' : formatDuration(item.oldest_wait_seconds)
      return {
        key: item.kind,
        title: '待处理工单',
        count: item.count,
        sub: item.count > 0 ? parts.join(' · ') : none,
        unit: '个未回复',
        stats: [
          { label: '高优先级', value: `${item.high_priority} 个` },
          { label: '最久等待', value: oldest },
        ],
        tone: item.count > 0 ? 'danger' : 'ok',
        action: '去回复',
        target: { module: 'tickets', tab: null },
        pending: item.count > 0,
      }
    }
    case 'withdrawals_pending':
      return {
        key: item.kind,
        title: '提现待审核',
        count: item.count,
        sub: item.count > 0 ? `合计 ${formatMoney(item.amount)}` : none,
        unit: '笔待审核',
        stats: [{ label: '合计金额', value: formatMoney(item.amount) }],
        tone: item.count > 0 ? 'info' : 'ok',
        action: '去审核',
        target: { module: 'marketing', tab: 'commission' },
        pending: item.count > 0,
      }
    case 'nodes_offline': {
      const sampleNames = item.sample.map((s) => s.name).join('、') || '—'
      const longest = item.longest_offline_seconds === null ? '—' : formatDuration(item.longest_offline_seconds)
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
        unit: '个节点离线',
        stats: [{ label: '最长离线', value: longest }, { label: '包括', value: sampleNames }],
        tone: item.count > 0 ? 'warn' : 'ok',
        action: '查看节点',
        target: { module: 'nodes', tab: 'nodes' },
        pending: item.count > 0,
      }
    }
    case 'orders_pending_stale':
      return {
        key: item.kind,
        title: '超时未支付订单',
        count: item.count,
        sub: item.count > 0 ? `超过 ${Math.round(item.threshold_seconds / 60)} 分钟，可能是回调丢失` : none,
        unit: '张超时未支付',
        stats: [{ label: '超时阈值', value: `${Math.round(item.threshold_seconds / 60)} 分钟` }],
        tone: item.count > 0 ? 'warn' : 'ok',
        action: '去核对',
        target: { module: 'billing', tab: 'orders', query: { s: 'pending' } },
        pending: item.count > 0,
      }
    case 'ledger_drift':
      if (item.count === 0) return null
      return {
        key: item.kind,
        title: '账本漂移',
        count: item.count,
        sub: '余额与账本流水对不上的账户',
        unit: '个账户对不上',
        stats: [],
        tone: 'danger',
        action: '去核对',
        target: { module: 'billing', tab: null },
        pending: true,
      }
    case 'notifications_backlog':
      return null
  }
}

const TASK_ORDER = ['ledger_drift', 'tickets_open', 'withdrawals_pending', 'nodes_offline', 'orders_pending_stale', 'notifications_backlog']

export function taskCards(items: readonly TaskItem[] | undefined, backlog: Backlog | undefined): TaskCard[] {
  const cards: TaskCard[] = []
  for (const item of items ?? []) {
    const card = taskCard(item)
    if (card) cards.push(card)
  }
  const notifyItem = items?.find((i): i is Extract<TaskItem, { kind: 'notifications_backlog' }> => i.kind === 'notifications_backlog')
  const notify = notificationCard(backlog, notifyItem)
  if (notify) cards.push(notify)
  return cards.sort((a, b) => TASK_ORDER.indexOf(a.key) - TASK_ORDER.indexOf(b.key))
}

const TASK_TONE_RANK: Record<Tone, number> = { danger: 0, warn: 1, info: 2, ok: 3, neutral: 4 }

export interface TaskBoard {
  hero: TaskCard | null
  rest: TaskCard[]
  pending: number
  urgent: number
  summary: string
}

export function taskBoard(cards: readonly TaskCard[]): TaskBoard {
  const pending = cards.filter((c) => c.pending).length
  const urgent = cards.filter((c) => c.pending && c.tone === 'danger').length
  const pendingCards = cards.filter((c) => c.pending)
  let hero: TaskCard | null = null
  if (pendingCards.length > 0) {
    hero = pendingCards.reduce((best, c) => (TASK_TONE_RANK[c.tone] < TASK_TONE_RANK[best.tone] ? c : best))
  }
  const rest = cards.filter((c) => c !== hero).sort((a, b) => {
    if (a.pending !== b.pending) return a.pending ? -1 : 1
    return 0
  })
  const summary = pending === 0 ? '0 项' : `${pending} 项` + (urgent > 0 ? ` · ${urgent} 项紧急` : '')
  return { hero, rest, pending, urgent, summary }
}

// ===========================================================================
// 经营 KPI
// ===========================================================================

/** 「较昨日」：昨日为 0 时不算百分比，只说有无收入 */
export function kpiRevenueDelta(today: number, yesterday: number): { text: string; tone: Tone } {
  if (yesterday === 0) return { text: today === 0 ? '昨日今日均无收入' : '昨日无收入', tone: 'neutral' }
  const change = percentChange(today, yesterday)!
  return { text: `较昨日 ${formatPercent(change)}`, tone: change > 0 ? 'ok' : change < 0 ? 'danger' : 'neutral' }
}

export interface KpiTile {
  key: string
  label: string
  value: string
  sub: string
  subTone?: Tone
  tip?: string
  target: Target
  muted: boolean
}

function signedMoney(amount: number): string {
  return `${amount > 0 ? '+' : ''}${formatMoney(amount)}`
}

export function kpiTiles(o: Overview | undefined, traffic: NodeTraffic | undefined): KpiTile[] {
  const tiles: KpiTile[] = []
  if (o) {
    const r = o.revenue
    const revEmpty = r.total === 0 && r.today === 0
    if (revEmpty) {
      tiles.push({
        key: 'revenue',
        label: '今日收入',
        value: '—',
        sub: '还没有订单',
        target: { module: 'billing', tab: 'orders' },
        muted: true,
      })
    } else {
      const delta = kpiRevenueDelta(r.today, r.yesterday)
      const tip = [
        r.adjustment_today !== 0 ? `含调整 ${signedMoney(r.adjustment_today)}` : '',
        `近 7 天 ${formatMoney(r.last_7_days)}`,
        `近 30 天 ${formatMoney(r.last_30_days)}`,
        `今日已付订单 ${formatCount(o.orders.paid_today)} · 待支付 ${formatCount(o.orders.pending)} · 今日失败 ${formatCount(o.orders.failed_today)}`,
      ]
        .filter(Boolean)
        .join('\n')
      tiles.push({
        key: 'revenue',
        label: '今日收入',
        value: formatMoney(r.today),
        sub: r.adjustment_today !== 0 ? `${delta.text} · 含调整` : delta.text,
        subTone: delta.tone,
        tip,
        target: { module: 'billing', tab: 'orders' },
        muted: false,
      })
    }
    const s = o.subscriptions
    const subsEmpty = s.active === 0 && s.expired === 0
    if (subsEmpty) {
      tiles.push({
        key: 'subs',
        label: '有效订阅',
        value: '0',
        sub: '还没有人购买套餐',
        target: { module: 'users', tab: 'list' },
        muted: true,
      })
    } else {
      tiles.push({
        key: 'subs',
        label: '有效订阅',
        value: formatCount(s.active),
        sub: `本周新增 ${formatCount(s.new_7_days)}`,
        tip: `7 天内到期 ${formatCount(s.expiring_7_days)} · 已过期 ${formatCount(s.expired)}`,
        target: { module: 'users', tab: 'list' },
        muted: false,
      })
    }
  }
  let trafficTile: KpiTile
  if (traffic === undefined) {
    trafficTile = {
      key: 'traffic',
      label: '流量 · 近 24 小时',
      value: '—',
      sub: '',
      tip: '节点原始上报的合计，不是计费账本',
      target: { module: 'nodes', tab: 'nodes' },
      muted: true,
    }
  } else if (traffic.totals.reported_bytes === '0') {
    trafficTile = {
      key: 'traffic',
      label: '流量 · 近 24 小时',
      value: '—',
      sub: '还没有节点上报',
      tip: '节点原始上报的合计，不是计费账本',
      target: { module: 'nodes', tab: 'nodes' },
      muted: true,
    }
  } else {
    trafficTile = {
      key: 'traffic',
      label: '流量 · 近 24 小时',
      value: formatBytes(traffic.totals.reported_bytes),
      sub: o ? `${formatCount(o.nodes.online)} / ${formatCount(o.nodes.total)} 节点在线` : '',
      tip: '节点原始上报的合计，不是计费账本',
      target: { module: 'nodes', tab: 'nodes' },
      muted: false,
    }
  }
  tiles.push(trafficTile)
  return tiles
}

// ===========================================================================
// 系统状态
// ===========================================================================

interface BackupSummary {
  state: ComponentState
  meta: string
}

function ageText(hours: number): string {
  return hours < 48 ? `${hours} 小时前` : `${Math.floor(hours / 24)} 天前`
}

export function backupSummary(b: BackupStatus): BackupSummary {
  if (!b.readable) return { state: 'unknown', meta: '读不到备份目录' }
  if (!b.latest) return { state: 'warn', meta: (b.count ?? 0) > 0 ? `${b.count} 份都恢复不了（缺封条或校验）` : '还没有备份' }
  const problems = [
    b.stale ? '过期' : '',
    b.identity_configured === false ? '未配解密私钥' : '',
    b.offsite_configured === false ? '未配异地' : '',
    (b.missing_checksum ?? 0) > 0 ? `${b.missing_checksum} 份缺校验` : '',
    (b.missing_seal ?? 0) > 0 ? `${b.missing_seal} 份缺封条` : '',
  ].filter(Boolean)
  const age = b.latest_age_hours !== undefined ? `最近一份 ${ageText(b.latest_age_hours)}` : '最近一份时间未知'
  return problems.length ? { state: 'warn', meta: problems.join(' · ') } : { state: 'ok', meta: age }
}

export interface SystemRow {
  key: string
  name: string
  short: string
  state: ComponentState
  meta: string
  hint?: string
  action?: { kind: 'go'; target: Target } | { kind: 'backup' }
}

export interface SystemView {
  rows: SystemRow[]
  attention: SystemRow[]
  healthy: SystemRow[]
  degraded: number
  label: string
  tone: 'ok' | 'warn'
  alert: { title: string; body: string } | null
  footer: string
}

const COMPONENT_NAMES: Record<SystemComponent['key'], string> = {
  postgres: 'PostgreSQL 主库',
  redis: 'Redis 缓存',
  node_fabric: 'NativeCore 调度',
  payment_callbacks: '支付回调队列',
  mail: '邮件投递',
  telegram: 'Telegram',
  sse: 'SSE 推送',
  backup: '数据库备份',
}

const COMPONENT_SHORT: Record<SystemComponent['key'], string> = {
  postgres: '主库',
  redis: '缓存',
  node_fabric: '节点调度',
  payment_callbacks: '支付回调',
  mail: '邮件投递',
  telegram: 'Telegram',
  sse: 'SSE 推送',
  backup: '数据库备份',
}

const num = (v: number | null | undefined) => (v == null ? '—' : formatCount(v))

function queueMeta(m: { queued?: number; retrying?: number; failed_total?: number }): string {
  return [`${num(m.queued)} 排队`, (m.retrying ?? 0) > 0 ? `${num(m.retrying)} 重试` : '', (m.failed_total ?? 0) > 0 ? `${num(m.failed_total)} 失败` : '']
    .filter(Boolean)
    .join(' · ')
}

function componentMeta(c: SystemComponent): string {
  const latency = c.latency_ms !== undefined ? formatLatency(c.latency_ms) : ''
  if (Object.keys(c.metrics).length === 0) return latency || '—'
  switch (c.key) {
    case 'postgres': {
      const m = c.metrics
      return [latency, m.size_bytes == null ? '' : formatBytes(m.size_bytes), `连接 ${num(m.connections)}/${num(m.max_connections)}`].filter(Boolean).join(' · ')
    }
    case 'redis':
      return latency || '—'
    case 'node_fabric': {
      const m = c.metrics
      return [`在线 ${num(m.online)}/${num(m.total)}`, (m.config_lagging ?? 0) > 0 ? `${m.config_lagging} 个节点配置未同步` : ''].filter(Boolean).join(' · ')
    }
    case 'payment_callbacks':
      return `${num(c.metrics.pending)} 积压`
    case 'mail':
    case 'telegram':
      return queueMeta(c.metrics)
    case 'sse':
      return `${num(c.metrics.connections)} 连接`
    case 'backup':
      return ''
  }
}

const ROW_ACTIONS: Partial<Record<SystemComponent['key'], Target>> = {
  node_fabric: { module: 'nodes', tab: 'nodes' },
  mail: { module: 'system', tab: 'notify' },
  telegram: { module: 'system', tab: 'notify' },
}

function systemAlert(attention: SystemRow[]): { title: string; body: string } | null {
  if (attention.length === 0) return null
  // 标题只点名确实降级（warn / down）的服务；状态未知的只在正文里说明
  const nonBackup = attention.filter((r) => r.key !== 'backup' && (r.state === 'warn' || r.state === 'down'))
  let title: string
  if (nonBackup.length === 0) {
    title = attention.some((r) => r.key === 'backup') ? '数据库备份需要处理' : '部分服务状态未知'
  } else if (nonBackup.length === 1) {
    title = `${nonBackup[0]!.short}受影响`
  } else if (nonBackup.length === 2) {
    title = `${nonBackup[0]!.short}与${nonBackup[1]!.short}受影响`
  } else {
    title = `${nonBackup.map((r) => r.short).join('、')}受影响`
  }
  const body = `${attention.map((r) => `${r.short}：${r.hint?.trim() ? r.hint : r.meta}`).join('；')}。`
  return { title, body }
}

export function systemRows(status: SystemStatus): SystemView {
  const backup = backupSummary(status.backup)
  const backupRow: SystemRow = {
    key: 'backup',
    name: COMPONENT_NAMES.backup,
    short: COMPONENT_SHORT.backup,
    state: backup.state,
    meta: backup.meta,
    action: { kind: 'backup' },
  }
  const rows: SystemRow[] = status.components
    .filter((c) => c.key !== 'backup')
    .map((c) => {
      const target = ROW_ACTIONS[c.key]
      return {
        key: c.key,
        name: COMPONENT_NAMES[c.key],
        short: COMPONENT_SHORT[c.key],
        state: c.state,
        meta: componentMeta(c),
        hint: c.message,
        action: target ? { kind: 'go' as const, target } : undefined,
      }
    })
  const fromServer = status.components.find((c) => c.key === 'backup')
  rows.push(fromServer ? { ...backupRow, state: fromServer.state, hint: fromServer.message } : backupRow)
  const attention = rows.filter((r) => r.state !== 'ok')
  const healthy = rows.filter((r) => r.state === 'ok')
  const degraded = attention.filter((r) => r.state === 'warn' || r.state === 'down').length
  const bad = status.state === 'degraded' || degraded > 0
  return {
    rows,
    attention,
    healthy,
    degraded,
    label: bad ? `${Math.max(degraded, 1)} 项降级` : '全部正常',
    tone: bad ? 'warn' : 'ok',
    alert: systemAlert(attention),
    footer: `共 ${rows.length} 项服务`,
  }
}
