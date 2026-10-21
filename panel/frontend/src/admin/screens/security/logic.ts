import type { QueryParams } from '../../../core/api'
import { formatDateTime } from '../../../core/format'
import type { RouteQuery } from '../../../core/router'
import type { TagTone } from '../../../ui'
import { ACTOR_KINDS, OUTCOMES, type AccessCategory, type AccessItem, type ActorKind, type AuditEvent, type Cluster, type ClusterDisabled, type ClusterReview, type Outcome, type Risk, type SkipReason, type SwitchRow } from './schemas'

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
  /** 邮箱（操作人是他，或对象是这个用户）与动作代号做包含匹配，ID 精确匹配对象或操作人；对象名不能搜 */
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

export const AUDIT_SEARCH_PLACEHOLDER = '邮箱、动作代号或对象 ID'

export const ACTOR_LABEL: Record<ActorKind, string> = { user: '用户', admin: '管理员', system: '系统', anonymous: '匿名' }

/** 操作人：有邮箱给邮箱；system 显示「系统」；其余按主体类型 */
export function actorLabel(e: Pick<AuditEvent, 'actor_email' | 'actor_kind'>): string {
  return e.actor_email ?? ACTOR_LABEL[e.actor_kind]
}

export const ACTION_NAMES: ReadonlyArray<readonly [string, string]> = [
  ['admin.admin_granted', '授予管理员'],
  ['admin.admin_revoked', '撤销管理员'],
  ['admin.password_reset', '重置后台口令'],
  ['announcement.published', '公告到点发布'],
  ['announcement.saved', '保存公告'],
  ['announcement.withdrawn', '撤回公告'],
  ['appearance.slot.save', '保存外观插槽'],
  ['appearance.theme.activate', '启用主题'],
  ['appearance.theme.delete', '删除主题'],
  ['appearance.theme.save', '保存主题'],
  ['audit.export', '导出审计日志'],
  ['auth.mail_link_login', '邮件链接登录'],
  ['auth.quick_login', '快捷登录'],
  ['balance.adjusted', '调整余额'],
  ['balance.topup_created', '建充值单'],
  ['commission.auto_transferred_to_balance', '佣金自动转余额'],
  ['commission.config_changed', '改佣金设置'],
  ['commission.entry_approved', '通过待复核佣金'],
  ['commission.entry_voided', '作废佣金'],
  ['commission.override_removed', '删单独佣金设置'],
  ['commission.override_set', '设单独佣金'],
  ['commission.transferred_to_balance', '佣金转余额'],
  ['content.archived', '归档内容'],
  ['content.version_created', '发布内容新版本'],
  ['coupon.batch_generated', '批量生成优惠券'],
  ['coupon.created', '新建优惠券'],
  ['coupon.status_change', '优惠券启停'],
  ['devices.settings_changed', '改设备策略'],
  ['feature_switch.toggle', '切换降级开关'],
  ['gift_card.batch_exported', '导出礼品卡批次'],
  ['gift_card.code_toggled', '礼品卡启停'],
  ['gift_card.codes_generated', '生成礼品卡'],
  ['gift_card.codes_report_exported', '导出礼品卡报表'],
  ['gift_card.redeemed', '兑换礼品卡'],
  ['gift_card_template.created', '新建礼品卡模板'],
  ['gift_card_template.updated', '改礼品卡模板'],
  ['home.settings_changed', '改门户首页设置'],
  ['late_payment.applied', '处理迟到付款'],
  ['mail.settings_changed', '改邮件设置'],
  ['node.activate', '上线节点'],
  ['node.batch_update', '批量改节点'],
  ['node.bootstrap_token.issue', '签发节点令牌'],
  ['node.create', '新建节点'],
  ['node.delete', '删除节点'],
  ['node.ech_disable', '关闭 ECH'],
  ['node.ech_generate', '生成 ECH 密钥'],
  ['node.identity.revoke', '吊销节点身份'],
  ['node.order_batch', '调整节点排序'],
  ['node.relay_create', '新建中转'],
  ['node.relay_delete', '删除中转'],
  ['node.relay_update', '改中转'],
  ['node.retire', '退役节点'],
  ['node.route_groups_update', '改节点路由组'],
  ['node.routing.global_publish', '发布全局路由'],
  ['node.routing.group_publish', '发布路由组路由'],
  ['node.routing.update', '改节点私有路由'],
  ['node.server_bind', '节点绑服务器'],
  ['node.server_unbind', '节点解绑服务器'],
  ['node.status_batch', '批量改节点状态'],
  ['node.traffic_reset', '重置节点流量'],
  ['node.update', '改节点'],
  ['node_pool.created', '新建节点池'],
  ['node_pool.deleted', '删除节点池'],
  ['node_pool.updated', '改节点池'],
  ['node_pool.user_groups_changed', '改池可见用户组'],
  ['notification_template.updated', '改通知模板'],
  ['notify.test_sent', '发测试通知'],
  ['order.cancelled', '取消订单'],
  ['order.created', '新购下单'],
  ['order.expired', '订单超时关闭'],
  ['order.manual_created', '后台人工开单'],
  ['order.marked_paid', '标记已支付'],
  ['order.payment_queried', '向渠道查单'],
  ['payment.quarantined', '付款隔离待查'],
  ['payment.succeeded', '付款成功入账'],
  ['payment_provider.create', '新建支付渠道'],
  ['payment_provider.toggle', '支付渠道启停'],
  ['payment_provider.update', '改支付渠道'],
  ['plan.archive', '归档套餐'],
  ['plan.create', '新建套餐'],
  ['plan.pools_update', '改套餐线路'],
  ['plan.prices_update', '改套餐价格'],
  ['plan.reset_update', '改流量重置规则'],
  ['plan.sales_update', '改销售设置'],
  ['plan.update', '改套餐信息'],
  ['plan.usage_update', '改套餐用量（存为草稿）'],
  ['plan_version.publish', '发布套餐版本'],
  ['plugin.hook.delete', '删 Webhook 钩子'],
  ['plugin.hook.save', '保存 Webhook 钩子'],
  ['registration.settings_changed', '改注册设置'],
  ['revenue.adjustment.create', '登记收入调整'],
  ['revenue.adjustment.reverse', '冲销收入调整'],
  ['risk.ip_cluster.disable', '禁用聚类账号'],
  ['risk.ip_cluster.mark_normal', '聚类标记正常'],
  ['route_group.create', '新建路由组'],
  ['route_group.delete', '删除路由组'],
  ['route_group.update', '改路由组'],
  ['security.source_ip_viewed', '查看明文 IP'],
  ['server.create', '新建服务器'],
  ['server.delete', '删除服务器'],
  ['server.enrollment.commit', '服务器接入完成'],
  ['server.identity.activate', '激活服务器身份'],
  ['server.identity.renew', '续签服务器证书'],
  ['server.status_change', '服务器状态变更'],
  ['server.update', '改服务器'],
  ['server.upgrade_requested', '请求升级节点程序'],
  ['session.refresh_reuse', '凭据重放吊销会话'],
  ['session.revoked_by_user', '用户踢下线会话'],
  ['settings.payment_updated', '改支付回调设置'],
  ['settings.purchase_updated', '改购买限制'],
  ['settings.support_updated', '改工单规则'],
  ['settings.telegram_extras_updated', '改 Telegram 扩展'],
  ['settings.telegram_updated', '改 Telegram 机器人'],
  ['signup.settings_changed', '改注册防护设置'],
  ['site.settings_changed', '改站点设置'],
  ['site.url_changed', '改主域名'],
  ['subscription.closed_by_account_deletion', '注销停订阅'],
  ['subscription.device_limit_changed', '改订阅设备上限'],
  ['subscription.domains_changed', '改订阅域名'],
  ['subscription.expired', '订阅到期'],
  ['subscription.extended', '订阅加时长'],
  // 不是审计动作，是访问日志给订阅拉取行起的名字
  ['subscription.fetch', '拉取订阅'],
  ['subscription.label_changed', '用户改订阅备注'],
  ['subscription.link_rotated', '用户换新订阅链接'],
  ['subscription.link_rotated_by_admin', '后台换发订阅链接'],
  ['subscription.period_end_set', '改订阅到期时间'],
  ['subscription.plan_change_created', '换套餐下单'],
  ['subscription.quota_limit_set', '改订阅流量上限'],
  ['subscription.renewal_closed', '续费期结束'],
  ['subscription.renewal_created', '续费下单'],
  ['subscription.settings_changed', '改订阅附加设置'],
  ['subscription.stack_created', '叠加下单'],
  ['subscription.traffic_granted', '订阅加流量'],
  ['subscription.trial_granted', '开通注册试用'],
  ['telegram.bound', '绑定 Telegram'],
  ['telegram.unbound', '解绑 Telegram'],
  ['ticket.assign', '指派工单'],
  ['ticket.auto_closed', '工单自动关闭'],
  ['ticket.close', '用户关闭工单'],
  ['ticket.create', '新建工单'],
  ['ticket.internal_note', '工单内部备注'],
  ['ticket.reply', '回复工单'],
  ['ticket.sla_escalate', '工单超时升级'],
  ['ticket.status_change', '改工单状态'],
  ['ticket.withdraw', '用户撤回工单'],
  ['ticket_macro.deleted', '删快捷回复'],
  ['ticket_macro.saved', '保存快捷回复'],
  ['traffic.manual_reset', '后台重置流量'],
  ['user.bulk_banned', '批量封禁'],
  ['user.bulk_exported', '导出用户'],
  ['user.bulk_generate_exported', '下载批量生成结果'],
  ['user.bulk_generate_requested', '提交批量生成'],
  ['user.bulk_generated', '批量生成完成'],
  ['user.bulk_mail_sent', '群发邮件'],
  ['user.created', '后台新建用户'],
  ['user.deleted', '注销账号'],
  ['user.email_changed', '改邮箱'],
  ['user.group_changed', '改用户组'],
  ['user.login', '登录成功'],
  ['user.login_failed', '登录失败'],
  ['user.logout', '退出登录'],
  ['user.note_changed', '改显示名与备注'],
  ['user.password_changed', '改密码'],
  ['user.password_reset', '找回密码完成'],
  ['user.password_reset_by_admin', '后台重置密码'],
  ['user.password_reset_requested', '申请找回密码'],
  ['user.reauthenticated', '二次认证'],
  ['user.referrer_changed', '改邀请人'],
  ['user.registered', '注册'],
  ['user.status_change', '改账号状态'],
  ['user_group.deleted', '删除用户组'],
  ['user_group.saved', '保存用户组'],
  ['user_group.status_changed', '用户组启停'],
  ['withdrawal.approve', '批准提现'],
  ['withdrawal.paid', '提现已打款'],
  ['withdrawal.reject', '驳回提现'],
  ['withdrawal.requested', '申请提现'],
]

