import type { NodeRow } from './schemas'
import type { Tone } from './logic'

// ---------------------------------------------------------------------------
// 节点的真实运行状态（w4deliver）：pdnd 上报的运行原因 + 生效回执，后端读模型见
// nodefabric/node_runtime_view.go。界面只显示四种：运行中 / 待生效 / 生效失败：原因 / 降级：用缓存服务
// ---------------------------------------------------------------------------

export type RuntimeFields = Pick<
  NodeRow,
  | 'serving_status'
  | 'last_heartbeat_at'
  | 'stale'
  | 'effective_state'
  | 'runtime_status'
  | 'runtime_reason'
  | 'runtime_reason_node'
  | 'last_apply_failure'
  | 'port_conflict_node'
>

export interface RunState {
  label: string
  tone: Tone
  /** 悬停或详情里给的完整说明 */
  detail: string
}

const PORT_IN_USE = /^port_in_use:(\d{1,5})\/(tcp|udp):(.+)$/

/**
 * 原因码翻成中文（pdnd node/status.go 与 kernel/port_claims.go 的 RuntimeReason）：
 * port_in_use:<端口>/<tcp|udp>:<节点 id|other>、not_started、config_apply_failed、serving_cached_config。
 * holder 是后端按节点 id 查出的占用者名字（同一面板才有）；认不出的码原样显示
 */
export function runtimeReasonText(reason: string | null | undefined, holder?: string | null): string {
  if (!reason) return ''
  const m = PORT_IN_USE.exec(reason)
  if (m) {
    const [, num = '', l4 = '', owner = ''] = m
    const port = `端口 ${num}/${l4.toUpperCase()}`
    if (owner === 'other') return `${port} 被本机其他服务占用`
    return holder ? `${port} 被节点「${holder}」占用` : `${port} 被本面板的另一个节点占用`
  }
  switch (reason) {
    case 'not_started':
      return '入站没有起来'
    case 'config_apply_failed':
      return '新配置装不上，仍在用旧配置服务'
    case 'serving_cached_config':
      return '面板还没确认，正用本地缓存的配置服务'
  }
  return reason
}

/**
 * 列表与详情显示的运行状态。没有心跳、已停用、已退役的节点不显示（null）：那些由在线状态说明。
 * 优先级：生效失败（回执失败或节点报端口被占、没起来、新配置装不上）→ 降级（用缓存服务）→ 待生效 → 运行中
 */
export function runState(n: RuntimeFields): RunState | null {
  if (n.serving_status === 'retired' || !n.last_heartbeat_at) return null
  const reason = runtimeReasonText(n.runtime_reason, n.runtime_reason_node)
  if (n.runtime_status === 'degraded' && n.runtime_reason === 'serving_cached_config') {
    return { label: '降级：用缓存服务', tone: 'warn', detail: reason }
  }
  if (n.effective_state === 'failed' || n.runtime_status === 'degraded') {
    const why = reason || n.last_apply_failure?.detail || '原因未知'
    return { label: `生效失败：${why}`, tone: 'danger', detail: why }
  }
  if (n.effective_state === 'pending') return { label: '待生效', tone: 'info', detail: '新配置已下发，节点还没回执生效' }
  if (n.stale) return null
  return { label: '运行中', tone: 'ok', detail: '' }
}

export const sameRunState = (a: RunState | null, b: RunState | null): boolean =>
  a === b || (!!a && !!b && a.label === b.label && a.tone === b.tone && a.detail === b.detail)

/** 门禁上线前留下的同机端口冲突（迁移没建唯一索引时才会有）：提示管理员改端口或退役其一 */
export function portConflictText(n: Pick<NodeRow, 'port_conflict_node' | 'server_port'>): string {
  if (!n.port_conflict_node) return ''
  return `端口 ${n.server_port ?? ''} 与同一服务器上的节点「${n.port_conflict_node}」冲突，改端口或退役其一`
}

/** 生效回执的失败阶段（node_config_applications.phase）翻成中文 */
export function applyPhaseLabel(phase: string): string {
  switch (phase) {
    case 'failed':
      return '应用失败'
    case 'precheck_failed':
      return '预检失败'
    case 'health_failed':
      return '健康检查失败'
    case 'rolled_back':
      return '已回滚'
  }
  return phase
}

/** 复制弹窗的端口框：空串沿用原节点（undefined），合法端口返回数字，其余 'invalid' */
export function copyPortValue(raw: string): number | undefined | 'invalid' {
  const v = raw.trim()
  if (!v) return undefined
  if (!/^\d{1,5}$/.test(v)) return 'invalid'
  const n = Number(v)
  return n >= 1 && n <= 65535 ? n : 'invalid'
}
