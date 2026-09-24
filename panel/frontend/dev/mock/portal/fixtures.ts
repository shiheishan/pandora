/**
 * [INPUT]: 依赖 node:crypto 的 randomBytes / randomUUID，依赖 ../types 的 AnonContext
 * [OUTPUT]: 对外提供 Scenario、SCENARIOS、setScenario、scenario、gate、portalState、subscriptionView、linkUrl、usageDays、zoneMidnight、GIB、MOCK_TIMEZONE 与各夹具类型
 * [POS]: dev/mock/portal 的共享夹具：概览、我的订阅、订单、流量包、公告几个页面文件读同一份按用户建的内存状态（订阅 ID 稳定、订阅地址可换发）；场景开关让浏览器实测空、多订阅、旧形状（待补字段缺席）、错误与慢加载，只在 dev 存在
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomBytes, randomUUID } from 'node:crypto'
import type { AnonContext } from '../types.ts'

export const GIB = 1024 ** 3
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

export interface OrderFixture {
  id: string
  order_no: string
  kind: 'new' | 'renewal' | 'topup' | 'addon' | 'upgrade'
  status: string
  plan_name?: string
  item_name?: string
  interval?: string
  interval_count?: number
  total_amount: number
  created_at: string
  expires_at?: string
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

function makeSub(init: { plan: string; status: SubFixture['status']; limitGiB: number; usedGiB: number; elapsedDays: number; resetInDays: number; expiresInDays: number; deviceLimit: number | null; online: number; sources: number; amount: number }): SubFixture {
  const now = Date.now()
  const days = usageDays(init.elapsedDays, init.usedGiB * GIB, now)
  const resetDay = ymdInZone(now + init.resetInDays * DAY_MS)
  return {
    id: randomUUID(),
    plan_id: randomUUID(),
    price_id: randomUUID(),
    plan_name: init.plan,
    plan_version: 3,
    status: init.status,
    current_period_start: new Date(now - 18 * DAY_MS).toISOString(),
    current_period_end: new Date(now + init.expiresInDays * DAY_MS).toISOString(),
    amount: init.amount,
    limitBytes: init.limitGiB * GIB,
    deviceLimit: init.deviceLimit,
    online: init.online,
    token: newToken(),
    fetchCount: 128,
    lastFetchedAt: new Date(now - 6 * 60_000).toISOString(),
    sources24h: init.sources,
    nodes: DESIGN_NODES,
    days,
    resetAt: zoneMidnight(resetDay),
  }
}

function build(s: Scenario): PortalState {
  if (s === 'empty') return { subs: [], packBytes: 0, orders: [], announcements: [] }
  const now = Date.now()
  const subs = [makeSub({ plan: '专业版', status: 'active', limitGiB: 500, usedGiB: 312, elapsedDays: 28, resetInDays: 3, expiresInDays: 42, deviceLimit: 5, online: 3, sources: 3, amount: 5900 })]
  if (s === 'multi') {
    const second = makeSub({ plan: '标准版', status: 'past_due', limitGiB: 100, usedGiB: 88, elapsedDays: 20, resetInDays: 10, expiresInDays: 5, deviceLimit: 3, online: 1, sources: 7, amount: 2900 })
    second.nodes = []
    subs.push(second)
  }
  return {
    subs,
    packBytes: 30 * GIB,
    orders: [
      {
        id: randomUUID(),
        order_no: 'PD-2609-3397',
        kind: 'addon',
        status: 'pending_payment',
        plan_name: '100 GB',
        item_name: '100 GB',
        total_amount: 1500,
        created_at: new Date(now - 12 * 60_000).toISOString(),
        expires_at: new Date(now + 18 * 60_000).toISOString(),
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
  return {
    ...base,
    device_limit: sub.deviceLimit,
    online_devices: sub.online,
    quota_reset_strategy: 'billing_cycle',
    next_reset_at: sub.resetAt,
    renewable: true,
    renewal_price: { id: sub.price_id, currency: 'CNY', unit_amount: sub.amount, billing_interval: 'month', interval_count: 1, available: true },
    pack_remaining_bytes: packBytes,
  }
}

export { zoneMidnight }
