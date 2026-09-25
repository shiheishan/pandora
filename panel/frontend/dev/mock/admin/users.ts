/**
 * [INPUT]: 依赖 node:crypto 的 randomUUID，依赖 ../types 的 MockModule / MockContext
 * [OUTPUT]: 对外提供 users 模块的假接口 MockModule，给 plans-store.ts 用的 PLAN_IDS、GROUPS 与 activeSubscriptions，给 billing-store.ts 用的 userStore、seedOrders / SeedOrder 与 setOrderSource（订单表归订单与收款，详情的最近订单取它登记的来源），以及 Sub / User 类型
 * [POS]: dev/mock/admin 的「用户（后台-03）」假接口，归后台前端一：列表（q 按邮箱 / 显示名 / 用户 id / 订阅令牌反查，status 逗号多值，group_id 含 none，sub_state，limit/offset）、详情、启停封禁、替用户设新密码、换发订阅链接（不回令牌）、人工调账、分配用户组、用户组列表、单订阅设备上限、风控画像；形状、权限、reauth、幂等与错误照 api-contract.md（含 R9 / R11 / R12 / R22）与 domain/adminops/users.go。订阅令牌只在内存里用于反查，任何响应都不返回
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomUUID } from 'node:crypto'
import type { MockContext, MockModule } from '../types.ts'
import { opsRoutes } from './users-ops.ts'

// ---------------------------------------------------------------------------
// 数据：确定性生成，vite 重启即复原。前 6 个用户的 id 与仪表盘流量排行里的一致，
// 从排行点进来能打开同一个人
// ---------------------------------------------------------------------------
const DAY = 86_400_000
const GiB = 1024 ** 3
const iso = (msAgo: number) => new Date(Date.now() - msAgo).toISOString()

type SubStatus = 'pending' | 'trialing' | 'active' | 'past_due' | 'grace' | 'paused' | 'cancelled' | 'expired'

export interface Sub {
  id: string
  plan_id: string
  plan_name: string
  plan_version: number
  status: SubStatus
  current_period_start: string | null
  current_period_end: string | null
  amount: number
  currency: string
  auto_renew: boolean
  traffic_limit: number | null
  traffic_used: number
  device_limit_override: number | null
  plan_max_devices: number | null
  online_devices: number
  created_at: string
  /** 只用于 q 反查，永不出现在响应里（保留规则 2） */
  token: string
}

export interface User {
  id: string
  email: string
  display_name: string | null
  status: 'pending' | 'active' | 'suspended' | 'banned' | 'deletion_scheduled' | 'anonymized'
  risk_level: 'trusted' | 'normal' | 'elevated' | 'high'
  group_id: string | null
  created_at: string
  last_login_at: string | null
  email_verified: boolean
  balance: number
  currency: string
  subs: Sub[]
  referrer: string | null
  telegram: { username: string; bound_at: string } | null
  roles: string[]
}

export interface Group {
  id: string
  code: string
  name: string
  description: string
  /** 引用计数：套餐可见性、专属价格、优惠券限定（后端按表实时数，这里是固定种子） */
  plans: number
  prices: number
  coupons: number
}

export const GROUPS: Group[] = [
  { id: '9c0e1a2b-2222-4b00-8000-000000000001', code: 'vip', name: 'VIP', description: '长期付费与大客户', plans: 2, prices: 3, coupons: 1 },
  { id: '9c0e1a2b-2222-4b00-8000-000000000002', code: 'enterprise', name: '企业客户', description: '对公结算', plans: 1, prices: 1, coupons: 0 },
  { id: '9c0e1a2b-2222-4b00-8000-000000000003', code: 'trial', name: '体验用户', description: '', plans: 0, prices: 0, coupons: 2 },
]

const PLANS: Array<[string, number, number | null, number | null]> = [
  // 名称, 月价（分）, 流量上限（GiB）, 套餐设备数
  ['标准版', 2500, 200, 3],
  ['专业版', 4500, 500, 5],
  ['家庭版', 6800, 1000, 8],
  ['体验版', 0, 20, 1],
]
// 套餐 id 固定：批量运营按 plan_id 筛当前订阅的套餐；plans-store.ts 用同一组 id 建目录
export const PLAN_IDS = PLANS.map((_, k) => `9c0e1a2b-3333-4b00-8000-00000000000${k + 1}`)

