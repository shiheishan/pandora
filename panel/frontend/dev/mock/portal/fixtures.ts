import { randomBytes, randomUUID } from 'node:crypto'
import type { AnonContext } from '../types.ts'
import { findPlan, findPrice, GIB, PACKS, PLANS, type CatalogPrice } from './catalog.ts'
import { buildProto } from './proto.ts'
import { profileName } from './purchase.ts'
import { isProto, onScenarioChange, scenario, SCENARIOS, switchScenario, type Scenario } from './scenario.ts'
import { pastOrders, seedLedger, seedRedemptions } from './seeds.ts'

export { GIB, scenario, SCENARIOS, type Scenario }
const DAY_MS = 86_400_000
/** 假后端的「站点时区」（修订 R50 默认值），按日用量按它切日 */
export const MOCK_TIMEZONE = 'Asia/Shanghai'
/** 配置名里的站点名（假后端外观默认的 site_name） */
export const MOCK_SITE = 'Pandora'

// ---------------------------------------------------------------------------
// 场景：POST /v1/__mock/portal-scenario {"name": "..."} 切换，切换即重建全部用户状态
//   default  一条生效订阅、一张待支付单、流量包、公告（含 critical）
//   empty    无订阅、无订单、无公告、无流量包
//   multi    两条生效订阅（第二条 past_due，节点 404、近 24 小时来源超设备上限、流量将尽）
//   legacy   同 default，但 Go 带 omitempty 的可缺席字段全部缺席（订单周期、支付方式、优惠码、有效期至），Telegram 站点未启用
//   error    页面读接口一律 500
//   slow     页面读接口延迟 2.5 秒（看骨架）
//   proto-*  购买流程原型的 11 个场景（proto.ts），原型三档目录；GET /v1/__mock/proto?s=&to= 切换并跳到页面
// ---------------------------------------------------------------------------
export const setScenario = switchScenario
onScenarioChange(() => states.clear())

/** 页面读接口的共同前置：slow 延迟、error 回 500；返回 false 时响应已写好。 */
export async function gate(ctx: AnonContext): Promise<boolean> {
  const current = scenario()
  if (current === 'slow') await new Promise((r) => setTimeout(r, 2500))
  if (current === 'error') {
    ctx.fail(500, 'internal_error', '服务暂时不可用（假后端 error 场景）')
    return false
  }
  return true
}

// ---------------------------------------------------------------------------
// 夹具
// ---------------------------------------------------------------------------
export interface NodeFixture {
  name: string
  protocol: string
  traffic_rate: number
}

export interface SubFixture {
  id: string
  plan_id: string
  price_id: string
  plan_name: string
  plan_version: number
  status: 'active' | 'past_due' | 'grace' | 'expired' | 'cancelled'
  /** 过期且续费窗口已关（renewal_closed_at 非空）：彻底停用 */
  renewalClosed?: boolean
  /** 用户起的备注名，没起为 null（订阅 label 列） */
  label: string | null
  /** 挂在这一份上的流量包余量（traffic_pack_grants.subscription_id） */
  packBytes: number
  /** 门户换新链接的时刻（毫秒），按份限频用 */
  rotations: number[]
  current_period_start: string
  current_period_end: string
  amount: number
  limitBytes: number
  deviceLimit: number | null
  online: number
  /** null = 没有可用凭据 */
  token: string | null
  fetchCount: number
  lastFetchedAt: string | null
  sources24h: number
  nodes: NodeFixture[]
  /** 每日用量，最后一个是今天；日期为 MOCK_TIMEZONE 的 YYYY-MM-DD */
  days: Array<{ date: string; bytes: number }>
  resetAt: string
}

/** 履约效果：支付完成（或 0 元当场）时对订阅 / 流量包做的事 */
export type OrderEffect =
  | { type: 'new'; planId: string; priceId: string; label: string | null }
  | { type: 'renewal'; subId: string; priceId: string }
  | { type: 'upgrade'; subId: string; planId: string; priceId: string; credit: number; refund: number }
  /** 差价不到支付最低额而免掉（SmallDue，设计稿 2.6 推荐 A） */
  | { type: 'addon'; bytes: number; subId: string }
  | { type: 'topup'; amount: number }
  | { type: 'none' }

