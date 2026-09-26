/**
 * [INPUT]: 依赖 ../../../core/format 的 formatDateTime / formatMoney / relativeTime，依赖 ../users/model 的 ORDER_STATUS_VIEW / orderWhat / parseYuan / REASON_MIN / Tone（订单词汇与元转分同一口径），依赖 ../plans/model 的 periodLabel，依赖 ./schemas 的类型
 * [OUTPUT]: 对外提供订单（ORDER_FILTERS / OrderFilter / isOrderFilter / filterStatuses、ORDER_STATUS_VIEW、orderWhat、channelLabel、sourceLabel、canMarkPaid、canCancel、PayLine / paymentLines、orderFacts）、人工开单（Settlement / SETTLEMENTS / ManualForm / emptyManual / PriceChoice / priceChoices / manualProblems / manualBody）、通用校验（reasonProblem / referenceProblem、REASON_MAX / REFERENCE_MAX）、挂账（LATE_FILTERS / LateFilter / isLateFilter、LATE_STATUS_VIEW、lateReason、ageDays、pendingTotals）、渠道（isOffline、providerMode / ProviderMode、toggleBody、todayLabel、rateLabel、providerNote）、收入调整（AdjustForm / emptyAdjust / adjustProblems / adjustBody、adjustmentView、reverseReason、todayLocal、ADJUST_MAX）
 * [POS]: admin/screens/billing 的纯逻辑：契约后台-05 的状态分组与映射、渠道兜底（余额 / 人工）、支付记录「以支付尝试为行、有入账看入账」、挂账文案（保留规则 6：平台欠用户的钱）、渠道开关到 enabled / accepting_new 的映射（PAY-009）、各写接口的前端预检（与 Go 同规则、fields 键名同后端）；不碰 React 与网络，model.test.ts 覆盖
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { formatDateTime, formatMoney, relativeTime } from '../../../core/format'
import { periodLabel } from '../plans/model'
import type { PlanRow } from '../plans/schemas'
import { ORDER_STATUS_VIEW, orderWhat, parseYuan, REASON_MIN, type Tone } from '../users/model'
import type { Adjustment, Currency, LateCase, LateKind, LateStatus, OrderDetail, OrderRow, OrderStatus, PaymentHistory, Provider } from './schemas'

export { ORDER_STATUS_VIEW, orderWhat, type Tone }

type Fields = Record<string, string>
const chars = (s: string) => [...s.trim()].length
export const REASON_MAX = 500
export const REFERENCE_MAX = 128

// ===========================================================================
// 订单
// ===========================================================================

/** 状态分段 → 后端多值 status（R63）；退款不单列（契约），在「全部」里看 */
export const ORDER_FILTERS = [
  ['all', '全部', ''],
  ['pending', '待支付', 'draft,pending_payment,processing'],
  ['paid', '已支付', 'paid,fulfilled'],
  ['cancelled', '已取消', 'cancelled,expired'],
] as const
export type OrderFilter = (typeof ORDER_FILTERS)[number][0]

export function isOrderFilter(v: string | null): v is OrderFilter {
  return ORDER_FILTERS.some(([k]) => k === v)
}

export function filterStatuses(f: OrderFilter): string | undefined {
  return ORDER_FILTERS.find(([k]) => k === f)?.[2] || undefined
}

/**
 * 渠道列：有入账或支付尝试就是渠道名；否则全额余额支付显示「余额」，人工单显示「人工」（抽屉按
 * manual_reason 传；列表的人工单改在订单号旁挂「人工」标识，R114），其余「—」
 */
export function channelLabel(o: Pick<OrderRow, 'provider_name' | 'balance_applied' | 'total_amount'>, manual = false): string {
  if (o.provider_name) return o.provider_name
  if (o.balance_applied > 0 && o.balance_applied === o.total_amount) return '余额'
  return manual ? '人工' : '—'
}

/** 来源：人工单按 manual_reason 判断（不是 kind），开单人账号删掉后 email 为 null */
export function sourceLabel(o: Pick<OrderDetail, 'manual_reason' | 'created_by_email'>): string {
  if (o.manual_reason === null) return '门户下单'
  return `人工开单 · ${o.created_by_email ?? '已删除的管理员'}`
}

