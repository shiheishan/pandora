/**
 * [INPUT]: 依赖 ../../../core/format 的 formatBytes，依赖 ./api 的类型与枚举
 * [OUTPUT]: 对外提供 STATUS_FILTERS / StatusFilter / isStatusFilter、listParams、USER_STATUS_VIEW、SUB_STATUS_VIEW、ORDER_STATUS_VIEW、RISK_VIEW、INTERVAL_LABELS、orderWhat、initial、shortId、expiryView、trafficView、deviceView、deviceLimitLabel、trafficQuota、currentSubscription、liveSubscriptions、isLiveSub、parseYuan、passwordProblem、REASON_MIN、Tone；④ 的 groupBlocker / groupRefs、BULK_STATUS / BULK_EXPIRY / BulkForm / bulkFilter / exportQuery / MAIL_MAX、GenerateForm / generateProblems / generatedRows、nearLimit / pips、RESET_REASON_VIEW / resetActor / noteProblem / NOTE_MAX / resettableSub / exactEmail
 * [POS]: admin/screens/users 的纯逻辑：契约后台-03 的账号状态 / 订阅态 / 订单状态映射、状态分段到后端 query、「套餐 · 到期」「本期流量」「设备」三列的文案与色、当前订阅的挑法（与后端 currentSubscriptionSQL 同一口径）、调账金额（元 → 分）与密码策略的前端预检；④ 的用户组删除拦截、批量筛选表单到 BulkFilter、批量生成的前端校验（与 adminops.GenerateUsers 同规则）、接近上限的订阅、重置日志的方式与操作人文案、手动重置挑哪条订阅（与 billing.ManualResetTraffic 同口径）、按邮箱精确匹配；不碰 React 与网络，model.test.ts 覆盖
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { formatBytes } from '../../../core/format'
import type { BulkFilter, OnlineDevice, OrderRow, OrderStatus, Quota, ResetLog, ResetReason, RiskLevel, SubscriptionRow, SubStatus, UserGroup, UserRow, UserStatus, UsersParams } from './api'

export type Tone = 'ok' | 'warn' | 'danger' | 'info' | 'neutral'

/** 调账、重置密码、换发订阅链接的原因至少 5 个字（后端 fields.reason） */
export const REASON_MIN = 5

// ===========================================================================
// 状态分段 → 后端 query（契约映射）
// ===========================================================================
export const STATUS_FILTERS = [
  ['all', '全部'],
  ['active', '正常'],
  ['disabled', '已禁用'],
  ['expired', '已过期'],
] as const
export type StatusFilter = (typeof STATUS_FILTERS)[number][0]

export function isStatusFilter(v: string | null): v is StatusFilter {
  return STATUS_FILTERS.some(([k]) => k === v)
}

/** 「已过期」是订阅态（sub_state），不是账号状态；「全部用户组」不带 group_id，「未分组」传 none */
export function listParams(filter: StatusFilter, group: string, q: string, offset: number): UsersParams {
  const params: UsersParams = { offset }
  const text = q.trim()
  if (text) params.q = text
  if (group) params.group_id = group
  if (filter === 'active') params.status = 'active'
  if (filter === 'disabled') params.status = 'suspended,banned'
  if (filter === 'expired') params.sub_state = 'expired'
  return params
}

// ===========================================================================
// 标签映射
// ===========================================================================
export const USER_STATUS_VIEW: Record<UserStatus, { label: string; tone: Tone }> = {
  active: { label: '正常', tone: 'ok' },
  suspended: { label: '已禁用', tone: 'danger' },
  // 设计只有「已禁用」；封禁在详情另标
  banned: { label: '已禁用', tone: 'danger' },
  pending: { label: '待验证', tone: 'neutral' },
  deletion_scheduled: { label: '注销中', tone: 'neutral' },
  anonymized: { label: '已匿名', tone: 'neutral' },
}

export const SUB_STATUS_VIEW: Record<SubStatus, { label: string; tone: Tone }> = {
  pending: { label: '待开通', tone: 'neutral' },
  trialing: { label: '试用中', tone: 'info' },
  active: { label: '生效中', tone: 'ok' },
  past_due: { label: '待续费', tone: 'warn' },
  grace: { label: '宽限期', tone: 'warn' },
  paused: { label: '已暂停', tone: 'neutral' },
  cancelled: { label: '已取消', tone: 'neutral' },
  expired: { label: '已过期', tone: 'danger' },
}