export interface OrderFixture {
  id: string
  order_no: string
  kind: 'new' | 'renewal' | 'topup' | 'addon' | 'upgrade'
  status: string
  plan_name?: string
  item_name?: string
  interval?: string
  interval_count?: number
  /** 小计（目录价） */
  subtotal: number
  /** 优惠后（变更套餐另减折算） */
  total_amount: number
  discount_amount: number
  balance_applied: number
  payable_amount: number
  paid_amount: number
  refunded_amount?: number
  coupon_code?: string
  subscription_id?: string
  created_at: string
  paid_at?: string
  cancelled_at?: string
  cancel_reason?: string
  expires_at?: string
  payMethod?: string
  payProvider?: string
  /** 发起过支付（POST v1/orders/{id}/pay 过）；行上的 has_payment_intent 取它或已付渠道 */
  hasIntent?: boolean
  effect: OrderEffect
}

export interface AnnouncementFixture {
  id: string
  title: string
  body: string
  severity: 'info' | 'notice' | 'warning' | 'critical'
  pinned: boolean
  published_at: string | null
}

export interface PortalState {
  subs: SubFixture[]
  /** 还没加到任何一份的流量包余量（traffic_pack_grants.subscription_id 为空） */
  unattachedBytes: number
  /** 流量包转移流水（traffic_pack_transfers，追加写） */
  transfers: Array<{ from: string | null; to: string; bytes: number; at: string }>
  /** 余额（分）；下单抵扣当场扣，订单过期或取消时退回 */
  balance: number
  /** 余额流水（契约门户-05 history），新的在前 */
  ledger: LedgerEntry[]
  orders: OrderFixture[]
  announcements: AnnouncementFixture[]
  /** 我的礼品卡兑换记录，新的在前 */
  redemptions: RedemptionFixture[]
  /** 已兑换过的卡码（大写），再兑回 422 */
  usedCodes: Set<string>
}

export interface LedgerEntry {
  kind: string
  delta: number
  memo: string
  at: string
}

export interface RedemptionFixture {
  template_name: string
  type: string
  code_hint: string
  prize_label?: string
  balance?: number
  traffic_bytes?: number
  expire_days?: number
  redeemed_at: string
}

const states = new Map<string, PortalState>()

export function portalState(userId: string): PortalState {
  let state = states.get(userId)
  if (!state) {
    state = build(scenario())
    states.set(userId, state)
  }
  return state
}

export const newToken = (tail = '') => randomBytes(18).toString('base64url').slice(0, 24 - tail.length) + tail
export const linkUrl = (token: string) => `https://sub.pandora.dev/s/${token}`

function ymdInZone(t: number): string {
  return new Intl.DateTimeFormat('en-CA', { timeZone: MOCK_TIMEZONE, year: 'numeric', month: '2-digit', day: '2-digit' }).format(new Date(t))
}

/** 设计稿的走势：周末高一截，按 total 归一，今天只算到现在的一部分 */
export function usageDays(count: number, total: number, now = Date.now()): Array<{ date: string; bytes: number }> {
  const raw = Array.from({ length: count }, (_, i) => {
    const t = now - (count - 1 - i) * DAY_MS
    const wk = [0, 6].includes(new Date(t).getDay())
    const base = (wk ? 1.45 : 1) * (0.75 + 0.5 * Math.abs(Math.sin(i * 1.7 + 0.4)))
    return { date: ymdInZone(t), weight: i === count - 1 ? base * 0.45 : base }
  })
  const sum = raw.reduce((s, d) => s + d.weight, 0)
  return raw.map((d) => ({ date: d.date, bytes: Math.round((d.weight / sum) * total) }))
}

/** 某个 YYYY-MM-DD 在 MOCK_TIMEZONE（+08:00，无夏令时）的零点 */
const zoneMidnight = (ymd: string) => new Date(`${ymd}T00:00:00+08:00`).toISOString()

const DESIGN_NODES: NodeFixture[] = [
  { name: '香港 01 · 原生', protocol: 'vless', traffic_rate: 1 },
  { name: '香港 02 · IPLC', protocol: 'hysteria2', traffic_rate: 1.5 },
  { name: '东京 03', protocol: 'hysteria2', traffic_rate: 1 },
  { name: '新加坡 02', protocol: 'vless', traffic_rate: 1 },
  { name: '洛杉矶 01 · 流媒体', protocol: 'trojan', traffic_rate: 2 },
  { name: '台北 01', protocol: 'vmess', traffic_rate: 0.5 },
  { name: '法兰克福 01', protocol: 'shadowsocks', traffic_rate: 1 },
]