const PENDING: readonly OrderStatus[] = ['draft', 'pending_payment', 'processing']

/** 标记已支付：后端只收 pending_payment / processing，且应付大于 0 */
export function canMarkPaid(o: Pick<OrderRow, 'status' | 'payable_amount'>): boolean {
  return (o.status === 'pending_payment' || o.status === 'processing') && o.payable_amount > 0
}

/** 取消：只对还没支付的单开放；有入账的单后端也会回 409 */
export function canCancel(o: Pick<OrderRow, 'status'>): boolean {
  return PENDING.includes(o.status)
}

/** 抽屉 facts（设计稿：用户 / 内容 / 金额 / 币种 / 渠道 / 来源），按契约补上应付、余额抵扣、过期时间与取消原因 */
export function orderFacts(o: OrderDetail): Array<[string, string]> {
  const money = (n: number) => formatMoney(n, o.currency)
  const facts: Array<[string, string]> = [
    ['用户', o.user_email],
    ['内容', orderWhat(o)],
    ['金额', money(o.total_amount)],
  ]
  if (o.discount_amount > 0) facts.push(['优惠', `− ${money(o.discount_amount)}`])
  if (o.balance_applied > 0) facts.push(['余额抵扣', money(o.balance_applied)])
  facts.push(['应付', money(o.payable_amount)])
  if (o.paid_amount > 0) facts.push(['实收', money(o.paid_amount)])
  if (o.refunded_amount > 0) facts.push(['已退款', money(o.refunded_amount)])
  facts.push(['币种', o.currency], ['渠道', channelLabel(o, o.manual_reason !== null)], ['来源', sourceLabel(o)])
  if (o.manual_reason) facts.push(['开单原因', o.manual_reason])
  if (PENDING.includes(o.status) && o.expires_at) facts.push(['支付截止', formatDateTime(o.expires_at)])
  if (o.paid_at) facts.push(['支付时间', formatDateTime(o.paid_at)])
  if (o.cancelled_at) facts.push(['取消时间', formatDateTime(o.cancelled_at)])
  if (o.expired_at) facts.push(['过期时间', formatDateTime(o.expired_at)])
  if (o.cancel_reason) facts.push(['取消原因', o.cancel_reason])
  return facts
}

// ---------------------------------------------------------------------------
// 支付记录：设计每行一个状态——以支付尝试为行，有对应入账的显示入账状态；线下入账没有
// 支付尝试，单独成行并标「人工确认」；退款（设计缺）按同一行式附在后面
// ---------------------------------------------------------------------------
export interface PayLine {
  key: string
  channel: string
  ref: string
  amount: number
  currency: string
  label: string
  tone: Tone
  at: string
}

const INTENT_VIEW: Record<string, { label: string; tone: Tone }> = {
  created: { label: '等待回调', tone: 'warn' },
  requires_action: { label: '等待回调', tone: 'warn' },
  processing: { label: '等待回调', tone: 'warn' },
  succeeded: { label: '成功', tone: 'ok' },
  failed: { label: '失败', tone: 'danger' },
  cancelled: { label: '已关闭', tone: 'neutral' },
  expired: { label: '已关闭', tone: 'neutral' },
}
const PAYMENT_VIEW: Record<string, { label: string; tone: Tone }> = {
  succeeded: { label: '成功', tone: 'ok' },
  refunded: { label: '已退款', tone: 'info' },
  partially_refunded: { label: '部分退款', tone: 'info' },
  disputed: { label: '争议中', tone: 'danger' },
  reversed: { label: '已冲回', tone: 'danger' },
}
const REFUND_VIEW: Record<string, { label: string; tone: Tone }> = {
  pending: { label: '退款处理中', tone: 'warn' },
  approved: { label: '退款处理中', tone: 'warn' },
  processing: { label: '退款处理中', tone: 'warn' },
  succeeded: { label: '已退款', tone: 'info' },
  failed: { label: '退款失败', tone: 'danger' },
  rejected: { label: '退款已驳回', tone: 'neutral' },
}

