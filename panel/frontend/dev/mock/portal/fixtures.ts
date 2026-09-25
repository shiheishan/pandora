/**
 * [INPUT]: 依赖 node:crypto 的 randomBytes / randomUUID，依赖 ../types 的 AnonContext，依赖 ./catalog 的套餐与流量包目录
 * [OUTPUT]: 对外提供 Scenario、SCENARIOS、setScenario、scenario、gate、portalState、subscriptionView、linkUrl、usageDays、makeSub、ARCHIVED_PRICE_ID、zoneMidnight、GIB、MOCK_TIMEZONE 与各夹具类型（含订单履约效果 OrderEffect）
 * [POS]: dev/mock/portal 的共享夹具：概览、我的订阅、订单、流量包、公告几个页面文件读同一份按用户建的内存状态（订阅挂在目录套餐上、ID 稳定、订阅地址可换发，另有余额与订单）；场景开关让浏览器实测空、多订阅、旧形状（待补字段缺席）、错误与慢加载，只在 dev 存在
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomBytes, randomUUID } from 'node:crypto'
import type { AnonContext } from '../types.ts'
import { findPlan, findPrice, GIB, PACKS, PLANS } from './catalog.ts'

export { GIB }
const DAY_MS = 86_400_000
/** 假后端的「站点时区」（修订 R50 默认值），按日用量按它切日 */
export const MOCK_TIMEZONE = 'Asia/Shanghai'

// ---------------------------------------------------------------------------
// 场景：POST /v1/__mock/portal-scenario {"name": "..."} 切换，切换即重建全部用户状态
//   default  一条生效订阅、一张待支付单、流量包、公告（含 critical）
//   empty    无订阅、无订单、无公告、无流量包
//   multi    两条生效订阅（第二条 past_due，节点 404、近 24 小时来源超设备上限、流量将尽）
//   legacy   同 default，但「待补·后端」字段全部缺席（对照后端当前实现）
//   error    页面读接口一律 500
//   slow     页面读接口延迟 2.5 秒（看骨架）
// ---------------------------------------------------------------------------
export const SCENARIOS = ['default', 'empty', 'multi', 'legacy', 'error', 'slow'] as const
export type Scenario = (typeof SCENARIOS)[number]

let current: Scenario = 'default'
export const scenario = () => current

export function setScenario(name: string): boolean {
  if (!(SCENARIOS as readonly string[]).includes(name)) return false
  current = name as Scenario
  states.clear()
  return true
}

/** 页面读接口的共同前置：slow 延迟、error 回 500；返回 false 时响应已写好。 */
export async function gate(ctx: AnonContext): Promise<boolean> {
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
  status: 'active' | 'past_due' | 'grace' | 'expired'
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
  | { type: 'new'; planId: string; priceId: string }
  | { type: 'renewal'; subId: string; priceId: string }
  | { type: 'upgrade'; subId: string; planId: string; priceId: string; credit: number; refund: number }
  | { type: 'addon'; bytes: number }
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
  coupon_code?: string
  subscription_id?: string
  created_at: string
  paid_at?: string
  cancelled_at?: string
  cancel_reason?: string
  expires_at?: string
  payMethod?: string
  payProvider?: string
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
  packBytes: number
  /** 余额（分）；下单抵扣当场扣，订单过期或取消时退回 */
  balance: number
  orders: OrderFixture[]
  announcements: AnnouncementFixture[]
}

const states = new Map<string, PortalState>()

export function portalState(userId: string): PortalState {
  let state = states.get(userId)
  if (!state) {
    state = build(current)
    states.set(userId, state)
  }
  return state
}

const newToken = () => randomBytes(18).toString('base64url')
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

