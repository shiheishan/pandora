import { formatBytes } from '../../../core/format'
import type { BulkFilter, OnlineDevice, OrderRow, OrderStatus, Quota, RiskLevel, SubscriptionRow, SubStatus, UserGroup, UserProfile, UserRow, UserStatus, UsersParams } from './api'

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
  ['deleted', '已注销'],
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
  if (filter === 'deleted') params.status = 'deleted'
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
  deleted: { label: '已注销', tone: 'neutral' },
}

export const SUB_STATUS_VIEW: Record<SubStatus, { label: string; tone: Tone }> = {
  active: { label: '生效中', tone: 'ok' },
  expired: { label: '已过期', tone: 'danger' },
}

/** 库里 5 个状态 → 设计 3 个 + 已过期 */
export const ORDER_STATUS_VIEW: Record<OrderStatus, { label: string; tone: Tone }> = {
  pending_payment: { label: '待支付', tone: 'warn' },
  paid: { label: '已支付', tone: 'ok' },
  fulfilled: { label: '已支付', tone: 'ok' },
  cancelled: { label: '已取消', tone: 'neutral' },
  expired: { label: '已过期', tone: 'neutral' },
}

export const RISK_VIEW: Record<RiskLevel, { label: string; tone: Tone }> = {
  trusted: { label: '可信', tone: 'ok' },
  normal: { label: '正常', tone: 'neutral' },
  elevated: { label: '偏高', tone: 'warn' },
  high: { label: '高', tone: 'danger' },
}

const INTERVAL_LABELS: Record<string, string> = {
  day: '日付',
  week: '周付',
  month: '月付',
  quarter: '季付',
  year: '年付',
  one_time: '一次性',
}

// 订单种类的封闭枚举归订单与收款模块；这里按字符串查，种类增删时没写到的退回空串
const KIND_LABELS: Readonly<Record<string, string>> = {
  new: '新购',
  renewal: '续费',
  upgrade: '变更套餐',
  stack: '叠加',
  topup: '余额充值',
  traffic_reset: '流量重置',
}