const DOMAINS = ['qq.com', 'gmail.com', '163.com', 'outlook.com', 'proton.me', 'icloud.com', 'foxmail.com']
const NAMES = ['zhang.wei', 'k.liu', 'wu.qing', 'm.chen', 'yao_ops', 'tomato', 'grace.h', 'lin.xiao', 'sec.check', 'mira', 'hu.jun', 'sun.yue', 'zhao.lei', 'qian.fei', 'luo.an', 'ma.teng', 'he.xin', 'gao.yu', 'lin.bo', 'xu.ke']

function makeSub(i: number, j: number, state: 'live' | 'expired'): Sub {
  const [name, price, gib, devices] = PLANS[(i + j) % PLANS.length]!
  const planId = PLAN_IDS[(i + j) % PLANS.length]!
  const start = Date.now() - ((i * 3 + j * 11) % 28) * DAY - (state === 'expired' ? 40 * DAY : 0)
  const end = start + 30 * DAY
  const status: SubStatus = state === 'expired' ? (j % 2 ? 'cancelled' : 'expired') : i % 9 === 4 ? 'trialing' : i % 13 === 7 ? 'grace' : 'active'
  const limit = gib === null ? null : gib * GiB
  const used = limit === null ? (i * 7 + 3) * GiB : Math.round(limit * (((i * 37 + j * 11) % 100) / 100))
  return {
    id: randomUUID(),
    plan_id: planId,
    plan_name: name,
    plan_version: 1 + ((i + j) % 3),
    status,
    current_period_start: new Date(start).toISOString(),
    current_period_end: new Date(end).toISOString(),
    amount: price,
    currency: 'CNY',
    auto_renew: i % 3 !== 0,
    traffic_limit: limit,
    traffic_used: used,
    device_limit_override: i % 11 === 5 ? 10 : null,
    plan_max_devices: devices,
    online_devices: state === 'live' ? (i * 5 + j) % ((devices ?? 3) + 2) : 0,
    created_at: new Date(start - DAY).toISOString(),
    token: `tk${String(i).padStart(3, '0')}${j}`,
  }
}

const FIXED_IDS = Array.from({ length: 6 }, (_, i) => `${(0x1a2b3c41 + i).toString(16)}-0000-4000-8000-00000000000${i + 1}`)

const users: User[] = Array.from({ length: 48 }, (_, i) => {
  const email = `${NAMES[i % NAMES.length]}${i >= NAMES.length ? Math.floor(i / NAMES.length) + 1 : ''}@${DOMAINS[i % DOMAINS.length]}`
  const subs: Sub[] = []
  if (i % 7 !== 6) subs.push(makeSub(i, 0, i % 8 === 3 ? 'expired' : 'live'))
  if (i % 10 === 2) subs.push(makeSub(i, 1, 'expired'))
  if (i % 12 === 1) subs.push(makeSub(i, 2, 'live'))
  return {
    id: FIXED_IDS[i] ?? randomUUID(),
    email,
    display_name: i % 4 === 0 ? `用户${i + 1}` : null,
    status: i % 15 === 9 ? 'suspended' : i % 23 === 17 ? 'banned' : i % 19 === 11 ? 'pending' : 'active',
    risk_level: i % 17 === 3 ? 'high' : i % 9 === 2 ? 'elevated' : i % 10 === 0 ? 'trusted' : 'normal',
    group_id: i % 5 === 1 ? GROUPS[0]!.id : i % 11 === 4 ? GROUPS[1]!.id : i % 13 === 6 ? GROUPS[2]!.id : null,
    created_at: iso((i * 3 + 2) * DAY),
    last_login_at: i % 6 === 5 ? null : iso(((i * 47) % 600) * 60_000),
    email_verified: i % 8 !== 7,
    balance: (i * 1375) % 26500,
    currency: 'CNY',
    subs,
    referrer: null,
    telegram: i % 3 === 0 ? { username: NAMES[i % NAMES.length]!.replace(/[._]/g, ''), bound_at: iso((i + 1) * DAY) } : null,
    roles: i === 0 ? ['support'] : [],
  }
})
// 邀请关系：每隔几个人由第 0 号邀请
for (let i = 2; i < users.length; i += 4) users[i]!.referrer = users[0]!.id