export function makeSub(init: { planId: string; priceId: string; status: SubFixture['status']; usedGiB: number; elapsedDays: number; resetInDays: number; expiresInDays: number; online: number; sources: number }): SubFixture {
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
    current_period_start: new Date(now - init.elapsedDays * DAY_MS).toISOString(),
    current_period_end: new Date(now + init.expiresInDays * DAY_MS).toISOString(),
    // 订阅上的 amount 是下单时的快照价；原价格已下架时目录里找不到它
    amount: price?.unit_amount ?? 2500,
    limitBytes: plan.trafficBytes ?? 0,
    deviceLimit: plan.max_devices,
    online: init.online,
    token: newToken(),
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
  if (s === 'empty') return { subs: [], packBytes: 0, balance: 2650, orders: [], announcements: [] }
  const now = Date.now()
  const [std, pro] = [PLANS[0]!, PLANS[1]!]
  const subs = [makeSub({ planId: pro.id, priceId: pro.prices[0]!.id, status: 'active', usedGiB: 312, elapsedDays: 28, resetInDays: 3, expiresInDays: 42, online: 3, sources: 3 })]
  if (s === 'multi') {
    const second = makeSub({ planId: std.id, priceId: ARCHIVED_PRICE_ID, status: 'past_due', usedGiB: 180, elapsedDays: 20, resetInDays: 10, expiresInDays: 5, online: 1, sources: 7 })
    second.nodes = []
    subs.push(second)
  }
  const pack = PACKS[0]!
  return {
    subs,
    packBytes: 30 * GIB,
    balance: 2650,
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
        effect: { type: 'addon', bytes: pack.traffic_bytes },
      },
    ],
    announcements: [
      { id: randomUUID(), title: '9 月 28 日凌晨 2:00–3:00 香港节点维护', body: '维护期间香港 01、02 会短暂中断，其他节点不受影响。', severity: 'critical', pinned: true, published_at: new Date(now - 2 * DAY_MS).toISOString() },
      { id: randomUUID(), title: '新增东京 03 节点，延迟更低', body: '已自动加入所有专业版与家庭版订阅，在客户端里更新订阅即可看到。', severity: 'notice', pinned: false, published_at: new Date(now - 5 * DAY_MS).toISOString() },
      { id: randomUUID(), title: '国庆活动：年付套餐 8 折', body: '10 月 1 日至 7 日，年付套餐下单自动打 8 折。', severity: 'info', pinned: false, published_at: new Date(now - 9 * DAY_MS).toISOString() },
    ],
  }
}

// ---------------------------------------------------------------------------
// 契约门户-02 GET v1/me/subscriptions 的一行；legacy 场景去掉全部「待补·后端」字段
// ---------------------------------------------------------------------------
export function subscriptionView(sub: SubFixture, packBytes: number) {
  const consumed = sub.days.reduce((s, d) => s + d.bytes, 0)
  const legacy = current === 'legacy'
  const quota = {
    metric: 'traffic.bytes',
    limit: sub.limitBytes,
    consumed,
    remaining: Math.max(0, sub.limitBytes - consumed),
    ...(legacy ? {} : { period: 'cycle', period_start: zoneMidnight(sub.days[0]!.date), period_end: sub.resetAt, granted_addon: 0, adjusted: 0 }),
  }
  const base = {
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
  }
  if (legacy) return base
  const plan = findPlan(sub.plan_id)
  const listed = plan ? findPrice(plan, sub.price_id) : undefined
  const live = ['active', 'trialing', 'grace', 'past_due'].includes(sub.status)
  return {
    ...base,
    device_limit: sub.deviceLimit,
    online_devices: sub.online,
    quota_reset_strategy: plan?.quota_reset_strategy ?? 'billing_cycle',
    next_reset_at: sub.resetAt,
    renewable: live && (plan?.allow_renewal ?? false),
    renewal_price: listed
      ? { id: listed.id, currency: listed.currency, unit_amount: listed.unit_amount, billing_interval: listed.billing_interval, interval_count: listed.interval_count, available: true }
      : { id: sub.price_id, currency: 'CNY', unit_amount: sub.amount, billing_interval: 'month', interval_count: 1, available: false },
    pack_remaining_bytes: packBytes,
  }
}

export { zoneMidnight }