/** 契约后台-05 公共映射：9 个后端状态 → 设计 3 个 + 已过期 + 退款 */
export const ORDER_STATUS_VIEW: Record<OrderStatus, { label: string; tone: Tone }> = {
  draft: { label: '待支付', tone: 'warn' },
  pending_payment: { label: '待支付', tone: 'warn' },
  processing: { label: '待支付', tone: 'warn' },
  paid: { label: '已支付', tone: 'ok' },
  fulfilled: { label: '已支付', tone: 'ok' },
  cancelled: { label: '已取消', tone: 'neutral' },
  expired: { label: '已过期', tone: 'neutral' },
  refunded: { label: '已退款', tone: 'info' },
  partially_refunded: { label: '部分退款', tone: 'info' },
}

export const RISK_VIEW: Record<RiskLevel, { label: string; tone: Tone }> = {
  trusted: { label: '可信', tone: 'ok' },
  normal: { label: '正常', tone: 'neutral' },
  elevated: { label: '偏高', tone: 'warn' },
  high: { label: '高', tone: 'danger' },
}

export const INTERVAL_LABELS: Record<string, string> = {
  day: '日付',
  week: '周付',
  month: '月付',
  quarter: '季付',
  year: '年付',
  one_time: '一次性',
}

const KIND_LABELS: Record<OrderRow['kind'], string> = {
  new: '新购',
  renewal: '续费',
  upgrade: '变更套餐',
  downgrade: '变更套餐',
  addon: '流量包',
  topup: '余额充值',
  manual: '人工',
}

/** 订单「买了什么」：套餐名 · 周期（多项时提示还有几项）；充值单没有套餐名 */
export function orderWhat(o: Pick<OrderRow, 'kind' | 'plan_name' | 'interval' | 'interval_count' | 'item_count'>): string {
  const name = o.plan_name || KIND_LABELS[o.kind]
  const every = INTERVAL_LABELS[o.interval]
  const cycle = every ? (o.interval_count > 1 ? `${o.interval_count} × ${every}` : every) : ''
  const more = o.item_count > 1 ? ` 等 ${o.item_count} 项` : ''
  return [name, cycle].filter(Boolean).join(' · ') + more
}

export function initial(email: string, displayName: string | null): string {
  return (displayName?.trim() || email).slice(0, 1).toUpperCase()
}

/** 设计稿的 #10482 在后端不存在：显示 uuid 前 8 位（契约本网关通用事实） */
export function shortId(id: string): string {
  return id.slice(0, 8)
}

// ===========================================================================
// 列表三列
// ===========================================================================
const DAY = 86_400_000

export function expiryView(periodEnd: string | null, now: Date): { text: string; tone: Tone } {
  if (!periodEnd) return { text: '长期有效', tone: 'neutral' }
  const days = Math.ceil((new Date(periodEnd).getTime() - now.getTime()) / DAY)
  if (days < 0) return { text: `已过期 ${-days} 天`, tone: 'danger' }
  if (days === 0) return { text: '今天到期', tone: 'warn' }
  return { text: `${days} 天后到期`, tone: days <= 7 ? 'warn' : 'neutral' }
}

export function trafficView(limit: number | null, consumed: number): { text: string; percent: number; tone: Tone } {
  if (limit === null) return { text: `${formatBytes(consumed)} / 不限`, percent: 0, tone: 'neutral' }
  const percent = limit > 0 ? Math.min(100, Math.round((consumed / limit) * 100)) : 100
  return { text: `${formatBytes(consumed)} / ${formatBytes(limit)}`, percent, tone: percent >= 90 ? 'danger' : percent >= 70 ? 'warn' : 'neutral' }
}

/** 设备列：online / limit，limit 为 0 表示不限（契约） */
export function deviceView(online: number, limit: number): { text: string; tone: Tone } {
  if (limit === 0) return { text: `${online}/不限`, tone: 'neutral' }
  return { text: `${online}/${limit}`, tone: online > limit ? 'danger' : online === limit ? 'warn' : 'neutral' }
}

/** 抽屉里的设备上限显示值：覆盖 ?? 套餐规定 ?? 不限；0 同样是不限 */
export function deviceLimitLabel(s: Pick<SubscriptionRow, 'device_limit_override' | 'plan_max_devices'>): { text: string; source: 'override' | 'plan' | 'none' } {
  const value = s.device_limit_override ?? s.plan_max_devices
  const source = s.device_limit_override !== null ? 'override' : s.plan_max_devices !== null ? 'plan' : 'none'
  return { text: value === null || value === 0 ? '不限' : String(value), source }
}

export function trafficQuota(quotas: readonly Quota[]): Quota | undefined {
  return quotas.find((q) => q.metric === 'traffic.bytes')
}

// ===========================================================================
// 当前订阅：与后端 currentSubscriptionSQL 同一口径——还在用的优先，其次到期最晚、最近创建
// （详情的 subscriptions 已按 created_at 倒序）
// ===========================================================================
const LIVE: ReadonlySet<SubStatus> = new Set(['active', 'trialing', 'grace', 'past_due'])