// ---------------------------------------------------------------------------
// 视图：列表行与详情，字段与 Go 的 json tag 一一对应
// ---------------------------------------------------------------------------
const LIVE = new Set<SubStatus>(['active', 'trialing', 'grace', 'past_due'])

/** 与后端 currentSubscriptionSQL 同一挑法：还在用的优先，其次到期最晚、最近创建 */
function current(u: User): Sub | undefined {
  return [...u.subs].sort(
    (a, b) =>
      Number(LIVE.has(b.status)) - Number(LIVE.has(a.status)) ||
      Date.parse(b.current_period_end ?? '') - Date.parse(a.current_period_end ?? '') ||
      b.created_at.localeCompare(a.created_at),
  )[0]
}

const groupName = (id: string | null) => GROUPS.find((g) => g.id === id)?.name ?? ''
const effectiveLimit = (s: Sub) => s.device_limit_override ?? s.plan_max_devices ?? 0

function row(u: User) {
  const cs = current(u)
  const activePlan = [...u.subs].filter((s) => s.status === 'active' || s.status === 'trialing').sort((a, b) => b.created_at.localeCompare(a.created_at))[0]
  return {
    id: u.id,
    email: u.email,
    display_name: u.display_name,
    status: u.status,
    risk_level: u.risk_level,
    group_name: groupName(u.group_id),
    group_id: u.group_id,
    created_at: u.created_at,
    last_login_at: u.last_login_at,
    subscription_count: u.subs.length,
    active_plan: activePlan?.plan_name ?? null,
    balance: u.balance,
    currency: u.currency,
    current_subscription: cs
      ? {
          id: cs.id,
          plan_name: cs.plan_name,
          status: cs.status,
          current_period_end: cs.current_period_end,
          traffic: { limit: cs.traffic_limit, consumed: cs.traffic_used },
          device_limit: effectiveLimit(cs),
          online_devices: cs.online_devices,
        }
      : null,
  }
}

// ---------------------------------------------------------------------------
// 订单归订单与收款（billing.ts）：它用 seedOrders 建订单表，再经 setOrderSource 登记回来，
// 详情的最近订单与统计就与订单页同一份数据（人工开单、标记已支付在抽屉里看得见）；没登记时退回种子
// ---------------------------------------------------------------------------
export type SeedOrder = ReturnType<typeof seedOrders>[number]

/** 每条订阅一张订单：首张新购、其余续费，免费套餐直接履约，每第三张已过期（没付） */
export function seedOrders(u: User) {
  return u.subs.map((s, k) => ({
    id: randomUUIDFor(`${u.id}-o${k}`),
    order_no: `PD${u.created_at.slice(2, 10).replaceAll('-', '')}${String(k + 1).padStart(4, '0')}`,
    user_email: u.email,
    kind: (k === 0 ? 'new' : 'renewal') as string,
    status: (s.amount === 0 ? 'fulfilled' : k % 3 === 2 ? 'expired' : 'paid') as string,
    currency: s.currency,
    total_amount: s.amount,
    payable_amount: s.amount,
    paid_amount: k % 3 === 2 ? 0 : s.amount,
    refunded_amount: 0,
    balance_applied: 0,
    created_at: s.created_at,
    paid_at: (k % 3 === 2 ? null : s.created_at) as string | null,
    provider_code: (s.amount ? 'epay' : null) as string | null,
    provider_name: (s.amount ? '聚合收银台' : null) as string | null,
    plan_name: s.plan_name,
    interval: 'month',
    interval_count: 1,
    item_count: 1,
    // 以下不是列表行字段：billing.ts 建订单表时用，详情响应前剥掉
    sub_id: s.id,
    plan_id: s.plan_id,
  }))
}

type OrderView = Omit<SeedOrder, 'sub_id' | 'plan_id'>
let orderSource: ((userId: string) => OrderView[]) | null = null
/** 剥掉只给 billing.ts 建表用的两个键 */
function listView(o: SeedOrder): OrderView {
  const view: Partial<SeedOrder> = { ...o }
  delete view.sub_id
  delete view.plan_id
  return view as OrderView
}
export function setOrderSource(source: (userId: string) => OrderView[]): void {
  orderSource = source
}

