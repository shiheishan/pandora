/**
 * [INPUT]: 依赖 node:crypto 的 randomUUID，依赖 ./users 的 userStore / seedOrders / setOrderSource / Sub / User，依赖 ./plans-store 的 plans / currentOf / trafficOf / GiB
 * [OUTPUT]: 对外提供订单表 orders 与 Order 类型、findOrder、nextOrderNo、intentFor / paymentFor（造支付尝试与入账）、orderRow / orderDetail / orderHistory 视图、fulfil（开订阅）、渠道 providers / Provider / providerView、挂账 lateCases / LateCase / lateView、收入调整 adjustments / Adjustment / adjustmentView、todayLocal、err / invalid / NOT_FOUND、isUuid
 * [POS]: dev/mock/admin 的「订单与收款（后台-05）」数据层，由 billing.ts 的路由使用：订单表以 users.ts 的种子订单（同 id）为底，再补齐设计稿要看的各种情形（待支付、处理中、余额付、人工赠送 / 线下、充值、流量包、退款、美元、多项），并把自己登记为 users.ts 的订单来源，让用户抽屉「订单」与订单页同一份数据；渠道只有后端真有的 epay / demo 适配器与内置 offline；挂账与收入调整是确定性种子。视图字段与 Go 的 json tag 一一对应
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomUUID } from 'node:crypto'
import type { MockResult } from '../types.ts'
import { currentOf, plans, trafficOf } from './plans-store.ts'
import { seedOrders, setOrderSource, userStore, type Sub, type User } from './users.ts'

const MIN = 60_000
const DAY = 86_400_000
const iso = (msAgo: number) => new Date(Date.now() - msAgo).toISOString()
/** 不早于本地今天零点：渠道卡「今日成交」在凌晨启动时也有数 */
const todayIso = (msAgo: number) => {
  const midnight = new Date()
  midnight.setHours(0, 0, 1, 0)
  return new Date(Math.min(Date.now(), Math.max(midnight.getTime(), Date.now() - msAgo))).toISOString()
}
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
export const isUuid = (v: string | undefined): v is string => !!v && UUID.test(v)
export const err = (status: number, code: string, message: string, fields?: Record<string, string>): MockResult => ({ status, body: { error: { code, message, ...(fields ? { fields } : {}) } } })
export const invalid = (fields: Record<string, string>) => err(422, 'validation_failed', '请求参数校验未通过', fields)
export const NOT_FOUND = err(404, 'not_found', '资源不存在或无权访问')