export function isLiveSub(status: SubStatus): boolean {
  return LIVE.has(status)
}

export function currentSubscription<T extends Pick<SubscriptionRow, 'status' | 'current_period_end'>>(subs: readonly T[]): T | undefined {
  const end = (s: T) => (s.current_period_end ? new Date(s.current_period_end).getTime() : -Infinity)
  return [...subs].sort((a, b) => Number(isLiveSub(b.status)) - Number(isLiveSub(a.status)) || end(b) - end(a))[0]
}

/** 换发订阅链接只对还在用的订阅有意义；一个都没有时退回全部 */
export function liveSubscriptions<T extends Pick<SubscriptionRow, 'status'>>(subs: readonly T[]): T[] {
  const live = subs.filter((s) => isLiveSub(s.status))
  return live.length ? live : [...subs]
}

// ===========================================================================
// 输入
// ===========================================================================
/** 调账：设计稿输入「+50 / -20」是元，转成分；非法或为 0 返回 null */
export function parseYuan(input: string): number | null {
  const text = input.trim().replace(/^¥/, '')
  if (!/^[+-]?\d+(\.\d{1,2})?$/.test(text)) return null
  const negative = text.startsWith('-')
  const [whole = '0', frac = ''] = text.replace(/^[+-]/, '').split('.')
  const cents = Number(whole) * 100 + Number(frac.padEnd(2, '0'))
  if (!Number.isSafeInteger(cents) || cents === 0) return null
  return negative ? -cents : cents
}

/** 与 platform/crypto.ValidatePassword 同一策略的前端预检；返回问题描述或 null */
export function passwordProblem(p: string): string | null {
  if ([...p].length < 8) return '密码至少需要 8 个字符'
  if (new TextEncoder().encode(p).length > 256) return '密码过长'
  if (!/\p{L}/u.test(p) || !/\p{Nd}/u.test(p)) return '密码必须同时包含字母和数字'
  return null
}

// ===========================================================================
// 用户组：删除前的拦截与「被引用」列（后端 409 的四种情况，按同样的先后顺序）
// ===========================================================================
export function groupBlocker(g: Pick<UserGroup, 'users' | 'plans' | 'prices' | 'coupons'>): string | null {
  if (g.users > 0) return `组内还有 ${g.users} 位用户，先把他们移出去`
  if (g.plans > 0) return `还有 ${g.plans} 个套餐按这个组控制可见性，先解除`
  if (g.prices > 0) return `还有 ${g.prices} 个专属价格挂在这个组上，先删掉`
  if (g.coupons > 0) return `还有 ${g.coupons} 张优惠券限定了这个组，先解除`
  return null
}

export function groupRefs(g: Pick<UserGroup, 'plans' | 'prices' | 'coupons'>): string {
  const parts = [g.plans && `套餐 ${g.plans}`, g.prices && `价格 ${g.prices}`, g.coupons && `优惠券 ${g.coupons}`].filter(Boolean)
  return parts.length ? parts.join(' · ') : '—'
}

// ===========================================================================
// 批量运营：设计稿的四个下拉 → BulkFilter（契约映射）
// 「已禁用」在 bulk 里只能单值：停用与封禁分成两项；「已过期」是订阅态
// ===========================================================================
// 「不限」是 Select 的 emptyOption（空串），不在表里
export const BULK_STATUS = [
  ['active', '正常'],
  ['suspended', '已停用'],
  ['banned', '已封禁'],
  ['expired', '订阅已过期'],
] as const
export const BULK_EXPIRY = [
  ['7', '7 天内到期'],
  ['30', '30 天内到期'],
] as const
export type BulkStatus = (typeof BULK_STATUS)[number][0] | ''
export type BulkExpiry = (typeof BULK_EXPIRY)[number][0] | ''

export interface BulkForm {
  plan: string
  status: BulkStatus
  expiry: BulkExpiry
  group: string
}

/** 只放有值的键：后端 DisallowUnknownFields 不怕多键，但空串的 status / group_id 会被当成条件校验 */
export function bulkFilter(f: BulkForm): BulkFilter {
  const out: BulkFilter = {}
  if (f.plan) out.plan_id = f.plan
  if (f.status === 'expired') out.sub_state = 'expired'
  else if (f.status) out.status = f.status
  if (f.expiry) out.expires_within_days = Number(f.expiry)
  if (f.group) out.group_id = f.group
  return out
}

/** 导出是 GET：同名 query 参数，数字转成字符串 */
export function exportQuery(filter: BulkFilter): Record<string, string> {
  return Object.fromEntries(Object.entries(filter).map(([k, v]) => [k, String(v)]))
}