export function makeSub(init: {
  planId: string
  priceId: string
  status: SubFixture['status']
  usedGiB: number
  elapsedDays: number
  resetInDays: number
  expiresInDays: number
  online: number
  sources: number
  label?: string | null
  packBytes?: number
  /** 链接尾号（原型的 ····a3f9） */
  tail?: string
}): SubFixture {
  const plan = findPlan(init.planId)!
  const price = findPrice(plan, init.priceId)
  const now = Date.now()
  const days = usageDays(Math.max(1, init.elapsedDays), init.usedGiB * GIB, now)
  const resetDay = ymdInZone(now + init.resetInDays * DAY_MS)
  return {
    id: randomUUID(),
    plan_id: plan.id,
    price_id: init.priceId,
    plan_name: plan.name,
    plan_version: 2,
    status: init.status,
    label: init.label ?? null,
    packBytes: init.packBytes ?? 0,
    rotations: [],
    current_period_start: new Date(now - init.elapsedDays * DAY_MS).toISOString(),
    current_period_end: new Date(now + init.expiresInDays * DAY_MS).toISOString(),
    // 订阅上的 amount 是下单时的快照价；原价格已下架时目录里找不到它
    amount: price?.unit_amount ?? 2500,
    limitBytes: plan.trafficBytes ?? 0,
    deviceLimit: plan.max_devices,
    online: init.online,
    token: newToken(init.tail),
    fetchCount: init.sources ? 128 : 0,
    lastFetchedAt: init.sources ? new Date(now - 6 * 60_000).toISOString() : null,
    sources24h: init.sources,
    nodes: DESIGN_NODES,
    days,
    resetAt: zoneMidnight(resetDay),
  }
}

/** multi 场景第二条订阅用的已下架旧价格：目录里没有，续费时 renewal_price.available=false（续费遇改价） */
export const ARCHIVED_PRICE_ID = '6f1c2a10-0000-4000-8000-0000000000aa'

function build(s: Scenario): PortalState {
  if (isProto(s)) return buildProto(s)
  if (s === 'empty') return { subs: [], unattachedBytes: 0, transfers: [], balance: 2650, ledger: [], orders: [], announcements: [], redemptions: [], usedCodes: new Set() }
  const now = Date.now()
  const [std, pro] = [PLANS[0]!, PLANS[1]!]
  // 流量包 30 GB 挂在第一份上（00138 回填：生效中的里到期最晚的那份）
  const subs = [makeSub({ planId: pro.id, priceId: pro.prices[0]!.id, status: 'active', usedGiB: 312, elapsedDays: 28, resetInDays: 3, expiresInDays: 42, online: 3, sources: 3, packBytes: 30 * GIB })]
  if (s === 'multi') {
    const second = makeSub({ planId: std.id, priceId: ARCHIVED_PRICE_ID, status: 'past_due', usedGiB: 180, elapsedDays: 20, resetInDays: 10, expiresInDays: 5, online: 1, sources: 7 })
    second.nodes = []
    subs.push(second)
  }
  const pack = PACKS[0]!
  return {
    subs,
    unattachedBytes: 0,
    transfers: [],
    balance: 2650,
    ledger: seedLedger(now),
    redemptions: seedRedemptions(now),
    usedCodes: new Set(['GC-0830-B4NC-TRAF']),
    orders: [
      {
        id: randomUUID(),
        order_no: 'PD-2609-3397',
        kind: 'addon',
        status: 'pending_payment',
        plan_name: pack.name,
        item_name: pack.name,
        subtotal: pack.unit_amount,
        total_amount: pack.unit_amount,
        discount_amount: 0,
        balance_applied: 0,
        payable_amount: pack.unit_amount,
        paid_amount: 0,
        created_at: new Date(now - 12 * 60_000).toISOString(),
        expires_at: new Date(now + 18 * 60_000).toISOString(),
        subscription_id: subs[0]!.id,
        effect: { type: 'addon', bytes: pack.traffic_bytes, subId: subs[0]!.id },
      },
      ...pastOrders(now, subs[0]!),
    ],
    announcements: [
      { id: randomUUID(), title: '9 月 28 日凌晨 2:00–3:00 香港节点维护', body: '维护期间香港 01、02 会短暂中断，其他节点不受影响。', severity: 'critical', pinned: true, published_at: new Date(now - 2 * DAY_MS).toISOString() },
      { id: randomUUID(), title: '新增东京 03 节点，延迟更低', body: '已自动加入所有专业版与家庭版订阅，在客户端里更新订阅即可看到。', severity: 'notice', pinned: false, published_at: new Date(now - 5 * DAY_MS).toISOString() },
      { id: randomUUID(), title: '国庆活动：年付套餐 8 折', body: '10 月 1 日至 7 日，年付套餐下单自动打 8 折。', severity: 'info', pinned: false, published_at: new Date(now - 9 * DAY_MS).toISOString() },
    ],
  }
}