const ACTION_LABEL = new Map(ACTION_NAMES)

export function actionLabel(code: string): string {
  return ACTION_LABEL.get(code) ?? code
}

export const RESOURCE_TYPE_LABEL: Readonly<Record<string, string>> = {
  announcement: '公告',
  commission_entry: '佣金条目',
  coupon: '优惠券',
  feature_switch: '降级开关',
  gift_card_batch: '礼品卡批次',
  gift_card_code: '礼品卡',
  gift_card_template: '礼品卡模板',
  ip_cluster_review: '聚类复核',
  late_payment_case: '迟到付款',
  ledger_transaction: '账本流水',
  node: '节点',
  node_pool: '节点池',
  node_relay: '中转',
  node_routing: '全局路由',
  node_set: '一批节点',
  notification_delivery: '通知投递',
  notification_template: '通知模板',
  notification_test: '测试通知',
  order: '订单',
  payment: '付款',
  payment_provider: '支付渠道',
  plan: '套餐',
  plan_version: '套餐版本',
  plugin_hook: 'Webhook 钩子',
  revenue_report_adjustment: '收入调整',
  route_group: '路由组',
  server: '服务器',
  server_set: '一批服务器',
  session: '会话',
  settings: '设置',
  site_slot: '外观插槽',
  site_theme: '主题',
  subscription: '订阅',
  subscription_domains: '订阅域名',
  ticket: '工单',
  ticket_macro: '快捷回复',
  user: '用户',
  user_generation_job: '批量生成任务',
  user_group: '用户组',
  withdrawal: '提现',
}

