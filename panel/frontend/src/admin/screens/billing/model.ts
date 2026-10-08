import { formatDateTime, formatMoney, relativeTime } from '../../../core/format'
import { periodLabel } from '../plans/model'
import type { PlanRow } from '../plans/schemas'
import { ORDER_STATUS_VIEW, orderWhat, parseYuan, REASON_MIN, type Tone } from '../users/model'
import type { PlacementChoice } from './placement'
import type { Adjustment, Currency, LateCase, LateKind, LateStatus, ManualCreated, OrderDetail, OrderQueried, OrderRow, OrderStatus, PaymentHistory, Placement, Provider } from './schemas'

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

/**
 * 向渠道查单（PAY-009）：只对发起过支付的未完结订单开放。待支付单没有入账，
 * 列表行的 provider_code 就是最近一次支付尝试的渠道；为空说明从没发起过支付（后端回 409）
 */
export function canQueryChannel(o: Pick<OrderRow, 'status' | 'provider_code'>): boolean {
  return (o.status === 'pending_payment' || o.status === 'processing') && !!o.provider_code && !isOffline({ code: o.provider_code })
}

/** 查单结果的一句话与色调：渠道怎么说、这次有没有补记、补记后订单是什么状态 */
export function queriedView(r: OrderQueried): { tone: Tone; text: string } {
  const status = ORDER_STATUS_VIEW[r.order_status].label
  if (r.channel_status === 'not_found') return { tone: 'neutral', text: `渠道 ${r.provider_code} 没有这笔订单的付款记录，订单未变（${status}）。` }
  if (r.channel_status === 'unpaid') return { tone: 'neutral', text: `渠道 ${r.provider_code} 显示尚未付款，订单未变（${status}）。` }
  if (r.already_recorded) return { tone: 'info', text: `渠道确认已付款；这笔钱此前已经入账（回调或之前的查单），没有重复记账。订单：${status}。` }
  if (r.quarantine_kind) return { tone: 'warn', text: `渠道确认已付款，但订单已${status}，款项已补记进挂账（${LATE_KIND_LABELS[r.quarantine_kind]}），可在「挂账」里处理。` }
  if (r.reconciled) return { tone: 'ok', text: `渠道确认已付款，已按回调同一流程补记入账。订单：${status}。` }
  return { tone: 'warn', text: `渠道确认已付款，但没有补记，订单：${status}。` }
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
  /** 落点选项的标题要写「换成<套餐名>」 */
  planName: string
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
            planName: p.name,
          }
        }),
    )
}

/** 前端预检，键名与后端 fields 一致（user_id / price_id / target / reference / reason） */
export function manualProblems(f: ManualForm, target: PlacementChoice | null): Fields {
  const out: Fields = {}
  if (!f.user) out.user_id = '先搜索并选中一位用户'
  if (!f.choice) out.price_id = '选择套餐与周期'
  else if (f.user && !target) out.target = '先选这单落到哪一份'
  if (f.settlement === 'offline') {
    const r = referenceProblem(f.reference)
    if (r) out.reference = r
  }
  const reason = reasonProblem(f.reason, '开单原因')
  if (reason) out.reason = reason
  return out
}

/** 只在 offline 时带 reference（别的结算方式后端忽略，但 DisallowUnknownFields 之外也不该多发） */
export function manualBody(f: ManualForm, target: PlacementChoice | null) {
  const [plan_id = '', price_id = ''] = f.choice.split(':')
  return {
    user_id: f.user?.id ?? '',
    plan_id,
    price_id,
    reason: f.reason.trim(),
    settlement: f.settlement,
    ...(target ? { target } : {}),
    ...(f.settlement === 'offline' ? { reference: f.reference.trim() } : {}),
  }
}

/**
 * 站点「能不能在线付」的门槛，与 billing 的 minPaymentSQL 同一口径：启用且收新单、支持 CNY 的渠道里
 * 最小的 min_amount（用户只要有一种方式能付就能付）；一个都没有为 0（不限）
 */
export function siteMinPayment(providers: ReadonlyArray<Pick<Provider, 'enabled' | 'accepting_new' | 'currencies' | 'min_amount'>>): number {
  const mins = providers.filter((p) => p.enabled && p.accepting_new && p.currencies.includes('CNY')).map((p) => p.min_amount)
  return mins.length ? Math.min(...mins) : 0
}