export function paymentLines(h: PaymentHistory): PayLine[] {
  const byIntent = new Map(h.payments.filter((p) => p.payment_intent_id).map((p) => [p.payment_intent_id!, p]))
  const intentIds = new Set(h.payment_intents.map((i) => i.id))
  const lines: PayLine[] = h.payment_intents.map((i) => {
    const p = byIntent.get(i.id)
    const view = p ? PAYMENT_VIEW[p.status]! : INTENT_VIEW[i.status]!
    return {
      key: i.id,
      channel: i.provider_name,
      ref: p?.provider_payment_id ?? i.provider_ref ?? i.failure_message ?? '—',
      amount: p?.amount ?? i.amount,
      currency: i.currency,
      ...view,
      at: p?.paid_at ?? i.created_at,
    }
  })
  for (const p of h.payments) {
    if (p.payment_intent_id && intentIds.has(p.payment_intent_id)) continue
    const offline = p.provider_code === 'offline' && p.status === 'succeeded'
    lines.push({
      key: p.id,
      channel: p.provider_name,
      ref: p.provider_payment_id,
      amount: p.amount,
      currency: p.currency,
      ...(offline ? { label: '人工确认', tone: 'info' as Tone } : PAYMENT_VIEW[p.status]!),
      at: p.paid_at,
    })
  }
  for (const r of h.refunds) {
    lines.push({ key: r.id, channel: '退款', ref: r.reason, amount: -r.amount, currency: r.currency, ...REFUND_VIEW[r.status]!, at: r.succeeded_at ?? r.created_at })
  }
  return lines.sort((a, b) => a.at.localeCompare(b.at))
}

// ===========================================================================
// 通用校验：与 Go 同规则（去首尾空白后按字数，不按字节）
// ===========================================================================
export function reasonProblem(text: string, what: string): string | null {
  const n = chars(text)
  if (n < REASON_MIN) return `请写清${what}，至少 ${REASON_MIN} 个字`
  if (n > REASON_MAX) return `${what}最多 ${REASON_MAX} 个字`
  return null
}

export function referenceProblem(text: string): string | null {
  const n = chars(text)
  if (n === 0) return '请填写线下凭证号（银行流水号、收据编号等）'
  if (n > REFERENCE_MAX) return `凭证号最多 ${REFERENCE_MAX} 个字`
  return null
}

// ===========================================================================
// 人工开单：POST v1/orders/manual（R64 / R74；balance 按 D-C-3（已决，5.A.2）不提供）
// ===========================================================================
export type Settlement = 'grant' | 'pending' | 'offline'
export const SETTLEMENTS: ReadonlyArray<{ value: Settlement; label: string; hint: string }> = [
  { value: 'pending', label: '待用户支付', hint: '生成一张待支付订单，用户在门户「订单」里付款；30 分钟内不付自动过期' },
  { value: 'offline', label: '线下已收款', hint: '已通过转账等方式收到钱：填凭证号，当场入账并开通，同时计收入与佣金' },
  { value: 'grant', label: '赠送（0 元）', hint: '全额减免、当场开通，不计收入' },
]

export interface ManualForm {
  user: { id: string; email: string } | null
  /** `${plan_id}:${price_id}` */
  choice: string
  settlement: Settlement
  reference: string
  reason: string
}

export const emptyManual = (user: ManualForm['user'] = null): ManualForm => ({ user, choice: '', settlement: 'pending', reference: '', reason: '' })

export interface PriceChoice {
  value: string
  label: string
  amount: number
  currency: string
}

/** 在售套餐（已发布、有当前版本）的在售价格；组专属价与试用在名字里注明，后端下单时再校验资格 */
export function priceChoices(plans: readonly PlanRow[]): PriceChoice[] {
  return plans
    .filter((p) => p.status === 'active' && p.current_version_id !== null)
    .flatMap((p) =>
      p.prices
        .filter((x) => x.status === 'active')
        // CNY 在前，同币种按价格升序（后端按创建时间倒序给）
        .sort((a, b) => Number(b.currency === 'CNY') - Number(a.currency === 'CNY') || a.currency.localeCompare(b.currency) || a.unit_amount - b.unit_amount)
        .map((x) => {
          const notes = [x.user_group_id ? '组专属' : '', x.trial_days > 0 ? `试用 ${x.trial_days} 天` : ''].filter(Boolean)
          return {
            value: `${p.id}:${x.id}`,
            label: `${p.name} · ${periodLabel(x.billing_interval, x.interval_count)} ${formatMoney(x.unit_amount, x.currency)}${notes.length ? `（${notes.join('，')}）` : ''}`,
            amount: x.unit_amount,
            currency: x.currency,
          }
        }),
    )
}