/** Go 目前只给用户（邮箱）与工单（工单号）解析可读名，其余对象只有类型与短 id */
export function objectLabel(e: Pick<AuditEvent, 'resource_label' | 'resource_type' | 'resource_id'>): string {
  if (e.resource_label) return e.resource_label
  if (e.resource_type) {
    const typeName = RESOURCE_TYPE_LABEL[e.resource_type] ?? e.resource_type
    return e.resource_id ? `${typeName} · ${e.resource_id.slice(0, 8)}` : typeName
  }
  if (e.resource_id) return e.resource_id.slice(0, 8)
  return '—'
}

export function reasonLabel(reason: string | null): string | null {
  if (reason === null) return null
  switch (reason) {
    case 'invalid_credentials':
      return '口令错误或账号不存在'
    case 'account_inactive':
      return '账号已停用或未激活'
    case 'not_admin':
      return '不是管理员却登后台'
    case 'staff_portal_blocked':
      return '后台账号登用户端被拒'
    default:
      return reason
  }
}

const auditOffset = (raw: string | null): number => {
  const n = Number.parseInt(raw ?? '', 10)
  return Number.isFinite(n) && n >= 0 ? n : 0
}

const isActorKind = (v: string): v is ActorKind => (ACTOR_KINDS as readonly string[]).includes(v)
const isOutcome = (v: string): v is Outcome => (OUTCOMES as readonly string[]).includes(v)