function detail(u: User) {
  const orders: OrderView[] = orderSource ? orderSource(u.id) : seedOrders(u).map(listView)
  const referrer = users.find((r) => r.id === u.referrer)
  return {
    ...row(u),
    // 详情不填这三个（后端 GetUser 零值输出）
    subscription_count: 0,
    active_plan: null,
    current_subscription: null,
    email_verified: u.email_verified,
    subscriptions: [...u.subs]
      .sort((a, b) => b.created_at.localeCompare(a.created_at))
      .map((s) => ({
        id: s.id,
        plan_name: s.plan_name,
        plan_version: s.plan_version,
        status: s.status,
        current_period_start: s.current_period_start,
        current_period_end: s.current_period_end,
        amount: s.amount,
        currency: s.currency,
        auto_renew: s.auto_renew,
        quotas: [{ metric: 'traffic.bytes', limit: s.traffic_limit, consumed: s.traffic_used, remaining: s.traffic_limit === null ? null : Math.max(0, s.traffic_limit - s.traffic_used) }],
        device_limit_override: s.device_limit_override,
        plan_max_devices: s.plan_max_devices,
        online_devices: s.online_devices,
      })),
    recent_orders: orders.sort((a, b) => b.created_at.localeCompare(a.created_at)).slice(0, 20),
    roles: u.roles,
    stats: {
      paid_total: orders.filter((o) => o.status === 'paid' || o.status === 'fulfilled').reduce((sum, o) => sum + o.paid_amount, 0),
      order_count: orders.length,
      referral_count: users.filter((r) => r.referrer === u.id).length,
    },
    referrer: referrer ? { id: referrer.id, email: referrer.email } : null,
    telegram: u.telegram,
  }
}

/** 稳定的伪 uuid：同一个种子得到同一个 id（订单没有独立存储，刷新后 id 不跳） */
function randomUUIDFor(seed: string): string {
  let h = 2166136261
  for (const ch of seed) h = Math.imul(h ^ ch.charCodeAt(0), 16777619) >>> 0
  const hex = (n: number) => (n >>> 0).toString(16).padStart(8, '0')
  const a = hex(h)
  const b = hex(Math.imul(h, 2654435761))
  const c = hex(Math.imul(h ^ 0x9e3779b9, 40503))
  return `${a}-${b.slice(0, 4)}-4${b.slice(5, 8)}-8${c.slice(1, 4)}-${c}${a.slice(0, 4)}`
}

function hasSubState(u: User, state: string): boolean {
  const live = u.subs.some((s) => LIVE.has(s.status))
  if (state === 'active') return live
  if (state === 'expired') return u.subs.length > 0 && !live
  return u.subs.length === 0
}

function envelope(code: string, message: string, fields?: Record<string, string>) {
  return { error: { code, message, ...(fields ? { fields } : {}) } }
}

const reasonOk = (v: unknown) => typeof v === 'string' && [...v.trim()].length >= 5
const findUser = (ctx: MockContext) => users.find((u) => u.id === ctx.params.id)

function passwordProblem(p: string): string | null {
  if ([...p].length < 8) return '密码至少需要 8 个字符'
  if (Buffer.byteLength(p) > 256) return '密码过长'
  if (!/\p{L}/u.test(p) || !/\p{Nd}/u.test(p)) return '密码必须同时包含字母和数字'
  return null
}

/** 套餐列表的「有效订阅」（plans-store.ts 用）：与 adminops.ListPlans 同口径，status 为 active 或 trialing */
export function activeSubscriptions(planId: string): number {
  return users.reduce((n, u) => n + u.subs.filter((s) => s.plan_id === planId && (s.status === 'active' || s.status === 'trialing')).length, 0)
}

/** 同一份用户数组：billing.ts 开单开订阅、挂账转入余额时直接改这里 */
export const userStore: User[] = users

