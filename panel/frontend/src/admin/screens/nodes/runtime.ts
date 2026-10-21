import type { NodeCert, NodeRow } from './schemas'
import type { Tone } from './logic'

// ---------------------------------------------------------------------------
// 节点的真实运行状态：pdnd 上报的运行原因 + 生效回执，后端读模型见
// node/service 的 node_list_delivery.go。界面只显示四种：运行中 / 待生效 / 生效失败：原因 / 降级：用缓存服务
// 另有证书相关：cert_pending（等待证书）、cert_expired（证书已过期续期失败）
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
>

export interface RunState {
  label: string
  tone: Tone
  /** 悬停或详情里给的完整说明 */
  detail: string
}

const PORT_IN_USE = /^port_in_use:(\d{1,5})\/(tcp\+udp|tcp|udp):(.+)$/

/**
 * 原因码翻成中文（pdnd node/status.go 与 kernel/port_claims.go 的 RuntimeReason）：
 * port_in_use:<端口>/<tcp|udp|tcp+udp>:<节点 id|other>（tcp+udp 是 Shadowsocks 同端口两种都听）、not_started、config_apply_failed、serving_cached_config、
 * cert_pending、cert_expired。holder 是后端按节点 id 查出的占用者名字（同一面板才有）；认不出的码原样显示
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
    case 'cert_pending':
      return "等待证书：节点端正在向 Let's Encrypt 申请"
    case 'cert_expired':
      return '证书已过期：续期一直失败，客户端会校验失败'
  }
  return reason
}

/**
 * 列表与详情显示的运行状态。没有心跳、已停用、已退役的节点不显示（null）：那些由在线状态说明。
 * 优先级：证书等待 / 过期 → 生效失败（回执失败或节点报端口被占、没起来、新配置装不上）→ 降级（用缓存服务）→ 待生效 → 运行中
 */
export function runState(n: RuntimeFields): RunState | null {
  if (n.serving_status === 'retired' || !n.last_heartbeat_at) return null
  const reason = runtimeReasonText(n.runtime_reason, n.runtime_reason_node)
  if (n.runtime_reason === 'cert_pending') {
    return {
      label: '等待证书',
      tone: 'info',
      detail: '节点端正在申请证书，申请到就自动生效；进度看「证书」一栏',
    }
  }
  if (n.runtime_reason === 'cert_expired') {
    return { label: '证书已过期', tone: 'danger', detail: runtimeReasonText('cert_expired') }
  }
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

/** 节点端证书申请失败的原因码（pdnd/certs/reason.go）→ 中文；认不出的码原样显示 */
const CERT_REASON_TEXT: Readonly<Record<string, string>> = {
  challenge_port_busy: '本机 80 端口被别的程序占用，验证没法进行',
  validation_failed: "Let's Encrypt 连不上本机 80 端口：检查防火墙 / 安全组是否放行 80，域名是否解析到这台机器",
  dns_failed: '域名解析不到：检查 DNS 记录',
  caa_forbidden: "域名的 CAA 记录不允许 Let's Encrypt 签发",
  rejected_identifier: "Let's Encrypt 拒绝给这个名字签发（内网地址、保留域名或被封禁）",
  rate_limited: "触发了 Let's Encrypt 的频率限制，到点后自动重试",
  paused: "这个名字连续失败太多次，被 Let's Encrypt 暂停申请；修好后按运维手册解除",
  profile_unsupported: '证书颁发机构不支持 IP 证书',
  acme_unreachable: "节点连不上 Let's Encrypt：检查节点机的出站网络",
  account_failed: "向 Let's Encrypt 注册账户失败",
  store_failed: '证书写盘失败：检查节点状态目录的权限与磁盘空间',
  unknown: '未知错误，见详情',
}

export function certReasonText(code: string | null | undefined): string {
  if (!code) return ''
  return CERT_REASON_TEXT[code] ?? code
}

export interface CertBadge {
  label: string
  tone: Tone
  /** 悬停或详情里的完整说明（含失败详情与下次重试时间） */
  detail: string
}

/** 快到期的门槛：IP 证书约 6 天一换，剩 2 天；域名与手填证书剩 14 天 */
export const CERT_WARN_DAYS = { ip: 2, other: 14 } as const

const MS_PER_DAY = 86400000

/** 本地时间 MM-DD HH:mm，解析失败则原样返回 */
function formatLocalMdHm(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

function certDetailPrefix(cert: NodeCert): string {
  if (cert.mode === 'acme' && cert.identifier) return `证书名字 ${cert.identifier}`
  if (cert.mode === 'file') return '手填证书'
  return ''
}

function joinCertDetail(cert: NodeCert, parts: string[]): string {
  const head = certDetailPrefix(cert)
  const body = parts.filter(Boolean).join('；')
  return head ? (body ? `${head}；${body}` : head) : body
}

/**
 * 证书徽标。cert 为 null 返回 null。now 是当前时刻（毫秒），测试注入。
 */
export function certBadge(cert: NodeRow['cert'], now: number): CertBadge | null {
  if (!cert) return null

  const reasonZh = cert.error_code ? certReasonText(cert.error_code) : ''
  const rawPart = cert.error_detail?.trim() ? `原话：${cert.error_detail.trim()}` : ''
  const retryPart = cert.next_attempt_at ? `下次重试 ${formatLocalMdHm(cert.next_attempt_at)}` : ''
  const errParts = [reasonZh, rawPart, retryPart]

  const notAfterMs = cert.not_after ? Date.parse(cert.not_after) : NaN
  const hasCert = cert.not_after !== null && !Number.isNaN(notAfterMs)

  if (!hasCert && !cert.error_code) {
    return {
      label: '证书申请中',
      tone: 'info',
      detail: joinCertDetail(cert, errParts),
    }
  }
  if (!hasCert && cert.error_code) {
    return {
      label: '证书申请失败',
      tone: 'danger',
      detail: joinCertDetail(cert, errParts),
    }
  }

  const daysLeft = Math.ceil((notAfterMs - now) / MS_PER_DAY)
  const warnDays = cert.identifier_kind === 'ip' ? CERT_WARN_DAYS.ip : CERT_WARN_DAYS.other

  if (notAfterMs <= now) {
    const parts = cert.error_code ? errParts : []
    return {
      label: '证书已过期',
      tone: 'danger',
      detail: joinCertDetail(cert, parts),
    }
  }

  if (cert.error_code) {
    return {
      label: `续期失败，${daysLeft} 天内到期`,
      tone: 'warn',
      detail: joinCertDetail(cert, errParts),
    }
  }

  if (daysLeft <= warnDays) {
    return {
      label: `${daysLeft} 天内到期`,
      tone: 'warn',
      detail: joinCertDetail(cert, []),
    }
  }

  return {
    label: `证书有效，${daysLeft} 天内到期`,
    tone: 'ok',
    detail: joinCertDetail(cert, []),
  }
}