export function auditFromQuery(query: URLSearchParams): { filter: AuditFilter; offset: number } {
  const actorRaw = query.get('actor') ?? ''
  const outcomeRaw = query.get('outcome') ?? ''
  return {
    filter: {
      q: query.get('q') ?? '',
      action: query.get('action') ?? '',
      actor: isActorKind(actorRaw) ? actorRaw : '',
      outcome: isOutcome(outcomeRaw) ? outcomeRaw : '',
    },
    offset: auditOffset(query.get('o')),
  }
}

export function auditToQuery(f: AuditFilter, offset: number): RouteQuery {
  return {
    q: f.q || undefined,
    action: f.action || undefined,
    actor: f.actor || undefined,
    outcome: f.outcome || undefined,
    o: offset || undefined,
  }
}

export function authLabel(ctx: AuditEvent['auth_context']): { label: string; strong: boolean } {
  if (ctx === 'reauth') return { label: '二次认证', strong: true }
  if (ctx === 'session') return { label: '会话', strong: false }
  return { label: '—', strong: false }
}

export const OUTCOME_LABEL: Record<Outcome, string> = { success: '成功', failure: '失败', denied: '拒绝', partial: '部分成功' }
export const OUTCOME_TONE: Record<Outcome, TagTone> = { success: 'ok', failure: 'danger', denied: 'warn', partial: 'info' }

// ---------------------------------------------------------------------------
// 访问日志（安全事件，不是 HTTP 访问日志）
// ---------------------------------------------------------------------------
export const ACCESS_PAGE = 50
/** 实时监控的轮询间隔：审计表不在 SSE 监听里（契约） */
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

/** 订阅拉取的 result（迁移 00017 CHECK）：ok / expired / rate_limited / error / not_found */
const FETCH_RESULT: Record<string, string> = { ok: '成功', expired: '已过期提示', rate_limited: '被限流', error: '失败', not_found: '链接无效' }

export type AccessTone = 'ok' | 'warn' | 'bad' | 'none'

/** 结果色：成功绿；订阅拉取被限流（rate_limited）黄；其余非成功红；没有结果 none */
export function accessTone(item: Pick<AccessItem, 'outcome'>): AccessTone {
  const o = item.outcome
  if (o === undefined) return 'none'
  if (o === 'success' || o === 'ok') return 'ok'
  if (o === 'rate_limited') return 'warn'
  return 'bad'
}

export function accessOutcomeLabel(item: Pick<AccessItem, 'outcome'>): string {
  const o = item.outcome
  if (!o) return '—'
  return OUTCOME_LABEL[o as Outcome] ?? FETCH_RESULT[o] ?? o
}

export interface AccessAccount {
  /** 「匿名 · 试登 」这类前缀；本人操作为空串 */
  prefix: string
  label: string
  /** 能打开的用户 id：本人操作是 user_id，登录失败是被试的 target_user_id；都没有为 null */
  userId: string | null
}

export function accessAccount(item: Pick<AccessItem, 'user_email' | 'user_id' | 'target_user_email' | 'target_user_id'>): AccessAccount {
  if (item.user_email || item.user_id) {
    return {
      prefix: '',
      label: item.user_email ?? `用户 ${item.user_id!.slice(0, 8)}`,
      userId: item.user_id ?? null,
    }
  }
  if (item.target_user_email || item.target_user_id) {
    return {
      prefix: '匿名 · 试登 ',
      label: item.target_user_email ?? `用户 ${item.target_user_id!.slice(0, 8)}`,
      userId: item.target_user_id ?? null,
    }
  }
  return { prefix: '', label: '匿名', userId: null }
}