/** 导出默认上限 10000（契约）；群发一次最多 20000 人（后端 422） */
export const EXPORT_MAX = 10_000
export const MAIL_MAX = 20_000

export interface GenerateForm {
  count: string
  prefix: string
  domain: string
  group: string
  reason: string
}

/** 与 adminops.GenerateUsers 同一套规则（后端先 trim、转小写再校验），返回按字段的问题 */
export function generateProblems(f: GenerateForm): Record<string, string> {
  const out: Record<string, string> = {}
  const n = Number(f.count.trim())
  if (!Number.isInteger(n) || n < 1 || n > 500) out.count = '一次生成 1 到 500 个'
  if (!/^[a-z0-9-]{1,20}$/.test(f.prefix.trim().toLowerCase())) out.prefix = '只能用小写字母、数字和短横线，1 到 20 位'
  const domain = f.domain.trim().toLowerCase()
  if (domain.length < 4 || domain.length > 63 || !domain.includes('.') || !/^[a-z0-9.-]+$/.test(domain)) out.domain = '域名格式不正确，如 example.com'
  const r = [...f.reason.trim()].length
  if (r < 5 || r > 500) out.reason = '请写清生成原因，5 到 500 个字'
  return out
}

/** 生成结果的本地 CSV（契约：「下载」不再请求服务器） */
export function generatedRows(users: ReadonlyArray<{ email: string; password: string }>): string[][] {
  return [['邮箱', '初始密码'], ...users.map((u) => [u.email, u.password])]
}

// ===========================================================================
// 设备策略：「接近或超出上限」= 有上限且在线数已到上限（契约前端过滤口径）
// ===========================================================================
export function nearLimit(devices: readonly OnlineDevice[]): OnlineDevice[] {
  return devices.filter((d) => d.limit > 0 && d.online >= d.limit)
}

/** 在线格：前 limit 格是额度，超出的画成危险色；最多画 12 格 */
export function pips(online: number, limit: number): Array<'on' | 'over' | 'off'> {
  const n = Math.min(Math.max(online, limit), 12)
  return Array.from({ length: n }, (_, i) => (i < online ? (limit > 0 && i >= limit ? 'over' : 'on') : 'off'))
}

// ===========================================================================
// 流量重置
// ===========================================================================
export const RESET_REASON_VIEW: Record<ResetReason, { label: string; tone: Tone; filter: string }> = {
  renewal: { label: '自动', tone: 'neutral', filter: '续费' },
  cycle_roll: { label: '自动', tone: 'neutral', filter: '周期滚动' },
  manual: { label: '手动', tone: 'info', filter: '手动' },
  gift_card: { label: '礼品卡', tone: 'ok', filter: '礼品卡' },
  plan_change: { label: '变更套餐', tone: 'warn', filter: '变更套餐' },
}

/** 操作人：有 actor_email 用它（有 note 追加「 · note」）；没有时按原因写成「系统 · …」 */
export function resetActor(log: Pick<ResetLog, 'reason' | 'actor_email' | 'note'>): string {
  if (log.actor_email) return log.note ? `${log.actor_email} · ${log.note}` : log.actor_email
  const system: Record<ResetReason, string> = {
    renewal: '系统 · 续费',
    cycle_roll: '系统 · 周期滚动',
    manual: '系统',
    gift_card: '用户 · 兑换礼品卡',
    plan_change: '系统 · 变更套餐',
  }
  return log.note ? `${system[log.reason]} · ${log.note}` : system[log.reason]
}

export const NOTE_MAX = 500

/** 重置原因 5–500 字（后端 fields.note） */
export function noteProblem(note: string): string | null {
  const n = [...note.trim()].length
  if (n < REASON_MIN) return `请写清重置原因，至少 ${REASON_MIN} 个字`
  if (n > NOTE_MAX) return `重置原因不超过 ${NOTE_MAX} 个字`
  return null
}

/** 手动重置作用的订阅：与 billing.ManualResetTraffic 同口径——只看 status=active，到期最晚的一条（不含试用） */
export function resettableSub<T extends Pick<SubscriptionRow, 'status' | 'current_period_end'>>(subs: readonly T[]): T | undefined {
  const end = (s: T) => (s.current_period_end ? new Date(s.current_period_end).getTime() : -Infinity)
  return subs.filter((s) => s.status === 'active').sort((a, b) => end(b) - end(a))[0]
}

/** 按邮箱找人：GET v1/users?q= 是模糊匹配，只认邮箱完全相等（不分大小写）的那一条 */
export function exactEmail<T extends Pick<UserRow, 'email'>>(users: readonly T[], email: string): T | undefined {
  const want = email.trim().toLowerCase()
  return want ? users.find((u) => u.email.toLowerCase() === want) : undefined
}
