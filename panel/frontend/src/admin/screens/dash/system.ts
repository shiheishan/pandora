import { formatCount } from '../../../core/format'
import type { BackupStatus, ComponentState, SystemComponent, SystemStatus } from './api'
import type { Target } from './model'

// 仪表盘「系统状态」卡与备份抽屉的纯函数：组件状态 → 行（圆点、说明、指标、去处）。

export interface BackupSummary {
  state: ComponentState
  meta: string
}

function ageText(hours: number): string {
  return hours < 48 ? `${hours} 小时前` : `${Math.floor(hours / 24)} 天前`
}

/** 往返延迟：0.4 ms / 3 ms */
export function formatLatency(ms: number): string {
  return ms < 1 ? `${(Math.round(ms * 10) / 10).toFixed(1)} ms` : `${Math.round(ms)} ms`
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
  key: SystemComponent['key']
  name: string // 组件名，同 COMPONENT_NAMES
  tone: 'ok' | 'warn' | 'bad' | 'off' // state：ok→ok、warn→warn、down→bad、unknown→off
  attention: boolean // state !== 'ok'
  why: string // 说明
  metric: string // 指标
  action?: { kind: 'go'; target: Target } | { kind: 'backup' } // node_fabric → nodes/nodes，有离线时带 f=offline；mail、telegram → system/notify；backup → { kind: 'backup' }；其余无
}

export interface SystemView {
  rows: SystemRow[] // 8 行；attention 的在前，其余在后；两组内部保持 components 的顺序，backup 行放在各自组的最后
  attention: number // attention 行数
  label: string // attention > 0 ? `${attention} 项需要关注` : '全部正常'
  tone: 'ok' | 'warn' // attention > 0 ? 'warn' : 'ok'
}

const COMPONENT_NAMES: Record<SystemComponent['key'], string> = {
  postgres: 'PostgreSQL 主库',
  redis: 'Redis 缓存',
  node_fabric: '节点调度',
  payment_callbacks: '支付回调队列',
  mail: '邮件投递',
  telegram: 'Telegram',
  sse: 'SSE 推送',
  backup: '数据库备份',
}

const ROW_ACTIONS: Partial<Record<SystemComponent['key'], Target>> = {
  mail: { module: 'system', tab: 'notify' },
  telegram: { module: 'system', tab: 'notify' },
}

function rowTarget(c: SystemComponent): Target | undefined {
  if (c.key === 'node_fabric') {
    const { online, total } = c.metrics
    if (typeof online === 'number' && typeof total === 'number' && online < total) {
      return { module: 'nodes', tab: 'nodes', query: { f: 'offline' } }
    }
    return { module: 'nodes', tab: 'nodes' }
  }
  return ROW_ACTIONS[c.key]
}

const num = (v: number | null | undefined) => (v == null ? '—' : formatCount(v))

function stateTone(state: ComponentState): 'ok' | 'warn' | 'bad' | 'off' {
  switch (state) {
    case 'ok':
      return 'ok'
    case 'warn':
      return 'warn'
    case 'down':
      return 'bad'
    case 'unknown':
      return 'off'
  }
}

function computeWhy(c: SystemComponent): string {
  if (c.state === 'ok') return '正常'
  const msg = c.message?.trim()
  if (msg) return msg

  switch (c.key) {
    case 'node_fabric': {
      const { online, total, config_lagging } = c.metrics
      const parts: string[] = []
      if (typeof total === 'number' && typeof online === 'number') {
        const offline = total - online
        if (offline > 0) parts.push(`${offline} 个节点离线`)
      }
      const lagging = config_lagging ?? 0
      if (lagging > 0) parts.push(`${lagging} 个节点配置未同步`)
      if (parts.length > 0) return parts.join(' · ')
      break
    }
    case 'mail': {
      const failed = c.metrics.failed_total ?? 0
      if (failed > 0) return `${failed} 封失败`
      break
    }
    case 'telegram': {
      const failed = c.metrics.failed_total ?? 0
      if (failed > 0) return `${failed} 条失败`
      break
    }
  }

  switch (c.state) {
    case 'warn':
      return '需要关注'
    case 'down':
      return '不可用'
    case 'unknown':
      return '状态未知'
  }
}

function computeMetric(c: SystemComponent): string {
  const latency = c.latency_ms !== undefined ? formatLatency(c.latency_ms) : ''
  if (Object.keys(c.metrics).length === 0) {
    return latency || '—'
  }

  switch (c.key) {
    case 'postgres': {
      const m = c.metrics
      const conn = `${num(m.connections)}/${num(m.max_connections)} 连接`
      return latency ? `${latency} · ${conn}` : conn
    }
    case 'redis':
      return latency || '—'
    case 'node_fabric':
      return `${num(c.metrics.online)}/${num(c.metrics.total)} 在线`
    case 'payment_callbacks':
      return `${num(c.metrics.pending)} 积压`
    case 'mail':
    case 'telegram':
      return `${num(c.metrics.queued)} 排队`
    case 'sse':
      return `${num(c.metrics.connections)} 连接`
    case 'backup':
      return ''
  }
}

export function systemView(status: SystemStatus): SystemView {
  const backupServer = status.components.find((c) => c.key === 'backup')
  const backupSummaryResult = backupSummary(status.backup)
  const backupState = backupServer ? backupServer.state : backupSummaryResult.state
  const backupAttention = backupState !== 'ok'

  const backupRow: SystemRow = {
    key: 'backup',
    name: COMPONENT_NAMES.backup,
    tone: stateTone(backupState),
    attention: backupAttention,
    why: backupState === 'ok' ? '正常' : backupSummaryResult.meta,
    metric: status.backup.latest_age_hours !== undefined ? ageText(status.backup.latest_age_hours) : '—',
    action: { kind: 'backup' },
  }

  const otherComponents = status.components.filter((c) => c.key !== 'backup')

  const otherRows: SystemRow[] = otherComponents.map((c) => {
    const target = rowTarget(c)
    const attention = c.state !== 'ok'
    return {
      key: c.key,
      name: COMPONENT_NAMES[c.key],
      tone: stateTone(c.state),
      attention,
      why: computeWhy(c),
      metric: computeMetric(c),
      action: target ? { kind: 'go', target } : undefined,
    }
  })

  const attentionOthers = otherRows.filter((r) => r.attention)
  const healthyOthers = otherRows.filter((r) => !r.attention)

  const rows: SystemRow[] = []
  if (backupAttention) {
    rows.push(...attentionOthers, backupRow, ...healthyOthers)
  } else {
    rows.push(...attentionOthers, ...healthyOthers, backupRow)
  }

  const attentionCount = rows.filter((r) => r.attention).length
  const label = attentionCount > 0 ? `${attentionCount} 项需要关注` : '全部正常'
  const tone: 'ok' | 'warn' = attentionCount > 0 ? 'warn' : 'ok'

  return {
    rows,
    attention: attentionCount,
    label,
    tone,
  }
}