/** 行里的账号展示：本人邮箱/id，或匿名试登目标账号 */
export function accessUser(item: Pick<AccessItem, 'user_email' | 'user_id' | 'target_user_email' | 'target_user_id'>): string {
  const { prefix, label } = accessAccount(item)
  return prefix + label
}

/** 行提示：账号、动作、客户端与网络类型（契约：邮箱与 UA 放 tooltip） */
export function accessTooltip(item: AccessItem): string {
  const kind = item.network_kind ? NETWORK_LABEL[item.network_kind] : undefined
  const actionLine = item.action ? `${actionLabel(item.action)}（${item.action}）` : undefined
  return [accessUser(item), actionLine, item.user_agent, kind, formatDateTime(item.occurred_at)].filter(Boolean).join('\n')
}

const ACCESS_VIEW_SET = new Set(ACCESS_VIEWS.map(([v]) => v))

export function accessFromQuery(query: URLSearchParams): { filter: AccessFilter; offset: number } {
  const viewRaw = query.get('view') ?? 'all'
  return {
    filter: {
      view: ACCESS_VIEW_SET.has(viewRaw as AccessView) ? (viewRaw as AccessView) : 'all',
      ip: query.get('ip') ?? '',
      user: query.get('user') ?? '',
    },
    offset: auditOffset(query.get('o')),
  }
}

export function accessToQuery(f: AccessFilter, offset: number): RouteQuery {
  return {
    view: f.view === 'all' ? undefined : f.view,
    ip: f.ip || undefined,
    user: f.user || undefined,
    o: offset || undefined,
  }
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
const NETWORK_LABEL: Record<string, string> = { residential: '住宅宽带', datacenter: '机房', education: '教育网', mobile: '移动网络', loopback: '本机', private: '内网' }

export function clusterPlace(c: Pick<Cluster, 'geo' | 'network_kind'>): string {
  const kind = NETWORK_LABEL[c.network_kind]
  return [c.geo || '归属地未知', kind].filter(Boolean).join(' · ')
}

type ReviewState = { kind: 'open' } | { kind: 'expired' } | { kind: 'normal'; until: string } | { kind: 'disabled'; at: string }

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

/** 风控的分段筛选：待处理 = 后端默认列表（不含 30 天内标记正常的），已标记正常 / 全部取 include_reviewed=1 的列表 */
export type ClusterView = 'open' | 'done' | 'all'

/** 当前有效的「标记正常」（过期的不算） */
export function isMarkedNormal(c: Pick<Cluster, 'review'>, now: Date = new Date()): boolean {
  return reviewState(c.review, now).kind === 'normal'
}

/** 分段的列表与计数：open 来自默认列表，done / all 来自含已标记正常的列表；任一列表还没到时为 null */
export function clusterViews(open: readonly Cluster[] | undefined, all: readonly Cluster[] | undefined, now: Date = new Date()): Record<ClusterView, readonly Cluster[] | null> {
  return {
    open: open ?? null,
    done: all ? all.filter((c) => isMarkedNormal(c, now)) : null,
    all: all ?? null,
  }
}

const OFF = new Set(['suspended', 'banned'])
/** 能停用的成员：已停用 / 已封禁的后端会跳过（already_disabled），前端先不列；后台账号只有后端知道，照样交给它跳过 */
export function disableCandidates(c: Pick<Cluster, 'users'>) {
  return c.users.filter((u) => !OFF.has(u.status))
}

export const USER_STATUS_LABEL: Record<string, string> = { pending: '待验证', active: '正常', suspended: '已停用', banned: '已封禁', deletion_scheduled: '待注销', anonymized: '已注销', deleted: '已注销' }

const runes = (s: string) => [...s].length
/** 与 normalizeDisableInput 同键同文案：原因 5–500 字，账号 1–200 个 */
export function validateDisable(userIds: readonly string[], reason: string): Record<string, string> {
  const fields: Record<string, string> = {}
  const n = runes(reason.trim())
  if (n < 5 || n > 500) fields.reason = '原因必须为 5 到 500 字'
  if (userIds.length === 0 || userIds.length > 200) fields.user_ids = '请选择 1 到 200 个账号'
  return fields
}

const SKIP_LABEL: Record<SkipReason, string> = { self: '自己', administrator: '后台账号', not_member: '已不在聚类里', already_disabled: '已停用' }

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
  /** 缺行时的实际效果：R58 的四个新开关缺行视为开启（true），auth.registration 缺行即关闭（false） */
  whenMissing: boolean
}