/** 订单「买了什么」：套餐名 · 周期（多项时提示还有几项）；充值单没有套餐名 */
export function orderWhat(o: Pick<OrderRow, 'plan_name' | 'interval' | 'interval_count' | 'item_count'> & { kind: string }): string {
  const name = o.plan_name || KIND_LABELS[o.kind] || ''
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

/** 已注销的账号邮箱是占位值，界面一律显示「已注销 #<短 id>」（与后端 DeletedShortID 同口径） */
export function userLabel(u: { id: string; email: string; status: UserStatus }): string {
  return u.status === 'deleted' ? `已注销 #${shortId(u.id)}` : u.email
}

/** 后台改邮箱、注销、改邀请人、批量封禁、改订阅的原因上限（后端 5–500 字） */
export const REASON_MAX = 500

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

/** 还在用 = 订阅状态为 active（库里只有 active / expired） */
export function isLive(status: SubStatus): boolean {
  return status === 'active'
}

export function currentSubscription<T extends Pick<SubscriptionRow, 'status' | 'current_period_end'>>(subs: readonly T[]): T | undefined {
  const end = (s: T) => (s.current_period_end ? new Date(s.current_period_end).getTime() : -Infinity)
  return [...subs].sort((a, b) => Number(isLive(b.status)) - Number(isLive(a.status)) || end(b) - end(a))[0]
}

/** 换发订阅链接的候选：还在用的订阅；一个都没有时退回全部（让管理员仍能换发已过期的那份） */
export function rotatableSubscriptions<T extends Pick<SubscriptionRow, 'status'>>(subs: readonly T[]): T[] {
  const live = subs.filter((s) => isLive(s.status))
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

/** 与 identity/service validatePassword 同一策略的前端预检；返回问题描述或 null */
export function passwordProblem(p: string): string | null {
  if ([...p].length < 8) return '密码至少需要 8 个字符'
  if (new TextEncoder().encode(p).length > 256) return '密码过长'
  if (!/\p{L}/u.test(p) || !/\p{Nd}/u.test(p)) return '密码必须同时包含字母和数字'
  return null
}

// ===========================================================================
// 用户组：删除前的拦截与「被引用」列（后端 409 的情况，按同样的先后顺序）
// 专属价格不拦：没卖过的随组删除，卖过的由后端 409 拒绝（提示改用停用）
// ===========================================================================
export function groupBlocker(g: Pick<UserGroup, 'users' | 'plans' | 'coupons' | 'exclusive_pools'>): string | null {
  // R104：与后端 409 同一顺序，先看节点池名单（删了会让那个池悄悄对所有人开放）
  const pool = g.exclusive_pools[0]
  if (pool) return `节点池「${pool.name}」限定了这个组，先在节点池里把它移出名单`
  if (g.users > 0) return `组内还有 ${g.users} 位用户，先把他们移出去`
  if (g.plans > 0) return `还有 ${g.plans} 个套餐按这个组控制可见性，先解除`
  if (g.coupons > 0) return `还有 ${g.coupons} 张优惠券限定了这个组，先解除`
  return null
}

/** R104「可用节点池」列：把这个组列入限定名单的池；空 = 只能用未限定的池（显示「—」，悬停说明） */
export const EXCLUSIVE_NONE_HINT = '只能用未限定的节点池'
export const exclusivePoolsLabel = (g: Pick<UserGroup, 'exclusive_pools'>) => (g.exclusive_pools.length ? g.exclusive_pools.map((p) => p.name).join('、') : '—')

/**
 * POST v1/settings/device-limit 的请求体：strict 才带 grace；R103 的 window_minutes 省略 = 不改，只在改了时带。
 * 宽容值不合法时返回 null（页面在输入框下提示）
 */
export function devicePolicyBody(form: { mode: 'loose' | 'strict'; grace: string; window: number }, savedWindow: number): Record<string, unknown> | null {
  const g = Number(form.grace.trim())
  if (form.mode === 'strict' && !(form.grace.trim() !== '' && Number.isInteger(g) && g >= 0 && g <= 5)) return null
  return { mode: form.mode, ...(form.mode === 'strict' ? { grace: g } : {}), ...(form.window !== savedWindow ? { window_minutes: form.window } : {}) }
}

export function groupRefs(g: Pick<UserGroup, 'plans' | 'coupons'>): string {
  const parts = [g.plans && `套餐 ${g.plans}`, g.coupons && `优惠券 ${g.coupons}`].filter(Boolean)
  return parts.length ? parts.join(' · ') : '—'
}

/** 停用组在下拉里的后缀 */
export const GROUP_DISABLED_SUFFIX = '（已停用）'
/** 下拉、勾选项里一个组的显示名：停用的组加「（已停用）」 */
export const groupOptionLabel = (g: { name: string; status: string }): string => (g.status === 'disabled' ? `${g.name}${GROUP_DISABLED_SUFFIX}` : g.name)
/**
 * 分配 / 选择类下拉的候选：隐藏停用组，但 keep 里的（当前已选的值）照样保留，不能把已有引用丢掉或显示成空。
 * 筛选类下拉不用它（停用组里还有成员，要能筛出来）。保持原顺序。
 */
export function assignableGroups<T extends { id: string; status: string }>(groups: readonly T[], keep: readonly string[] = []): T[] {
  return groups.filter((g) => g.status !== 'disabled' || keep.includes(g.id))
}
/** 停用确认的正文 */
export const GROUP_DISABLE_NOTICE = '已有成员不受影响，照样看到这个组的套餐、节点池和订阅域名。停用后不能再把用户加进这个组，后台各处的分配下拉里也不再出现它；可以随时重新启用。'
/** 删组确认的正文 */
export const GROUP_DELETE_NOTICE = '删除后无法恢复。这个组已经没有成员，也没有套餐或优惠券引用它。'

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

/** 与 identity/service normalizeGenerateInput 同一套规则（后端先 trim、转小写再校验），返回按字段的问题 */
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
// 手动重置本期流量（按份：订阅卡上点哪份重置哪份）
// ===========================================================================
export const NOTE_MAX = 500

/** 重置原因 5–500 字（后端 fields.note） */
export function noteProblem(note: string): string | null {
  const n = [...note.trim()].length
  if (n < REASON_MIN) return `请写清重置原因，至少 ${REASON_MIN} 个字`
  if (n > NOTE_MAX) return `重置原因不超过 ${NOTE_MAX} 个字`
  return null
}

/** 这一份怎么称呼：有备注名写「备注名」套餐名，没有就只写套餐名 */
export function subName(s: Pick<SubscriptionRow, 'label' | 'plan_name'>): string {
  return s.label ? `「${s.label}」${s.plan_name}` : s.plan_name
}

/** 下拉与对话框里一份订阅的完整名字：称呼 · 版本 · 状态 */
export function subLabel(s: Pick<SubscriptionRow, 'label' | 'plan_name' | 'plan_version' | 'status'>): string {
  return `${subName(s)} · v${s.plan_version} · ${SUB_STATUS_VIEW[s.status].label}`
}

/** 「给这份开单」有意义的订阅：还在用的，或过期不满 30 天还能续的（其余落点规则里不出现） */
export function isOrderTarget<T extends Pick<SubscriptionRow, 'status' | 'current_period_end'>>(s: T, now: Date): boolean {
  return isLive(s.status) || extendableSubscriptions([s], now).length > 0
}

/** 按邮箱找人：GET v1/users?q= 是模糊匹配，只认邮箱完全相等（不分大小写）的那一条 */
export function exactEmail<T extends Pick<UserRow, 'email'>>(users: readonly T[], email: string): T | undefined {
  const want = email.trim().toLowerCase()
  return want ? users.find((u) => u.email.toLowerCase() === want) : undefined
}

// ===========================================================================
// 加时长：POST v1/subscriptions/{id}/extend，口径与后端 billing.subscriptionExtendable、
// validateAdminExtend、extendSubscriptionTx 一致
// ===========================================================================
export const EXTEND_DAYS_MAX = 3650
export const EXTEND_PRESETS = [7, 30, 90] as const

/** 原地续费窗口（后端过期扫描在过期满 30 天时关窗，billing/expire.go） */
const RENEWAL_WINDOW_DAYS = 30

/** 已过期整天数（周期末已过才有，否则 null） */
export function expiredDays(periodEnd: string | null, now: Date): number | null {
  if (!periodEnd) return null
  const ms = now.getTime() - new Date(periodEnd).getTime()
  return ms >= 0 ? Math.floor(ms / DAY) : null
}

/**
 * 能加时长的订阅：生效中，或已过期不满 30 天（救回：状态回到 active、流量按天数折算、
 * 旧链接恢复可用）；都要有到期时间。别的状态后端回 409，没有到期时间回 422。后台列表不带关窗时刻，
 * 按周期末推算（关窗由过期扫描一分钟内写上）。
 */
export function extendableSubscriptions<T extends Pick<SubscriptionRow, 'status' | 'current_period_end'>>(subs: readonly T[], now: Date = new Date()): T[] {
  return subs.filter((s) => {
    if (s.current_period_end === null) return false
    if (s.status === 'active') return true
    const d = expiredDays(s.current_period_end, now)
    return s.status === 'expired' && d !== null && d < RENEWAL_WINDOW_DAYS
  })
}

/** 这次加时长是不是「救回」：已过期（状态或周期末），要二次确认 */
export function isRescue<T extends Pick<SubscriptionRow, 'status' | 'current_period_end'>>(s: T, now: Date): boolean {
  return s.status === 'expired' || expiredDays(s.current_period_end, now) !== null
}

/** 天数：1 到 3650 的整数，否则 null（后端 fields.days） */
export function parseExtendDays(input: string): number | null {
  const v = input.trim()
  if (!/^\d{1,4}$/.test(v)) return null
  const n = Number(v)
  return n >= 1 && n <= EXTEND_DAYS_MAX ? n : null
}

/** 原因 5–500 字（后端 fields.reason） */
export function extendReasonProblem(reason: string): string | null {
  const n = [...reason.trim()].length
  if (n < REASON_MIN) return `请写清加时长的原因，至少 ${REASON_MIN} 个字`
  if (n > NOTE_MAX) return `原因不超过 ${NOTE_MAX} 个字`
  return null
}

/** 预计新到期：从「原到期」与「现在」里更晚的那个起算，加 days 个 24 小时（后端按 UTC 加天） */
export function extendedEnd(periodEnd: string, days: number, now: Date): Date {
  const base = Math.max(new Date(periodEnd).getTime(), now.getTime())
  return new Date(base + days * DAY)
}

// ===========================================================================
// 两类套餐（10-16）：订阅套餐按时间算、每周期一份流量；一次性套餐按流量算、总量不清零，有效天数可空（永久）。
// 后台详情不直接带套餐类型：按流量配额的周期认（subscription 服务 trafficPeriod：一次性是 total，订阅是 cycle）
// ===========================================================================
export type PlanKind = 'subscription' | 'one_time'

export function planKind(s: Pick<SubscriptionRow, 'quotas'>): PlanKind {
  return trafficQuota(s.quotas)?.period === 'total' ? 'one_time' : 'subscription'
}

/** 永久的远期哨兵门槛（与 subscription 服务 IsPermanent 同口径：期末晚于现在 + 50 年） */
const PERMANENT_YEARS = 50

/** 一次性套餐买的「永久」：期末是开通时 + 100 年的哨兵 */
export function isPermanent(periodEnd: string | null, now: Date): boolean {
  if (!periodEnd) return false
  const horizon = new Date(now.getTime())
  horizon.setFullYear(horizon.getFullYear() + PERMANENT_YEARS)
  return new Date(periodEnd).getTime() > horizon.getTime()
}

/** 订阅卡与列表的到期文案：永久的一次性不写天数 */
export function subExpiryView(periodEnd: string | null, now: Date): { text: string; tone: Tone } {
  return isPermanent(periodEnd, now) ? { text: '永久有效', tone: 'neutral' } : expiryView(periodEnd, now)
}

// ===========================================================================
// 风控：近 7 天登录 IP 数（画像 ips[].last 在近 7 天内的去重 IP 数，前端算）
// ===========================================================================
const LOGIN_IP_WINDOW_DAYS = 7

export function loginIps7d(ips: UserProfile['ips'], now: Date): number {
  const since = now.getTime() - LOGIN_IP_WINDOW_DAYS * DAY
  return new Set(ips.filter((r) => new Date(r.last).getTime() >= since).map((r) => r.ip)).size
}

/** 4 个以上值得看一眼（设计稿标警示色） */
export const LOGIN_IPS_WARN = 4

// ===========================================================================
// 调整余额：「增加 / 扣减」单选 + 正金额（元，最多两位小数）→ 带符号的分；不合法或为 0 返回 null
// ===========================================================================
export type BalanceDirection = 'plus' | 'minus'

export function balanceCents(direction: BalanceDirection, input: string): number | null {
  const text = input.trim().replace(/^¥/, '')
  if (!/^\d+(\.\d{1,2})?$/.test(text)) return null
  const cents = parseYuan(text)
  if (cents === null) return null
  return direction === 'minus' ? -cents : cents
}

// ===========================================================================
// 加流量：POST v1/subscriptions/{id}/traffic-pack（路径沿用旧名），按 GB 填、提交字节
// ===========================================================================
const GIB = 1024 ** 3

/** 一次最多 10240 GB（后端上限 10 TiB，fields.bytes） */
export const GRANT_GB_MAX = 10240
export const GRANT_GB_PRESETS = [10, 50, 100] as const

/** GB 数：1 到 10240 的整数，换成字节；否则 null */
export function parseGrantGB(input: string): number | null {
  const v = input.trim()
  if (!/^\d{1,5}$/.test(v)) return null
  const n = Number(v)
  return n >= 1 && n <= GRANT_GB_MAX ? n * GIB : null
}

/** 加流量的原因 5–500 字（后端 fields.reason） */
export function grantReasonProblem(reason: string): string | null {
  const n = [...reason.trim()].length
  if (n < REASON_MIN) return `请写清加流量的原因，至少 ${REASON_MIN} 个字`
  if (n > NOTE_MAX) return `原因不超过 ${NOTE_MAX} 个字`
  return null
}