/** 前端预检，键名与后端 fields 一致（user_id / price_id / reference / reason） */
export function manualProblems(f: ManualForm): Fields {
  const out: Fields = {}
  if (!f.user) out.user_id = '先搜索并选中一位用户'
  if (!f.choice) out.price_id = '选择套餐与周期'
  if (f.settlement === 'offline') {
    const r = referenceProblem(f.reference)
    if (r) out.reference = r
  }
  const reason = reasonProblem(f.reason, '开单原因')
  if (reason) out.reason = reason
  return out
}

/** 只在 offline 时带 reference（别的结算方式后端忽略，但 DisallowUnknownFields 之外也不该多发） */
export function manualBody(f: ManualForm) {
  const [plan_id = '', price_id = ''] = f.choice.split(':')
  return {
    user_id: f.user?.id ?? '',
    plan_id,
    price_id,
    reason: f.reason.trim(),
    settlement: f.settlement,
    ...(f.settlement === 'offline' ? { reference: f.reference.trim() } : {}),
  }
}

// ===========================================================================
// 挂账（保留规则 6）：订单取消后才到账、续费或变更时订阅已结束、或超额扣款的钱，平台欠用户的
// ===========================================================================
export const LATE_FILTERS = [
  ['all', '全部', ''],
  ['suspense', '待处理', 'suspense'],
  ['applied', '已转入余额', 'applied'],
] as const
export type LateFilter = (typeof LATE_FILTERS)[number][0]
export function isLateFilter(v: string | null): v is LateFilter {
  return LATE_FILTERS.some(([k]) => k === v)
}

export const LATE_STATUS_VIEW: Record<LateStatus, { label: string; tone: Tone }> = {
  suspense: { label: '待处理', tone: 'warn' },
  applied: { label: '已转入余额', tone: 'ok' },
  refunded: { label: '已原路退回', tone: 'neutral' },
  refund_pending: { label: '退款处理中', tone: 'info' },
  manual_review: { label: '人工核对中', tone: 'info' },
}

const LATE_KIND_LABELS: Record<LateKind, string> = { released_order: '订单取消后到账', excess_capture: '超额扣款', ineligible_subscription: '订阅已结束后到账' }
export function lateReason(c: Pick<LateCase, 'case_kind' | 'order_no'>): string {
  return `${LATE_KIND_LABELS[c.case_kind]} · ${c.order_no}`
}

/** 账龄：now − received_at，整天 */
export function ageDays(receivedAt: string, now: Date): number {
  const ms = now.getTime() - Date.parse(receivedAt)
  return Number.isFinite(ms) ? Math.max(0, Math.floor(ms / 86_400_000)) : 0
}

/** 待处理合计按币种分开（R3）：CNY 在前，零额不列；没有待处理的返回空数组 */
export function pendingTotals(amounts: Readonly<Record<string, number>>): string[] {
  return Object.entries(amounts)
    .filter(([, v]) => v > 0)
    .sort(([a], [b]) => (a === 'CNY' ? -1 : b === 'CNY' ? 1 : a.localeCompare(b)))
    .map(([currency, v]) => formatMoney(v, currency))
}

// ===========================================================================
// 支付渠道（PAY-009）：开关只动 accepting_new，「完全停用」才动 enabled
// ===========================================================================
export type ProviderMode = 'on' | 'paused' | 'off'

/** offline 是系统内置的线下收款渠道（accepting_new 恒 false），只读展示 */
export const isOffline = (p: Pick<Provider, 'code'>) => p.code === 'offline'

export function providerMode(p: Pick<Provider, 'enabled' | 'accepting_new'>): ProviderMode {
  if (!p.enabled) return 'off'
  return p.accepting_new ? 'on' : 'paused'
}