/** 待支付单低于最低付款额：应付与门槛（分），用来在提交前写明原因 */
export interface BelowMinimum {
  due: number
  min: number
  currency: string
}

/**
 * 后台待支付单的应付低于站点最低付款额时，用户没法在线付，服务端回 422（billing.balancePlan 的 Manual 分支：
 * 不用余额、不免零头）。这里在提交前就把它算出来：续一期与另开一份应付是价格；换套餐先抵原套餐没用完的部分
 * （preview 时刻的数，服务端以开单时刻为准）。赠送与线下已收款不走在线支付，不受限；只有 CNY 有门槛。
 * minPay 为 null 表示不知道门槛（没有读渠道的权限或还没读回来），这时不拦，交给服务端
 */
export function manualBelowMinimum(
  settlement: Settlement,
  price: Pick<PriceChoice, 'amount' | 'currency'> | undefined,
  option: Pick<Placement, 'kind' | 'credit'> | undefined,
  minPay: number | null,
): BelowMinimum | null {
  if (settlement !== 'pending' || !price || minPay === null || price.currency !== 'CNY') return null
  const due = option?.kind === 'change' ? Math.max(price.amount - (option.credit ?? 0), 0) : price.amount
  return due > 0 && minPay > 1 && due < minPay ? { due, min: minPay, currency: price.currency } : null
}

export function belowMinimumText(b: BelowMinimum): string {
  return `这单应付 ${formatMoney(b.due, b.currency)}，低于支付渠道的最低付款额 ${formatMoney(b.min, b.currency)}，用户没法在线付。请改用「赠送」或「线下已收款」`
}

/**
 * 开单失败里能落到表单上的那部分。服务端「待支付单低于最低付款额」的 422 不带 fields（A 路 errManualBelowMinimum），
 * 只能按文案认出来，落到结算方式上：它的下一步就是换结算方式。其余交给通用的 fields / Toast
 */
export function manualFailureFields(e: { status: number; fields: Readonly<Record<string, string>>; message: string }): Fields | null {
  if (e.status === 422 && Object.keys(e.fields).length === 0 && e.message.includes('最低付款额')) return { settlement: e.message }
  return null
}

/** preview 的请求体：用户、套餐、价格都选好才发；入口订阅（从订阅行点「给这份开单」）只影响默认值 */
export function previewParams(f: ManualForm, entrySubscriptionId: string | null) {
  if (!f.user || !f.choice) return null
  const [plan_id = '', price_id = ''] = f.choice.split(':')
  if (!plan_id || !price_id) return null
  return { user_id: f.user.id, plan_id, price_id, ...(entrySubscriptionId ? { entry_subscription_id: entrySubscriptionId } : {}) }
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
  if (p.min_amount > 0) parts.push(`最低付款 ${formatMoney(p.min_amount, 'CNY')}`)
  parts.push(p.last_callback_at ? `最近回调 ${ago(p.last_callback_at, now)}` : '尚无回调')
  const tone: Tone = mode === 'off' ? 'danger' : !p.has_credentials || mode === 'paused' ? 'warn' : 'neutral'
  return { text: parts.join(' · '), tone }
}

// ===========================================================================
// 支付渠道的新建与编辑（w2pay）：只有易支付能在后台建 / 改；code 与类型建后不可改；
// 商户号与密钥只写不读，编辑时留空 = 不改。规则照 billing/provider_admin.go 的
// normalizeProviderSettings，后端仍是最终裁判（字段错误原样标回表单）。
// ===========================================================================
export const EPAY_METHODS = [
  { value: 'alipay', label: '支付宝' },
  { value: 'wxpay', label: '微信支付' },
] as const

/** 与门户 paymentMethodLabels 同一张表；不认识的方式原样显示 */
export function methodLabel(method: string): string {
  return EPAY_METHODS.find((m) => m.value === method)?.label ?? method
}

/** 门户实际会列出的方式：config.methods 非空用它，否则 default_method（与 PaymentMethods 的 SQL 同口径） */
export function effectiveMethods(p: Pick<Provider, 'methods' | 'default_method'>): string[] {
  if (p.methods.length > 0) return p.methods
  return p.default_method ? [p.default_method] : []
}