/** 本地日期 YYYY-MM-DD（假后端把本机时区当租户时区） */
export function todayLocal(at = new Date()): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}`
}

// ===========================================================================
// 渠道：后端适配器只有 epay 与 demo，外加系统内置的 offline（迁移 00043）
// ===========================================================================
export interface Provider {
  id: string
  code: string
  adapter: string
  display_name: string
  enabled: boolean
  accepting_new: boolean
  has_credentials: boolean
  base_url: string
  currencies: string[]
}
export const providers: Provider[] = [
  { id: randomUUID(), code: 'demo', adapter: 'demo', display_name: '演示渠道', enabled: false, accepting_new: false, has_credentials: true, base_url: '', currencies: ['CNY', 'USD'] },
  { id: randomUUID(), code: 'epay', adapter: 'epay', display_name: '聚合收银台', enabled: true, accepting_new: true, has_credentials: true, base_url: 'https://pay.example.com', currencies: ['CNY'] },
  { id: randomUUID(), code: 'epay_backup', adapter: 'epay', display_name: '易支付 · 备用', enabled: true, accepting_new: false, has_credentials: false, base_url: 'https://pay2.example.com', currencies: ['CNY', 'USD'] },
  { id: randomUUID(), code: 'offline', adapter: 'offline', display_name: '线下收款', enabled: true, accepting_new: false, has_credentials: true, base_url: '', currencies: ['CNY', 'USD'] },
]
const providerName = (code: string) => providers.find((p) => p.code === code)?.display_name ?? code

// ===========================================================================
// 订单
// ===========================================================================
interface Intent {
  id: string
  provider_code: string
  currency: string
  amount: number
  status: string
  provider_ref: string | null
  failure_code: string | null
  failure_message: string | null
  expires_at: string | null
  created_at: string
  updated_at: string
}
interface Payment {
  id: string
  payment_intent_id: string | null
  provider_code: string
  provider_payment_id: string
  currency: string
  amount: number
  fee_amount: number
  refunded_amount: number
  status: string
  method: string | null
  paid_at: string
}
interface Refund {
  id: string
  payment_id: string | null
  provider_refund_id: string | null
  currency: string
  amount: number
  reason: string
  status: string
  entitlement_revoked: boolean
  commission_reversed: boolean
  failure_message: string | null
  succeeded_at: string | null
  created_at: string
}
interface Item {
  id: string
  plan_id: string | null
  price_id: string | null
  product_name: string
  plan_name: string | null
  plan_version: number | null
  interval: string | null
  interval_count: number | null
  quantity: number
  unit_amount: number
  currency: string
}

export interface Order {
  id: string
  order_no: string
  user_id: string
  user_email: string
  kind: string
  status: string
  currency: string
  subtotal_amount: number
  discount_amount: number
  total_amount: number
  balance_applied: number
  payable_amount: number
  paid_amount: number
  refunded_amount: number
  state_version: number
  manual_reason: string | null
  created_by: string | null
  created_by_email: string | null
  subscription_id: string | null
  created_at: string
  updated_at: string
  paid_at: string | null
  expires_at: string | null
  fulfilled_at: string | null
  cancelled_at: string | null
  expired_at: string | null
  cancel_reason: string | null
  items: Item[]
  intents: Intent[]
  payments: Payment[]
  refunds: Refund[]
}

let seq = 3300
export const nextOrderNo = () => `PD${todayLocal().slice(2).replaceAll('-', '')}${String(++seq).padStart(4, '0')}`

function blank(u: User, over: Partial<Order>): Order {
  const at = over.created_at ?? iso(0)
  return {
    id: randomUUID(),
    order_no: nextOrderNo(),
    user_id: u.id,
    user_email: u.email,
    kind: 'new',
    status: 'pending_payment',
    currency: 'CNY',
    subtotal_amount: 0,
    discount_amount: 0,
    total_amount: 0,
    balance_applied: 0,
    payable_amount: 0,
    paid_amount: 0,
    refunded_amount: 0,
    state_version: 1,
    manual_reason: null,
    created_by: null,
    created_by_email: null,
    subscription_id: null,
    created_at: at,
    updated_at: at,
    paid_at: null,
    expires_at: null,
    fulfilled_at: null,
    cancelled_at: null,
    expired_at: null,
    cancel_reason: null,
    items: [],
    intents: [],
    payments: [],
    refunds: [],
    ...over,
  }
}

export function intentFor(o: Order, code: string, status: string, at: string, over: Partial<Intent> = {}): Intent {
  return { id: randomUUID(), provider_code: code, currency: o.currency, amount: o.payable_amount, status, provider_ref: `${code.toUpperCase()}${o.order_no.slice(2)}`, failure_code: null, failure_message: null, expires_at: new Date(Date.parse(at) + 30 * MIN).toISOString(), created_at: at, updated_at: at, ...over }
}

export function paymentFor(o: Order, code: string, intent: Intent | null, at: string, ref?: string): Payment {
  return { id: randomUUID(), payment_intent_id: intent?.id ?? null, provider_code: code, provider_payment_id: ref ?? `${code}:${o.order_no}`, currency: o.currency, amount: o.payable_amount, fee_amount: code === 'offline' ? 0 : Math.round(o.payable_amount * 0.006), refunded_amount: 0, status: 'succeeded', method: code === 'offline' ? null : 'alipay', paid_at: at }
}

const planItem = (planId: string | null, name: string, amount: number, currency: string, interval = 'month', count = 1): Item => ({
  id: randomUUID(),
  plan_id: planId,
  price_id: null,
  product_name: name,
  plan_name: planId ? name : null,
  plan_version: planId ? 2 : null,
  interval: planId ? interval : null,
  interval_count: planId ? count : null,
  quantity: 1,
  unit_amount: amount,
  currency,
})


export const orders: Order[] = []

// ---- 种子一：users.ts 的每条订阅一张订单（同 id，用户抽屉的深链落在这里）---------------
for (const u of userStore) {
  for (const s of seedOrders(u)) {
    const expired = s.status === 'expired'
    const o = blank(u, {
      id: s.id,
      order_no: s.order_no,
      kind: s.kind,
      status: s.status,
      currency: s.currency,
      subtotal_amount: s.total_amount,
      total_amount: s.total_amount,
      payable_amount: s.payable_amount,
      paid_amount: s.paid_amount,
      created_at: s.created_at,
      updated_at: s.paid_at ?? s.created_at,
      paid_at: s.paid_at,
      fulfilled_at: s.paid_at,
      expires_at: new Date(Date.parse(s.created_at) + 30 * MIN).toISOString(),
      expired_at: expired ? new Date(Date.parse(s.created_at) + 30 * MIN).toISOString() : null,
      subscription_id: expired ? null : s.sub_id,
      state_version: expired ? 2 : 3,
      items: [planItem(s.plan_id, s.plan_name, s.total_amount, s.currency)],
    })
    if (s.provider_code) {
      const intent = intentFor(o, s.provider_code, expired ? 'expired' : 'succeeded', s.created_at)
      o.intents.push(intent)
      if (s.paid_at) o.payments.push(paymentFor(o, s.provider_code, intent, s.paid_at))
    }
    orders.push(o)
  }
}

// ---- 种子二：设计稿要看的各种情形，挂在固定的种子用户上 --------------------------------
const U = (i: number) => userStore[i]!
const std = plans[0]!
const pro = plans[1] ?? std
const OPS = { created_by: randomUUID(), created_by_email: 'ops@pandora.dev' }

function add(u: User, over: Partial<Order>, then?: (o: Order) => void): Order {
  const o = blank(u, { subtotal_amount: over.total_amount ?? 0, ...over })
  if (o.items.length === 0 && o.kind !== 'topup')
    o.items.push(o.kind === 'addon' ? planItem(null, '100 GB 加油包', o.subtotal_amount, o.currency) : planItem(pro.id, pro.name, o.subtotal_amount, o.currency))
  then?.(o)
  orders.push(o)
  return o
}
const paidVia = (code: string) => (o: Order) => {
  const i = intentFor(o, code, 'succeeded', o.created_at)
  o.intents.push(i)
  o.payments.push(paymentFor(o, code, i, o.created_at))
}
const done = (at: string) => ({ created_at: at, updated_at: at, paid_at: at, fulfilled_at: at, state_version: 3 })

// 待支付：等回调、首笔失败、人工开的待用户支付（还没发起支付）、处理中
add(U(7), { total_amount: 2500, payable_amount: 2500, created_at: iso(4 * MIN), expires_at: iso(-26 * MIN), items: [planItem(std.id, std.name, 2500, 'CNY')] }, (o) =>
  o.intents.push(intentFor(o, 'epay', 'requires_action', iso(3 * MIN))),
)
add(U(7), { kind: 'topup', total_amount: 10000, payable_amount: 10000, created_at: iso(9 * MIN), expires_at: iso(-21 * MIN) }, (o) =>
  o.intents.push(intentFor(o, 'epay', 'failed', iso(8 * MIN), { failure_code: 'user_cancelled', failure_message: '渠道返回：用户取消支付' })),
)
add(U(3), { total_amount: 4500, payable_amount: 4500, created_at: iso(15 * MIN), expires_at: iso(-15 * MIN), manual_reason: '对公转账客户，先开单再付款', ...OPS })
// 超时未支付（仪表盘「超时未支付订单」数的就是这两张）：创建超过 30 分钟、过期作业还没处理，像是回调丢了
add(U(9), { total_amount: 4500, payable_amount: 4500, created_at: iso(2 * 60 * MIN), expires_at: iso(90 * MIN), items: [planItem(pro.id, pro.name, 4500, 'CNY')] }, (o) =>
  o.intents.push(intentFor(o, 'epay', 'requires_action', iso(2 * 60 * MIN - MIN))),
)
add(U(6), { total_amount: 2500, payable_amount: 2500, created_at: iso(6 * 60 * MIN), expires_at: iso(5 * 60 * MIN + 30 * MIN), items: [planItem(std.id, std.name, 2500, 'CNY')] }, (o) =>
  o.intents.push(intentFor(o, 'epay_backup', 'requires_action', iso(6 * 60 * MIN - 2 * MIN))),
)
add(U(11), { status: 'processing', total_amount: 69000, payable_amount: 69000, created_at: iso(22 * MIN), expires_at: iso(-8 * MIN), state_version: 2, items: [planItem(pro.id, pro.name, 69000, 'CNY', 'year')] }, (o) =>
  o.intents.push(intentFor(o, 'epay', 'processing', iso(21 * MIN))),
)
// 已支付：余额全额、美元、流量包（R32）、多项、充值
add(U(5), { status: 'fulfilled', total_amount: 6900, balance_applied: 6900, ...done(iso(3 * 60 * MIN)), items: [planItem(std.id, std.name, 6900, 'CNY', 'month', 3)] })
add(U(4), { status: 'fulfilled', currency: 'USD', total_amount: 890, payable_amount: 890, paid_amount: 890, ...done(iso(5 * 60 * MIN)), items: [planItem(pro.id, pro.name, 890, 'USD')] }, paidVia('epay_backup'))
add(U(1), { kind: 'addon', status: 'fulfilled', total_amount: 2500, payable_amount: 2500, paid_amount: 2500, ...done(todayIso(7 * 60 * MIN)) }, paidVia('epay'))
add(
  U(13),
  { status: 'fulfilled', total_amount: 7000, payable_amount: 7000, paid_amount: 7000, ...done(iso(20 * 60 * MIN)), items: [planItem(std.id, std.name, 4500, 'CNY'), planItem(null, '100 GB 加油包', 2500, 'CNY')] },
  paidVia('epay'),
)
add(U(9), { kind: 'topup', status: 'paid', total_amount: 20000, payable_amount: 20000, paid_amount: 20000, ...done(iso(DAY)), fulfilled_at: null, state_version: 2 }, (o) => {
  o.intents.push(intentFor(o, 'epay', 'failed', iso(DAY + 2 * MIN), { failure_code: 'timeout', failure_message: '渠道超时' }))
  paidVia('epay')(o)
})
// 人工：赠送（全额减免）、线下已收款（offline 入账没有支付尝试）
add(U(2), { status: 'fulfilled', subtotal_amount: 4500, discount_amount: 4500, total_amount: 0, ...done(iso(2 * DAY)), manual_reason: '工单 TK20260920 补偿一个月', ...OPS })
add(
  U(6),
  { status: 'fulfilled', total_amount: 69000, payable_amount: 69000, paid_amount: 69000, ...done(iso(3 * DAY)), manual_reason: '企业客户对公转账，年付', ...OPS, items: [planItem(pro.id, pro.name, 69000, 'CNY', 'year')] },
  (o) => o.payments.push(paymentFor(o, 'offline', null, o.created_at, 'offline:ICBC-20260921-0088')),
)
// 已取消、全额退款、部分退款
add(U(8), { status: 'cancelled', total_amount: 2500, payable_amount: 2500, created_at: iso(4 * DAY), cancelled_at: iso(4 * DAY - 10 * MIN), cancel_reason: '用户重复下单，保留另一张', state_version: 2 }, (o) =>
  o.intents.push(intentFor(o, 'epay', 'cancelled', o.created_at)),
)
// 已过期：用户没在 30 分钟内付款，之后钱才到（挂账「订单取消后到账」的来源）
for (const [i, days, amount] of [[14, 12, 4500], [15, 40, 6800]] as const)
  add(U(i), { status: 'expired', total_amount: amount, payable_amount: amount, created_at: iso(days * DAY + 60 * MIN), expires_at: iso(days * DAY + 30 * MIN), expired_at: iso(days * DAY + 30 * MIN), state_version: 2 }, (o) =>
    o.intents.push(intentFor(o, 'epay', 'expired', o.created_at)),
  )
function refunded(amount: number, reason: string, revoke: boolean) {
  return (o: Order) => {
    paidVia('epay')(o)
    const p = o.payments[0]!
    p.status = amount === o.paid_amount ? 'refunded' : 'partially_refunded'
    p.refunded_amount = amount
    const at = new Date(Date.parse(o.created_at) + DAY).toISOString()
    o.refunds.push({ id: randomUUID(), payment_id: p.id, provider_refund_id: `RF${o.order_no.slice(2)}`, currency: o.currency, amount, reason, status: 'succeeded', entitlement_revoked: revoke, commission_reversed: true, failure_message: null, succeeded_at: at, created_at: at })
  }
}
add(U(10), { status: 'refunded', total_amount: 4500, payable_amount: 4500, paid_amount: 4500, refunded_amount: 4500, ...done(iso(6 * DAY)), state_version: 5 }, refunded(4500, '线路不可用，全额退款', true))
add(U(12), { status: 'partially_refunded', total_amount: 69000, payable_amount: 69000, paid_amount: 69000, refunded_amount: 20000, ...done(iso(9 * DAY)), state_version: 5 }, refunded(20000, '降级补差', false))

/** 仪表盘 orders_pending_stale 的口径（契约后台-01）：pending_payment 且创建超过 thresholdMs 仍未被过期作业处理 */
export const stalePendingCount = (thresholdMs: number, now = Date.now()) => orders.filter((o) => o.status === 'pending_payment' && now - Date.parse(o.created_at) > thresholdMs).length

export const findOrder = (id: string | undefined) => (isUuid(id) ? orders.find((o) => o.id === id) : undefined)

// ---------------------------------------------------------------------------
// 视图：列表行（首项快照，R32 流量包单显示包名）、详情、支付记录
// ---------------------------------------------------------------------------
export function orderRow(o: Order) {
  const first = o.items[0]
  const pay = o.payments.at(-1) ?? null
  const code = pay?.provider_code ?? o.intents.at(-1)?.provider_code ?? null
  return {
    id: o.id,
    order_no: o.order_no,
    user_email: o.user_email,
    kind: o.kind,
    status: o.status,
    currency: o.currency,
    total_amount: o.total_amount,
    payable_amount: o.payable_amount,
    paid_amount: o.paid_amount,
    refunded_amount: o.refunded_amount,
    balance_applied: o.balance_applied,
    created_at: o.created_at,
    paid_at: o.paid_at,
    provider_code: code,
    provider_name: code ? providerName(code) : null,
    plan_name: first ? (first.plan_name ?? first.product_name) : '',
    interval: first?.interval ?? '',
    interval_count: first?.interval_count ?? 0,
    item_count: o.items.length,
    // R95 / R114：manual_reason 非空即人工单，与详情「来源」同口径
    manual: o.manual_reason !== null,
  }
}

export function orderDetail(o: Order) {
  return {
    ...orderRow(o),
    user_id: o.user_id,
    organization_id: null,
    state_version: o.state_version,
    subtotal_amount: o.subtotal_amount,
    discount_amount: o.discount_amount,
    tax_amount: 0,
    coupon_id: null,
    manual_reason: o.manual_reason,
    created_by: o.created_by,
    created_by_email: o.created_by_email,
    subscription_id: o.subscription_id,
    expires_at: o.expires_at,
    fulfilled_at: o.fulfilled_at,
    cancelled_at: o.cancelled_at,
    expired_at: o.expired_at,
    cancel_reason: o.cancel_reason,
    updated_at: o.updated_at,
    items: o.items.map((i) => ({
      ...i,
      product_id: randomUUIDFor(i.product_name),
      plan_version_id: i.plan_id ? randomUUIDFor(`${i.plan_id}-v${i.plan_version}`) : null,
      snapshot_entitlements: [],
      snapshot_quotas: [],
      line_amount: i.unit_amount * i.quantity,
      created_at: o.created_at,
    })),
  }
}

export function orderHistory(o: Order) {
  return {
    payment_intents: o.intents.map((i) => ({ ...i, provider_name: providerName(i.provider_code) })),
    payments: o.payments.map((p) => ({ ...p, provider_name: providerName(p.provider_code) })),
    refunds: o.refunds,
  }
}

/** 稳定的伪 uuid（订单项的 product / 版本 id 只是展示用） */
function randomUUIDFor(seed: string): string {
  let h = 2166136261
  for (const ch of seed) h = Math.imul(h ^ ch.charCodeAt(0), 16777619) >>> 0
  const hex = (n: number) => (n >>> 0).toString(16).padStart(8, '0')
  const a = hex(h)
  const b = hex(Math.imul(h, 2654435761))
  return `${a}-${b.slice(0, 4)}-4${b.slice(5, 8)}-8${a.slice(1, 4)}-${b}${a.slice(0, 4)}`
}

// 用户抽屉的「最近订单」与统计取这份订单表（users.ts 的详情按它出）
setOrderSource((userId) =>
  orders
    .filter((o) => o.user_id === userId)
    .sort((a, b) => b.created_at.localeCompare(a.created_at))
    .map(orderRow),
)

// ---------------------------------------------------------------------------
// 履约：赠送、线下已收款、标记已支付之后给用户开一条订阅（与后端开通同一时刻）
// ---------------------------------------------------------------------------
export function fulfil(o: Order, at: string): void {
  const u = userStore.find((x) => x.id === o.user_id)
  const item = o.items[0]
  const plan = plans.find((p) => p.id === item?.plan_id)
  o.status = 'fulfilled'
  o.fulfilled_at = at
  if (!u || !item || !plan) return
  const version = currentOf(plan)
  const traffic = version ? trafficOf(version) : null
  const sub: Sub = {
    id: randomUUID(),
    plan_id: plan.id,
    plan_name: plan.name,
    plan_version: version?.version ?? 1,
    status: 'active',
    current_period_start: at,
    current_period_end: new Date(Date.parse(at) + (item.interval === 'year' ? 365 : 30 * (item.interval_count ?? 1)) * DAY).toISOString(),
    amount: o.total_amount,
    currency: o.currency,
    auto_renew: false,
    traffic_limit: traffic,
    traffic_used: 0,
    device_limit_override: null,
    plan_max_devices: version?.max_devices ?? null,
    online_devices: 0,
    created_at: at,
    token: `tk-manual-${o.order_no}`,
  }
  u.subs.push(sub)
  o.subscription_id = sub.id
}

// ===========================================================================
// 挂账（保留规则 6）：订单取消后才到账、或超额扣款的钱，挂在 suspense 科目
// ===========================================================================
export interface LateCase {
  id: string
  case_kind: 'released_order' | 'excess_capture'
  status: 'suspense' | 'refund_pending' | 'refunded' | 'manual_review' | 'applied'
  amount: number
  currency: string
  order: Order
  received_at: string
  resolved_at?: string
  resolution_reason?: string
}
const cancelledOrder = orders.find((o) => o.status === 'cancelled')!
// 种子二里的两张过期单（种子一里的过期单都是随订阅生成的，不一定有）
const expiredOrders = orders.filter((o) => o.status === 'expired').slice(-2)
const lateSeed = (kind: LateCase['case_kind'], o: Order, amount: number, days: number, over: Partial<LateCase> = {}): LateCase => ({
  id: randomUUID(),
  case_kind: kind,
  status: 'suspense',
  amount,
  currency: o.currency,
  order: o,
  received_at: iso(days * DAY + 37 * MIN),
  ...over,
})
export const lateCases: LateCase[] = [
  lateSeed('released_order', cancelledOrder, 2500, 4),
  lateSeed('released_order', expiredOrders[0] ?? cancelledOrder, 4500, 12),
  lateSeed('excess_capture', orders.find((o) => o.currency === 'USD') ?? cancelledOrder, 120, 33, { currency: 'USD' }),
  lateSeed('released_order', expiredOrders[1] ?? cancelledOrder, 6800, 40, { status: 'manual_review' }),
  lateSeed('excess_capture', orders.find((o) => o.kind === 'topup' && o.status === 'paid') ?? cancelledOrder, 1000, 21, {
    status: 'applied',
    resolved_at: iso(20 * DAY),
    resolution_reason: '用户确认多付，转入余额',
  }),
]

export function lateView(c: LateCase) {
  return {
    id: c.id,
    case_kind: c.case_kind,
    status: c.status,
    amount: c.amount,
    currency: c.currency,
    order_no: c.order.order_no,
    order_status: c.order.status,
    user_id: c.order.user_id,
    user_email: c.order.user_email,
    received_at: c.received_at,
    // omitempty：没处理的不带这两个键
    ...(c.resolved_at ? { resolved_at: c.resolved_at } : {}),
    ...(c.resolution_reason ? { resolution_reason: c.resolution_reason } : {}),
  }
}

// ===========================================================================
// 渠道卡统计（R66）：今日成交按币种、近 24 小时成功率、最近回调
// ===========================================================================
export function providerView(p: Provider) {
  const today = todayLocal()
  const pays = orders.flatMap((o) => o.payments).filter((x) => x.provider_code === p.code)
  const sums: Record<string, number> = {}
  for (const x of pays) if (x.status === 'succeeded' && todayLocal(new Date(x.paid_at)) === today) sums[x.currency] = (sums[x.currency] ?? 0) + x.amount
  const since = Date.now() - DAY
  const terminal = orders
    .flatMap((o) => o.intents)
    .filter((i) => i.provider_code === p.code && Date.parse(i.created_at) >= since && ['succeeded', 'failed', 'cancelled', 'expired'].includes(i.status))
  const last = pays.map((x) => x.paid_at).sort().at(-1) ?? null
  return {
    ...p,
    today: sums,
    success_rate_24h: terminal.length ? terminal.filter((i) => i.status === 'succeeded').length / terminal.length : null,
    last_callback_at: p.code === 'offline' ? null : last,
  }
}

// ===========================================================================
// 收入调整（报表口径，只追加）
// ===========================================================================
export interface Adjustment {
  id: string
  currency: 'CNY' | 'USD'
  amount: number
  reason: string
  effective_on: string
  reversal_of?: string
  created_by: string
  created_by_email: string | null
  created_at: string
}
const adjSeed = (currency: Adjustment['currency'], amount: number, reason: string, days: number, email: string | null): Adjustment => ({
  id: randomUUID(),
  currency,
  amount,
  reason,
  effective_on: todayLocal(new Date(Date.now() - days * DAY)),
  created_by: randomUUID(),
  created_by_email: email,
  created_at: iso(days * DAY),
})
export const adjustments: Adjustment[] = [
  adjSeed('CNY', 240000, '线下对公转账补录 · 9 月', 12, 'admin@pandora.dev'),
  adjSeed('USD', -1200, '重复扣款退回，渠道已原路退款', 6, 'ops@pandora.dev'),
  adjSeed('CNY', 31240, '渠道手续费返还 · 8 月', 3, null),
]

export function adjustmentView(a: Adjustment) {
  const { reversal_of, ...rest } = a
  return { ...rest, ...(reversal_of ? { reversal_of } : {}), reversed: adjustments.some((x) => x.reversal_of === a.id) }
}