/** 按显示顺序。D-A-3 已决（5.A.2、R102）：设计稿的「订阅下发使用缓存」不做；三个没有代码读取的开关（ops.bulk_export / ops.reports / node.autoscale）后端从种子删除，字典也不再收；三个核心项（auth.login / subscription.renewal / client.config_sync）没有代码读取，迁移 00064 删去，essential 一并取消 */
export const SWITCH_META: Readonly<Record<string, SwitchMeta>> = {
  'auth.registration': { title: '暂停新用户注册', desc: '注册页不可用，邀请链接同样失效。', note: '实际能否注册还取决于「通知与插件 · 注册与验证」里的注册模式。', whenMissing: false },
  'billing.checkout': { title: '暂停下单与支付', desc: '新购、续费、变更套餐、充值与发起支付一律拒绝；已发起支付的回调照常处理。', whenMissing: true },
  'marketing.giftcard.redeem': { title: '暂停礼品卡兑换', desc: '用户端兑换礼品卡一律拒绝，发现卡码泄露时使用。', whenMissing: true },
  'admin.writes': { title: '管理端只读模式', desc: '除降级开关、登录与二次认证、修改自己的密码外，后台所有写操作都会被拒绝。', whenMissing: true },
  'notify.email': { title: '暂停邮件投递', desc: '邮件留在队列里不发送，恢复后按序投递。', note: '暂停期间：要验证邮箱的注册、找回密码、邮件登录的第 1 步直接提示稍后再试，用户端也不显示找回密码与邮件登录入口；暂停前已发出的找回验证码照常可用。', whenMissing: true },
}

type SwitchKind = 'toggle' | 'missing'

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
}

const metaOrder = (code: string) => {
  const i = Object.keys(SWITCH_META).indexOf(code)
  return i < 0 ? Number.MAX_SAFE_INTEGER : i
}

export function switchViews(rows: readonly SwitchRow[]): SwitchView[] {
  const views: SwitchView[] = rows.map((r) => {
    const meta = SWITCH_META[r.code]
    const base = { code: r.code, title: meta?.title ?? r.code, desc: meta?.desc ?? '前端字典里还没有这个开关的说明；打开「已开启」即关闭这项功能（后端 enabled=false）。', note: meta?.note, reason: r.reason }
    return { ...base, kind: 'toggle', degraded: !r.enabled, status: r.enabled ? '关闭' : '已开启' }
  })
  const present = new Set(rows.map((r) => r.code))
  for (const [code, meta] of Object.entries(SWITCH_META)) {
    if (present.has(code)) continue
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
    })
  }
  return views.sort((a, b) => metaOrder(a.code) - metaOrder(b.code) || a.code.localeCompare(b.code))
}

/** 降级时原因必填（数据库 CHECK，后端回 409 而不是 422，所以前端先拦）；恢复时可选 */
export function validateSwitchReason(degrade: boolean, reason: string): string {
  return degrade && !reason.trim() ? '暂停时必须写原因，会写入审计' : ''
}

/** 切换请求体：degrade = 要进入降级（enabled=false） */
export function switchBody(degrade: boolean, reason: string): { enabled: boolean; reason: string } {
  return { enabled: !degrade, reason: reason.trim() }
}

/** 术语的一句话解释：表头与标签悬停（title）用，口径照 Go（identity/service/trace_merge.go、ops_access.go 的 clusterRisk） */
export const TERM_HINT = {
  datacenterHits: '来自机房 IP（云服务器、VPN 出口）的登录等行为与订阅拉取次数；正常用户多用家里宽带或手机网络，次数多要留意',
  uaKinds: '用过几种不同的客户端标识（User-Agent：某个浏览器、Clash、sing-box 各算一种），登录等行为与订阅拉取两路各自去重后相加',
  sharedIps: '这些 IP 也被别的账号用过（两路合并去重）：可能是一家人或同一公司出口，也可能是共享、倒卖账号',
  linkedIps: '登录等行为用过的 IP 与订阅拉取用过的 IP 合并去重后的个数',
  clusterRisk: '机房出口或 5 个以上账号为高风险，3–4 个为中风险，2 个为低风险',
} as const