export const isEditableProvider = (p: Pick<Provider, 'adapter' | 'code'>) => p.adapter === 'epay' && !isOffline(p)

export interface ProviderForm {
  code: string
  display_name: string
  base_url: string
  submit_path: string
  api_path: string
  methods: string[]
  default_method: string
  allow_private_host: boolean
  /** 最低付款额，元（文本框原样）；提交时换成分 */
  min_amount: string
  merchant_id: string
  key: string
}

/** 渠道最低付款额的范围（分）：与后端 provider_admin.go 的白名单校验一致，epay 默认 100（¥1.00） */
export const MIN_AMOUNT_MIN = 1
export const MIN_AMOUNT_MAX = 100_000
export const MIN_AMOUNT_DEFAULT = 100

/** 元的文本换成分：最多两位小数，范围 0.01 到 1000；否则 null */
export function parseMinAmount(input: string): number | null {
  const text = input.trim().replace(/^¥/, '')
  if (!/^\d{1,6}(\.\d{1,2})?$/.test(text)) return null
  const [whole = '0', frac = ''] = text.split('.')
  const cents = Number(whole) * 100 + Number(frac.padEnd(2, '0'))
  return cents >= MIN_AMOUNT_MIN && cents <= MIN_AMOUNT_MAX ? cents : null
}

/** 分换成输入框里的元：100 → 1.00 */
export const minAmountText = (cents: number) => (cents / 100).toFixed(2)

export const emptyProviderForm = (): ProviderForm => ({
  code: '',
  display_name: '易支付',
  base_url: '',
  submit_path: '/submit.php',
  api_path: '/api.php',
  methods: ['alipay', 'wxpay'],
  default_method: 'alipay',
  allow_private_host: false,
  min_amount: minAmountText(MIN_AMOUNT_DEFAULT),
  merchant_id: '',
  key: '',
})

/** 编辑回填：凭据一律空（只写不读）；旧渠道没有 methods 时用 default_method 起步 */
export function providerFormFrom(p: Provider): ProviderForm {
  const methods = effectiveMethods(p).filter((m) => EPAY_METHODS.some((x) => x.value === m))
  const picked = methods.length ? methods : ['alipay']
  return {
    code: p.code,
    display_name: p.display_name,
    base_url: p.base_url,
    submit_path: p.submit_path || '/submit.php',
    api_path: p.api_path || '/api.php',
    methods: picked,
    default_method: picked.includes(p.default_method) ? p.default_method : picked[0]!,
    allow_private_host: p.allow_private_host,
    // 旧渠道没配过最低额时后端按默认值算，表单也从默认值起步
    min_amount: minAmountText(p.min_amount > 0 ? p.min_amount : MIN_AMOUNT_DEFAULT),
    merchant_id: '',
    key: '',
  }
}

/** 勾选 / 取消一种方式；默认方式被取消时改成剩下的第一个 */
export function toggleMethod(f: ProviderForm, method: string, on: boolean): ProviderForm {
  const methods = on ? EPAY_METHODS.map((m) => m.value).filter((m) => m === method || f.methods.includes(m)) : f.methods.filter((m) => m !== method)
  const default_method = methods.includes(f.default_method) ? f.default_method : (methods[0] ?? '')
  return { ...f, methods, default_method }
}

/** 解析失败或空串回 null */
function parseURL(raw: string): URL | null {
  if (!raw) return null
  try {
    return new URL(raw)
  } catch {
    return null
  }
}

const PROVIDER_CODE = /^[a-z][a-z0-9_-]{1,31}$/
const PROVIDER_PATH = /^\/[A-Za-z0-9._~/-]{0,127}$/

