/**
 * [INPUT]: 依赖 ../../../core/api 的 QueryParams 类型，依赖 ../../../core/format 的 formatDateTime，依赖 ../../../ui 的 TagTone 类型，依赖 ./schemas 的类型与枚举
 * [OUTPUT]: 对外提供安全与运维页的纯函数与文案表：审计（筛选 → 查询串、导出查询串与日期校验、操作人 / 对象 / 认证 / 结果文字、时间）、访问日志（分段 → 分类与结果、查询串、行文字与是否错误、提示文字）、风控（风险与网络类型文字、复核状态、可停用成员、停用校验与结果摘要、写后缓存补丁）、降级开关（字典、行视图含缺行、切换请求与原因校验）
 * [POS]: admin/screens/security 的逻辑层，组件只做渲染与接线；security.test.ts 逐条守住。口径全部来自 Go：审计筛选与 auditCond 同键、访问日志分类表与 accessCategoryRules 同名、风险分级只展示后端给的 risk、开关极性 enabled = 可用（R58 缺行视为开启，auth.registration 反之）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { QueryParams } from '../../../core/api'
import { formatDateTime } from '../../../core/format'
import type { TagTone } from '../../../ui'
import type { AccessCategory, AccessItem, ActorKind, AuditEvent, Cluster, ClusterDisabled, ClusterReview, Outcome, Risk, SkipReason, SwitchRow } from './schemas'

const p2 = (n: number) => String(n).padStart(2, '0')
const trimmed = (v: string) => v.trim() || undefined

/** 同一天给 HH:mm:ss，跨天给 MM-DD HH:mm:ss（浏览器本地时区）；解析不了原样返回 */
export function clockTime(at: string, now: Date = new Date()): string {
  const d = new Date(at)
  if (Number.isNaN(d.getTime())) return at
  const time = `${p2(d.getHours())}:${p2(d.getMinutes())}:${p2(d.getSeconds())}`
  const sameDay = d.getFullYear() === now.getFullYear() && d.getMonth() === now.getMonth() && d.getDate() === now.getDate()
  return sameDay ? time : `${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${time}`
}

// ---------------------------------------------------------------------------
// 审计日志
// ---------------------------------------------------------------------------
export const AUDIT_PAGE = 50

export interface AuditFilter {
  /** 搜索框：动作与操作者邮箱做包含匹配，对象 id 精确匹配 */
  q: string
  /** 动作前缀，如 order. */
  action: string
  actor: ActorKind | ''
  outcome: Outcome | ''
}
export const EMPTY_AUDIT_FILTER: AuditFilter = { q: '', action: '', actor: '', outcome: '' }

const filterParams = (f: AuditFilter): QueryParams => ({ q: trimmed(f.q), action: trimmed(f.action), actor_kind: f.actor || undefined, outcome: f.outcome || undefined })

export function auditQuery(f: AuditFilter, offset: number): QueryParams {
  return { limit: AUDIT_PAGE, offset, ...filterParams(f) }
}

/** 导出：与列表同一组筛选，外加半开区间的起止日期（后端 to 含当天） */
export function exportQuery(f: AuditFilter, from: string, to: string): QueryParams {
  return { ...filterParams(f), from: trimmed(from), to: trimmed(to) }
}

const DAY_RE = /^\d{4}-\d{2}-\d{2}$/
const realDay = (v: string) => DAY_RE.test(v) && new Date(`${v}T00:00:00Z`).toISOString().startsWith(v)

/** 与 auditExportRange 同一套规则、同一组 fields 键与文案，前端先拦 */
export function validateExportRange(from: string, to: string): Record<string, string> {
  const fields: Record<string, string> = {}
  const f = from.trim()
  const t = to.trim()
  if (f && !realDay(f)) fields.from = '日期格式应为 YYYY-MM-DD'
  if (t && !realDay(t)) fields.to = '日期格式应为 YYYY-MM-DD'
  if (!fields.from && !fields.to && f && t && t < f) fields.to = '结束日期不能早于开始日期'
  return fields
}

export function hasAuditFilter(f: AuditFilter): boolean {
  return Boolean(f.q.trim() || f.action.trim() || f.actor || f.outcome)
}

/** agent 是客服、node 是节点代理进程（迁移 00012） */
export const ACTOR_LABEL: Record<ActorKind, string> = { user: '用户', admin: '管理员', system: '系统', agent: '客服', node: '节点代理', plugin: '插件', anonymous: '匿名' }

/** 操作人：有邮箱给邮箱；system 显示「系统」；其余按主体类型 */
export function actorLabel(e: Pick<AuditEvent, 'actor_email' | 'actor_kind'>): string {
  return e.actor_email ?? ACTOR_LABEL[e.actor_kind]
}