export const users_: MockModule = {
  routes: {
    // ---- 读 -----------------------------------------------------------------
    'GET /v1/users': (ctx) => {
      if (!ctx.requirePermission('iam.user.read')) return
      const qs = ctx.query
      const group = (qs.get('group_id') ?? '').trim()
      const subState = qs.get('sub_state') ?? ''
      const fields: Record<string, string> = {}
      if (group && group !== 'none' && !/^[0-9a-f-]{36}$/i.test(group)) fields.group_id = '用户组必须是 uuid 或 none'
      if (!['', 'active', 'expired', 'none'].includes(subState)) fields.sub_state = '订阅状态只能是 active、expired 或 none'
      if (Object.keys(fields).length) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', fields)
      const q = (qs.get('q') ?? '').trim().toLowerCase()
      // 粘贴整条订阅地址时取最后一段
      const token = q.split('/').at(-1)?.split('?')[0] ?? ''
      const statuses = (qs.get('status') ?? '').split(',').map((s) => s.trim()).filter(Boolean)
      let limit = Number.parseInt(qs.get('limit') ?? '', 10)
      if (!(limit > 0 && limit <= 100)) limit = 25
      const offset = Math.max(0, Number.parseInt(qs.get('offset') ?? '', 10) || 0)
      const hits = users
        .filter(
          (u) =>
            !q ||
            u.email.toLowerCase().includes(q) ||
            (u.display_name ?? '').toLowerCase().includes(q) ||
            u.id === q ||
            u.subs.some((s) => s.token === token && s.status !== 'cancelled'),
        )
        .filter((u) => statuses.length === 0 || statuses.includes(u.status))
        .filter((u) => !group || (group === 'none' ? u.group_id === null : u.group_id === group))
        .filter((u) => !subState || hasSubState(u, subState))
        .sort((a, b) => b.created_at.localeCompare(a.created_at))
      ctx.send(200, { users: hits.slice(offset, offset + limit).map(row), total: hits.length })
    },

    'GET /v1/user-groups': (ctx) => {
      if (!ctx.requirePermission('iam.user.read')) return
      ctx.send(200, { groups: GROUPS.map((g) => ({ ...g, users: users.filter((u) => u.group_id === g.id).length })) })
    },

    'GET /v1/users/:id': (ctx) => {
      if (!ctx.requirePermission('iam.user.read')) return
      const u = findUser(ctx)
      if (!u) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      ctx.send(200, detail(u))
    },

    // 风控画像：security.audit.read；明文 IP 只在这里
    'GET /v1/users/:id/profile': (ctx) => {
      if (!ctx.requirePermission('security.audit.read')) return
      const u = findUser(ctx)
      const n = u ? users.indexOf(u) : 0
      const ip = (k: number) => `112.64.${(n * 7 + k) % 250}.${(k * 31 + 18) % 250}`
      const shared = n % 4 === 2
      ctx.send(200, {
        events: Array.from({ length: 6 }, (_, k) => ({ action: k % 3 ? 'auth.login' : 'user.password_changed', outcome: k === 4 ? 'failure' : 'success', ip: ip(k % 2), ua: 'Mozilla/5.0', domain: 'public', at: iso((k * 9 + 1) * 3_600_000) })),
        ips: [0, 1].map((k) => ({ ip: ip(k), count: 12 - k * 7, first: iso(20 * DAY), last: iso((k + 1) * 3_600_000), accounts: shared && k === 0 ? 3 : 1 })),
        related: shared ? users.filter((r) => r !== u).slice(n % 5, (n % 5) + 2).map((r) => ({ id: r.id, email: r.email })) : [],
        fetches: Array.from({ length: 5 }, (_, k) => ({ ip: ip(k % (shared ? 5 : 2)), ua: 'clash-verge/2.0', family: k % 2 ? 'clash' : 'sing-box', result: k === 3 ? 'denied' : 'ok', format: k % 2 ? 'clash' : 'singbox', at: iso((k * 5 + 2) * 3_600_000) })),
        fetch_sources_7d: shared ? 6 : 1 + (n % 3),
        registered_ip: n % 5 === 0 ? '' : ip(9),
      })
    },

    // ---- 写 -----------------------------------------------------------------
    // 启用 / 停用 / 封禁：iam.user.write + reauth（R9），无幂等；停用封禁要原因（422 无 fields）
    'POST /v1/users/:id/status': async (ctx) => {
      if (!ctx.requirePermission('iam.user.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const status = typeof body.status === 'string' ? body.status : ''
      if (!['active', 'suspended', 'banned'].includes(status)) return ctx.fail(400, 'bad_request', '不支持的账号状态')
      if (status !== 'active' && !(typeof body.reason === 'string' && body.reason.trim())) return ctx.fail(422, 'validation_failed', '停用或封禁必须填写原因')
      if (ctx.params.id === ctx.user.userId) return ctx.fail(409, 'conflict', '不能变更自己的账号状态')
      const u = findUser(ctx)
      if (!u) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      u.status = status as User['status']
      ctx.send(200, { ok: true, status })
    },

    // 设新密码：iam.user.write + reauth，无幂等；不回显新密码
    'POST /v1/users/:id/reset-password': async (ctx) => {
      if (!ctx.requirePermission('iam.user.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      if (!reasonOk(body.reason)) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { reason: '请写清为什么要改这个用户的密码，5 到 500 字。这条会进审计' })
      if (ctx.params.id === ctx.user.userId) return ctx.fail(400, 'bad_request', '改自己的密码请用「修改密码」，那里会先验证当前密码')
      const problem = passwordProblem(typeof body.new_password === 'string' ? body.new_password : '')
      if (problem) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { password: problem })
      if (!findUser(ctx)) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      ctx.send(200, { ok: true, sessions_revoked: true })
    },

    // 换发订阅链接：path 是订阅 id；iam.user.write + reauth；R11 不回令牌
    'POST /v1/subscriptions/:id/rotate': async (ctx) => {
      if (!ctx.requirePermission('iam.user.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      if (!reasonOk(body.reason)) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { reason: '请写清为什么要换这条订阅链接，至少 5 个字。这条会进审计' })
      const owner = users.find((u) => u.subs.some((s) => s.id === ctx.params.id))
      const sub = owner?.subs.find((s) => s.id === ctx.params.id)
      if (!owner || !sub) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      sub.token = `tk${randomUUID().slice(0, 8)}`
      ctx.send(200, { user_email: owner.email, old_revoked: true })
    },

    // 人工调账：billing.provider.write + reauth + 幂等 admin_user_balance_adjust
    'POST /v1/users/:id/balance': async (ctx) => {
      if (!ctx.requirePermission('billing.provider.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('admin_user_balance_adjust', () => {
        const amount = typeof body.amount === 'number' && Number.isInteger(body.amount) ? body.amount : 0
        if (amount === 0) return { status: 422, body: envelope('validation_failed', '调整金额不能为 0') }
        if (!reasonOk(body.reason)) return { status: 422, body: envelope('validation_failed', '请求参数校验未通过', { reason: '调整原因至少 5 个字' }) }
        const u = findUser(ctx)
        if (!u) return { status: 404, body: envelope('not_found', '资源不存在或无权访问') }
        if (u.balance + amount < 0) return { status: 409, body: envelope('conflict', '余额不足，无法扣减') }
        u.balance += amount
        return { status: 200, body: { balance: u.balance } }
      })
    },

    // 分配用户组：iam.user.write，无幂等；空串移出分组；不存在 422 无 fields
    'POST /v1/users/:id/group': async (ctx) => {
      if (!ctx.requirePermission('iam.user.write')) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const groupId = typeof body.group_id === 'string' ? body.group_id : ''
      const u = findUser(ctx)
      if (!u || (groupId && !GROUPS.some((g) => g.id === groupId))) return ctx.fail(422, 'validation_failed', '用户或分组不存在')
      u.group_id = groupId || null
      ctx.send(200, { ok: true })
    },

    // 单订阅设备上限：iam.user.write，无幂等；limit 0–1000 或 null（R12：不存在 404）
    'POST /v1/subscriptions/:id/device-limit': async (ctx) => {
      if (!ctx.requirePermission('iam.user.write')) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const limit = body.limit
      if (limit !== null && !(typeof limit === 'number' && Number.isInteger(limit) && limit >= 0 && limit <= 1000)) return ctx.fail(422, 'validation_failed', '设备数需在 0 到 1000 之间')
      const sub = users.flatMap((u) => u.subs).find((s) => s.id === ctx.params.id)
      if (!sub) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      sub.device_limit_override = limit as number | null
      ctx.send(200, { ok: true })
    },

    // 第 ④ 步：用户组增删改、批量运营、设备策略、流量重置（users-ops.ts）
    ...opsRoutes({ users, groups: GROUPS, current, hasSubState, effectiveLimit }),
  },
}

export { users_ as users }