/** 开 = 收新单，关 = 停止新单但回调照常（设计的「停用」）；完全停用连回调也不处理 */
export function toggleBody(target: ProviderMode): { enabled: boolean; accepting_new: boolean } {
  if (target === 'on') return { enabled: true, accepting_new: true }
  if (target === 'paused') return { enabled: true, accepting_new: false }
  return { enabled: false, accepting_new: false }
}

export function todayLabel(today: Readonly<Record<string, number>>): string {
  const parts = pendingTotals(today)
  return parts.length ? parts.join(' · ') : '—'
}

export function rateLabel(rate: number | null): string {
  return rate === null ? '—' : `${(rate * 100).toFixed(1)}%`
}

/** relativeTime 给「N 分钟 / N 小时」时补「前」，「刚刚」与日期原样 */
function ago(at: string, now: Date): string {
  const rel = relativeTime(at, now)
  return /^\d+ (分钟|小时)$/.test(rel) ? `${rel}前` : rel
}

export function providerNote(p: Provider, now: Date): { text: string; tone: Tone } {
  if (isOffline(p)) return { text: '系统内置：人工开单「线下已收款」与「标记已支付」的入账都记在这里，结账页不显示', tone: 'neutral' }
  const mode = providerMode(p)
  const parts: string[] = []
  if (mode === 'off') parts.push('已完全停用，回调也不处理')
  if (mode === 'paused') parts.push('已停止新单，进行中的支付仍会回调')
  if (!p.has_credentials) parts.push('未配置凭据')
  parts.push(p.last_callback_at ? `最近回调 ${ago(p.last_callback_at, now)}` : '尚无回调')
  const tone: Tone = mode === 'off' ? 'danger' : !p.has_credentials || mode === 'paused' ? 'warn' : 'neutral'
  return { text: parts.join(' · '), tone }
}

// ===========================================================================
// 收入调整（报表口径，只追加）
// ===========================================================================
export const ADJUST_MAX = 1_000_000_000_000

export interface AdjustForm {
  currency: Currency
  amount: string
  reason: string
  /** YYYY-MM-DD，空 = 租户今天 */
  effectiveOn: string
}
export const emptyAdjust = (): AdjustForm => ({ currency: 'CNY', amount: '', reason: '', effectiveOn: '' })

/** 本地日期 YYYY-MM-DD：生效日输入框的上限（租户今天由后端最终判定） */
export function todayLocal(now: Date = new Date()): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`
}

export function adjustProblems(f: AdjustForm, today: string): Fields {
  const out: Fields = {}
  const cents = parseYuan(f.amount)
  if (cents === null) out.amount = '金额写成 +312.40 或 -12，最多两位小数，不能为 0'
  else if (Math.abs(cents) > ADJUST_MAX) out.amount = '调整金额必须非零且绝对值不超过 100 亿'
  const reason = reasonProblem(f.reason, '调整原因')
  if (reason) out.reason = reason
  if (f.effectiveOn && !/^\d{4}-\d{2}-\d{2}$/.test(f.effectiveOn)) out.effective_on = '日期格式应为 YYYY-MM-DD'
  else if (f.effectiveOn > today) out.effective_on = '生效日期不能晚于今天'
  return out
}

export function adjustBody(f: AdjustForm) {
  return { currency: f.currency, amount: parseYuan(f.amount) ?? 0, reason: f.reason.trim(), ...(f.effectiveOn ? { effective_on: f.effectiveOn } : {}) }
}

export function adjustmentView(a: Adjustment) {
  const reversal = a.reversal_of !== undefined
  return {
    amount: `${a.amount > 0 ? '+' : '−'}${formatMoney(Math.abs(a.amount), a.currency)}`,
    tone: (a.amount < 0 ? 'danger' : 'neutral') as Tone,
    meta: `${a.created_by_email ?? '已删除的账号'} · ${formatDateTime(a.created_at)}`,
    /** 冲销单与已冲销的都不能再冲销（后端 409） */
    state: reversal ? ('reversal' as const) : a.reversed ? ('reversed' as const) : ('open' as const),
  }
}

/** 冲销原因的默认值：「冲销：」+ 原因（原因 ≥ 5 字，拼出来必然够长），截到 500 字 */
export function reverseReason(a: Pick<Adjustment, 'reason'>): string {
  return [...`冲销：${a.reason}`].slice(0, REASON_MAX).join('')
}