/** 对象：可读名优先，退回「类型 · 短 id」，没有对象给 — */
export function objectLabel(e: Pick<AuditEvent, 'resource_label' | 'resource_type' | 'resource_id'>): string {
  if (e.resource_label) return e.resource_label
  if (!e.resource_type) return e.resource_id ? e.resource_id.slice(0, 8) : '—'
  return e.resource_id ? `${e.resource_type} · ${e.resource_id.slice(0, 8)}` : e.resource_type
}

export function authLabel(ctx: AuditEvent['auth_context']): { label: string; strong: boolean } {
  if (ctx === 'reauth') return { label: '二次认证', strong: true }
  if (ctx === 'session') return { label: '会话', strong: false }
  return { label: '—', strong: false }
}

export const OUTCOME_LABEL: Record<Outcome, string> = { success: '成功', failure: '失败', denied: '拒绝', partial: '部分成功' }
export const OUTCOME_TONE: Record<Outcome, TagTone> = { success: 'ok', failure: 'danger', denied: 'danger', partial: 'warn' }

// ---------------------------------------------------------------------------
// 访问日志（安全事件，不是 HTTP 访问日志）
// ---------------------------------------------------------------------------
export const ACCESS_PAGE = 50
/** 实时尾随的轮询间隔：审计表不在 SSE 监听里（契约） */
export const TAIL_MS = 5000

export type AccessView = 'all' | 'error' | 'admin' | 'login' | 'register' | 'subscribe'
export const ACCESS_VIEWS: ReadonlyArray<readonly [AccessView, string]> = [
  ['all', '全部'],
  ['error', '仅错误'],
  ['admin', '管理端'],
  ['login', '登录'],
  ['register', '注册'],
  ['subscribe', '订阅拉取'],
]

export interface AccessFilter {
  view: AccessView
  ip: string
  user: string
}
export const EMPTY_ACCESS_FILTER: AccessFilter = { view: 'all', ip: '', user: '' }

/** 分段：「仅错误」是 outcome=error（非 success，订阅拉取 ok 以外都算），其余是 category */
export function accessQuery(f: AccessFilter, offset: number): QueryParams {
  return {
    limit: ACCESS_PAGE,
    offset,
    category: f.view === 'all' || f.view === 'error' ? undefined : f.view,
    outcome: f.view === 'error' ? 'error' : undefined,
    ip: trimmed(f.ip),
    user: trimmed(f.user),
  }
}

export const CATEGORY_LABEL: Record<AccessCategory, string> = {
  login: '登录',
  register: '注册',
  reset_password: '改密',
  order: '订单',
  payment: '支付',
  ticket: '工单',
  admin: '管理',
  other: '其他',
  subscribe: '订阅',
}

/** 订阅拉取的 result（00018）：ok / not_found / revoked / expired / rate_limited */
const FETCH_RESULT: Record<string, string> = { ok: '成功', not_found: '链接无效', revoked: '已吊销', expired: '已过期', rate_limited: '限流' }

export function isAccessError(item: Pick<AccessItem, 'outcome'>): boolean {
  return item.outcome !== undefined && item.outcome !== 'success' && item.outcome !== 'ok'
}

export function accessOutcomeLabel(item: Pick<AccessItem, 'outcome'>): string {
  const o = item.outcome
  if (!o) return '—'
  return OUTCOME_LABEL[o as Outcome] ?? FETCH_RESULT[o] ?? o
}

export function ipLabel(item: Pick<AccessItem, 'ip' | 'geo'>): string {
  if (!item.ip) return '—'
  return item.geo ? `${item.ip} · ${item.geo}` : item.ip
}

/** 行提示：账号、客户端与网络类型（契约：邮箱与 UA 放 tooltip） */
export function accessTooltip(item: AccessItem): string {
  const kind = item.network_kind ? NETWORK_LABEL[item.network_kind] : undefined
  return [item.user_email ?? (item.user_id ? `用户 ${item.user_id.slice(0, 8)}` : '匿名'), item.user_agent, kind, formatDateTime(item.occurred_at)].filter(Boolean).join('\n')
}

export function accessKey(item: AccessItem, index: number): string {
  return `${item.occurred_at}|${item.category}|${item.action ?? ''}|${item.ip ?? ''}|${index}`
}

// ---------------------------------------------------------------------------
// 风控：共享 IP 聚类
// ---------------------------------------------------------------------------
export const RISK_LABEL: Record<Risk, string> = { high: '高风险', mid: '中风险', low: '低风险' }
export const RISK_TONE: Record<Risk, TagTone> = { high: 'danger', mid: 'warn', low: 'neutral' }