// ---------------------------------------------------------------------------
// 周期：period.AddInterval（AddDate 语义：月按日历月加，溢出顺延，与 JS setUTCMonth 一致）
// ---------------------------------------------------------------------------
export function addInterval(from: number, p: Pick<CatalogPrice, 'billing_interval' | 'interval_count'>): number {
  const d = new Date(from)
  const n = p.interval_count
  switch (p.billing_interval) {
    case 'day':
      return from + n * DAY_MS
    case 'week':
      return from + 7 * n * DAY_MS
    case 'quarter':
      d.setUTCMonth(d.getUTCMonth() + 3 * n)
      return d.getTime()
    case 'year':
      d.setUTCFullYear(d.getUTCFullYear() + n)
      return d.getTime()
    default:
      d.setUTCMonth(d.getUTCMonth() + n)
      return d.getTime()
  }
}

const LIVE = new Set(['active', 'trialing', 'grace', 'past_due'])
export const isLiveSub = (s: Pick<SubFixture, 'status'>) => LIVE.has(s.status)
/** 过期 30 天内、窗口没关：还能原地续费或换套餐（billing.subscriptionAcceptsPaidChange，w5expiry） */
export const isRevivable = (s: SubFixture, now = Date.now()) => s.status === 'expired' && !s.renewalClosed && now - new Date(s.current_period_end).getTime() < 30 * DAY_MS
/** 彻底停用：cancelled，或过期且窗口已关 */
export const isDead = (s: SubFixture, now = Date.now()) => !isLiveSub(s) && !isRevivable(s, now)
export const usedBytes = (s: SubFixture) => s.days.reduce((n, d) => n + d.bytes, 0)

/** 订阅当前计价：目录里还有就用目录价，下架了就是订阅上的快照价（renewal_price.available=false） */
export function subPrice(sub: SubFixture): CatalogPrice & { available: boolean } {
  const plan = findPlan(sub.plan_id)
  const listed = plan ? findPrice(plan, sub.price_id) : undefined
  return listed ? { ...listed, available: true } : { id: sub.price_id, currency: 'CNY', unit_amount: sub.amount, billing_interval: 'month', interval_count: 1, trial_days: 0, available: false }
}

export const clientName = (sub: Pick<SubFixture, 'label' | 'plan_name'>) => profileName(MOCK_SITE, sub.label, sub.plan_name)

// ---------------------------------------------------------------------------
// 契约门户-02 GET v1/me/subscriptions 的一行（含设计稿 2.9 的 label / client_name / changeable / renew_until，
// pack_remaining_bytes 改成这一份自己的余量）；Go 的 mySubscriptionView 无 omitempty，各场景字段恒在
// ---------------------------------------------------------------------------
export function subscriptionView(sub: SubFixture) {
  const consumed = usedBytes(sub)
  const plan = findPlan(sub.plan_id)
  const quota = {
    metric: 'traffic.bytes',
    limit: sub.limitBytes,
    consumed,
    remaining: Math.max(0, sub.limitBytes - consumed),
    period: plan?.quotaPeriod ?? 'cycle',
    period_start: zoneMidnight(sub.days[0]!.date),
    period_end: sub.resetAt,
    granted_addon: 0,
    adjusted: 0,
  }
  const price = subPrice(sub)
  const now = Date.now()
  // 与 renewable 同一口径，但不看 allow_renewal
  const changeable = isLiveSub(sub) || isRevivable(sub, now)
  const renewable = changeable && (plan?.allow_renewal ?? false)
  const base = isLiveSub(sub) ? new Date(sub.current_period_end).getTime() : now
  return {
    id: sub.id,
    plan_id: sub.plan_id,
    price_id: sub.price_id,
    plan_name: sub.plan_name,
    plan_version: sub.plan_version,
    status: sub.status,
    current_period_start: sub.current_period_start,
    current_period_end: sub.current_period_end,
    currency: 'CNY',
    amount: sub.amount,
    quotas: [quota],
    device_limit: sub.deviceLimit,
    online_devices: sub.online,
    quota_reset_strategy: plan?.quota_reset_strategy ?? 'billing_cycle',
    next_reset_at: sub.resetAt,
    renewable,
    renewal_price: { id: price.id, currency: price.currency, unit_amount: price.unit_amount, billing_interval: price.billing_interval, interval_count: price.interval_count, available: price.available },
    pack_remaining_bytes: sub.packBytes,
    label: sub.label,
    client_name: clientName(sub),
    changeable,
    renew_until: renewable ? new Date(addInterval(base, price)).toISOString() : null,
  }
}

export { zoneMidnight }