export function providerProblems(f: ProviderForm, mode: 'create' | 'edit', hasCredentials = false): Fields {
  const out: Fields = {}
  if (mode === 'create') {
    if (!PROVIDER_CODE.test(f.code.trim())) out.code = '编码需为 2–32 位小写字母、数字、- 或 _，以字母开头'
    else if (f.code.trim() === 'offline') out.code = 'offline 是系统内置渠道的编码'
  }
  const name = chars(f.display_name)
  if (name < 1 || name > 40) out.display_name = '名称需为 1–40 个字'
  const base = f.base_url.trim()
  const url = parseURL(base)
  if (!url || (url.protocol !== 'https:' && url.protocol !== 'http:') || url.search || url.hash || url.username) out.base_url = '请填写完整的站点地址，如 https://pay.example.com'
  else if (url.protocol !== 'https:' && !f.allow_private_host) out.base_url = '站点地址必须使用 https'
  for (const key of ['submit_path', 'api_path'] as const) {
    const v = f[key].trim()
    if (v && (!PROVIDER_PATH.test(v) || v.startsWith('//'))) out[key] = '路径需以 / 开头，只含字母、数字与 . _ ~ / -'
  }
  if (f.methods.length === 0) out.methods = '至少选一种支付方式'
  else if (!f.methods.includes(f.default_method)) out.default_method = '默认方式必须是已勾选的方式之一'
  // 留空 = 不传：编辑时保留原值、新建按默认 ¥1.00（后端口径）；填了就要合法，不会把空值当 0 发出去
  if (f.min_amount.trim() !== '' && parseMinAmount(f.min_amount) === null) out.min_amount = '写成元，如 1 或 0.50，范围 0.01 到 1000'
  const needCreds = mode === 'create' || !hasCredentials
  if (needCreds && !f.merchant_id.trim()) out.merchant_id = mode === 'create' ? '必填' : '该渠道还没有商户号，需填写'
  if (needCreds && !f.key.trim()) out.key = mode === 'create' ? '必填' : '该渠道还没有密钥，需填写'
  return out
}

/** PUT 的请求体没有 code / adapter（后端不收，建后不可改）；凭据留空原样发空串 = 不改 */
export function providerBody(f: ProviderForm, mode: 'create' | 'edit') {
  const settings = {
    display_name: f.display_name.trim(),
    base_url: f.base_url.trim(),
    submit_path: f.submit_path.trim(),
    api_path: f.api_path.trim(),
    methods: [...f.methods],
    default_method: f.default_method,
    allow_private_host: f.allow_private_host,
    merchant_id: f.merchant_id.trim(),
    key: f.key.trim(),
  }
  // 最低付款额只在填了时带：后端把不传（或 0）当作「编辑保留原值、新建用默认」，所以前端从不发 0
  const min = parseMinAmount(f.min_amount)
  const body = min === null ? settings : { ...settings, min_amount: min }
  return mode === 'create' ? { code: f.code.trim(), adapter: 'epay', ...body } : body
}

/** 最低付款额输入框下的说明：留空的含义随新建 / 编辑不同 */
export function minAmountHint(mode: 'create' | 'edit'): string {
  const blank = mode === 'edit' ? '留空表示不改' : `留空按默认 ¥${minAmountText(MIN_AMOUNT_DEFAULT)}`
  return `应付低于它时不能在线付：用户只能用余额付，后台待支付单要改用赠送或线下已收款。${blank}`
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

/** 开单成功的提示：按落点说清续了、换了还是新开了，换套餐时带上原订阅剩余价值的去向 */
export function manualCreatedToast(r: ManualCreated, settlement: Settlement, kind: PlacementChoice['kind'] = 'new'): string {
  if (kind === 'change') {
    const refund = r.balance_refund !== undefined && r.balance_refund > 0 ? `，原套餐剩余价值 ${formatMoney(r.balance_refund, r.currency)} 已退回余额` : ''
    return settlement === 'pending'
      ? `订单 ${r.order_no} 已创建（在原订阅上换套餐），等待用户在 30 分钟内支付`
      : `订单 ${r.order_no} 已在原订阅上换套餐，订阅链接不变${refund}`
  }
  if (kind === 'renew') {
    return settlement === 'pending'
      ? `订单 ${r.order_no} 已创建（续费），等待用户在 30 分钟内支付`
      : settlement === 'offline'
        ? `订单 ${r.order_no} 已按线下收款入账，订阅已续期，链接不变`
        : `订单 ${r.order_no} 已赠送续期，订阅链接不变`
  }
  return settlement === 'pending'
    ? `订单 ${r.order_no} 已创建，等待用户在 30 分钟内支付`
    : settlement === 'offline'
      ? `订单 ${r.order_no} 已按线下收款入账，已另开一份订阅`
      : `订单 ${r.order_no} 已赠送开通，已另开一份订阅`
}