/** geoip.NetworkKind；空串是判不出来 */
export const NETWORK_LABEL: Record<string, string> = { residential: '住宅宽带', datacenter: '机房', education: '教育网', mobile: '移动网络', loopback: '本机', private: '内网' }

export function clusterPlace(c: Pick<Cluster, 'geo' | 'network_kind'>): string {
  const kind = NETWORK_LABEL[c.network_kind]
  return [c.geo || '归属地未知', kind].filter(Boolean).join(' · ')
}

export type ReviewState = { kind: 'open' } | { kind: 'expired' } | { kind: 'normal'; until: string } | { kind: 'disabled'; at: string }

/** 复核结论：标记正常有 30 天有效期，过期即重新提示；停用没有有效期 */
export function reviewState(review: ClusterReview | null, now: Date = new Date()): ReviewState {
  if (!review) return { kind: 'open' }
  if (review.decision === 'disabled') return { kind: 'disabled', at: review.decided_at }
  if (review.expires_at && new Date(review.expires_at).getTime() <= now.getTime()) return { kind: 'expired' }
  return { kind: 'normal', until: review.expires_at ?? review.decided_at }
}

export function reviewText(state: ReviewState): string | null {
  switch (state.kind) {
    case 'normal':
      return `已标记为正常，${formatDateTime(state.until).slice(5, 10)} 前不再提示`
    case 'disabled':
      return `已于 ${formatDateTime(state.at).slice(5)} 禁用账号`
    case 'expired':
      return '标记正常已满 30 天，重新提示'
    default:
      return null
  }
}

const OFF = new Set(['suspended', 'banned'])
/** 能停用的成员：已停用 / 已封禁的后端会跳过（already_disabled），前端先不列；后台账号只有后端知道，照样交给它跳过 */
export function disableCandidates(c: Pick<Cluster, 'users'>) {
  return c.users.filter((u) => !OFF.has(u.status))
}

export const USER_STATUS_LABEL: Record<string, string> = { pending: '待验证', active: '正常', suspended: '已停用', banned: '已封禁', deletion_scheduled: '待注销', anonymized: '已注销' }

const runes = (s: string) => [...s].length
/** 与 normalizeDisableInput 同键同文案：原因 5–500 字，账号 1–200 个 */
export function validateDisable(userIds: readonly string[], reason: string): Record<string, string> {
  const fields: Record<string, string> = {}
  const n = runes(reason.trim())
  if (n < 5 || n > 500) fields.reason = '原因必须为 5 到 500 字'
  if (userIds.length === 0 || userIds.length > 200) fields.user_ids = '请选择 1 到 200 个账号'
  return fields
}

export const SKIP_LABEL: Record<SkipReason, string> = { self: '自己', administrator: '后台账号', not_member: '已不在聚类里', already_disabled: '已停用' }

export function disableSummary(r: ClusterDisabled): { message: string; ok: boolean } {
  const counts = new Map<SkipReason, number>()
  for (const s of r.skipped) counts.set(s.reason, (counts.get(s.reason) ?? 0) + 1)
  const why = [...counts].map(([reason, n]) => `${SKIP_LABEL[reason]} ${n}`).join('、')
  if (r.disabled === 0) return { message: `一个账号都没有禁用${why ? `：跳过 ${why}` : ''}`, ok: false }
  return { message: `已禁用 ${r.disabled} 个账号${why ? `，跳过 ${r.skipped.length} 个（${why}）` : ''}`, ok: true }
}

/** 写成功后就地补到缓存里：卡片按设计稿留在原位、显示结果行，下次重拉（默认列表不含已标记正常的）才消失 */
export function withReview(clusters: readonly Cluster[], key: string, review: ClusterReview): Cluster[] {
  return clusters.map((c) => (c.key === key ? { ...c, review } : c))
}

/** 停用成功：被停掉的成员标成 suspended；一个都没停成时后端不写结论，这里也不写 */
export function withDisabled(clusters: readonly Cluster[], key: string, requested: readonly string[], r: ClusterDisabled, now: Date = new Date()): Cluster[] {
  if (r.disabled === 0) return [...clusters]
  const skipped = new Set(r.skipped.map((s) => s.user_id))
  const done = new Set(requested.filter((id) => !skipped.has(id)))
  return clusters.map((c) =>
    c.key === key
      ? {
          ...c,
          users: c.users.map((u) => (done.has(u.id) ? { ...u, status: 'suspended' } : u)),
          review: { decision: 'disabled', decided_at: now.toISOString(), expires_at: null },
        }
      : c,
  )
}

// ---------------------------------------------------------------------------
// 降级开关（enabled = 功能可用；设计稿的「开启『暂停…』」= enabled=false）
// ---------------------------------------------------------------------------
interface SwitchMeta {
  title: string
  desc: string
  /** 连带影响，显示在说明下方 */
  note?: string
  /** 缺行时的实际效果：R58 的四个新开关缺行视为开启（true），auth.registration 缺行即关闭（false）；undefined = 缺行不显示 */
  whenMissing?: boolean
}

/** 按显示顺序：可切换的、核心能力。D-A-3 已决（5.A.2、R102）：设计稿的「订阅下发使用缓存」不做；三个没有代码读取的开关（ops.bulk_export / ops.reports / node.autoscale）后端从种子删除，字典也不再收 */
export const SWITCH_META: Readonly<Record<string, SwitchMeta>> = {
  'auth.registration': { title: '暂停新用户注册', desc: '注册页不可用，邀请链接同样失效。', note: '实际能否注册还取决于「通知与插件 · 注册与验证」里的注册模式。', whenMissing: false },
  'billing.checkout': { title: '暂停下单与支付', desc: '新购、续费、变更套餐、流量包、充值与发起支付一律拒绝；已发起支付的回调照常处理。', whenMissing: true },
  'marketing.giftcard.redeem': { title: '暂停礼品卡兑换', desc: '门户兑换礼品卡一律拒绝，发现卡码泄露时使用。', whenMissing: true },
  'admin.writes': { title: '管理端只读模式', desc: '除降级开关、登录与二次认证、修改自己的密码外，后台所有写操作都会被拒绝。', whenMissing: true },
  'notify.email': { title: '暂停邮件投递', desc: '邮件留在队列里不发送，恢复后按序投递。', note: '注册验证码也会滞留：开了注册邮箱验证时，暂停期间新用户注册实际走不通。', whenMissing: true },
  'auth.login': { title: '用户登录', desc: '核心能力，数据库约束保证不能关闭。' },
  'subscription.renewal': { title: '订阅续费', desc: '核心能力，数据库约束保证不能关闭。' },
  'client.config_sync': { title: '客户端配置同步', desc: '核心能力，数据库约束保证不能关闭。' },
}

export type SwitchKind = 'toggle' | 'essential' | 'missing'

export interface SwitchView {
  code: string
  title: string
  desc: string
  note?: string
  kind: SwitchKind
  /** 当前处于降级（功能被关掉）：enabled=false，缺行时按 whenMissing 推 */
  degraded: boolean
  reason: string | null
  status: string
  tone: 'danger' | 'muted' | 'ok'
}

const KIND_ORDER: Record<SwitchKind, number> = { toggle: 0, missing: 0, essential: 1 }
const metaOrder = (code: string) => {
  const i = Object.keys(SWITCH_META).indexOf(code)
  return i < 0 ? Number.MAX_SAFE_INTEGER : i
}

export function switchViews(rows: readonly SwitchRow[]): SwitchView[] {
  const views: SwitchView[] = rows.map((r) => {
    const meta = SWITCH_META[r.code]
    const base = { code: r.code, title: meta?.title ?? r.code, desc: meta?.desc ?? '前端字典里还没有这个开关的说明；打开「已开启」即关闭这项功能（后端 enabled=false）。', note: meta?.note, reason: r.reason }
    if (r.essential) return { ...base, kind: 'essential', degraded: false, status: '始终开启', tone: 'ok' }
    return { ...base, kind: 'toggle', degraded: !r.enabled, status: r.enabled ? '关闭' : '已开启', tone: r.enabled ? 'muted' : 'danger' }
  })
  const present = new Set(rows.map((r) => r.code))
  for (const [code, meta] of Object.entries(SWITCH_META)) {
    if (present.has(code) || meta.whenMissing === undefined) continue
    const degraded = !meta.whenMissing
    views.push({
      code,
      title: meta.title,
      desc: meta.desc,
      note: `这个租户没有这一行，${degraded ? '按已暂停处理' : '按未暂停处理'}，也不能在这里切换。`,
      kind: 'missing',
      degraded,
      reason: null,
      status: degraded ? '已开启' : '关闭',
      tone: degraded ? 'danger' : 'muted',
    })
  }
  return views.sort((a, b) => KIND_ORDER[a.kind] - KIND_ORDER[b.kind] || metaOrder(a.code) - metaOrder(b.code) || a.code.localeCompare(b.code))
}

/** 降级时原因必填（数据库 CHECK，后端回 409 而不是 422，所以前端先拦）；恢复时可选 */
export function validateSwitchReason(degrade: boolean, reason: string): string {
  return degrade && !reason.trim() ? '暂停时必须写原因，会写入审计' : ''
}

/** 切换请求体：degrade = 要进入降级（enabled=false） */
export function switchBody(degrade: boolean, reason: string): { enabled: boolean; reason: string } {
  return { enabled: !degrade, reason: reason.trim() }
}
