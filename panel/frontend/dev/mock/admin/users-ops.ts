/**
 * [INPUT]: 依赖 node:crypto 的 randomInt / randomUUID，依赖 ../types 的 Json / MockContext / MockResult / MockRoute，依赖 ./users 的 User / Sub / Group 类型（只取类型，运行时不回引）
 * [OUTPUT]: 对外提供 opsRoutes(store)：用户模块第 ④ 步的假接口，由 users.ts 展开进同一个 MockModule
 * [POS]: dev/mock/admin 的「用户（后台-03）」其余四个标签：用户组增删改、批量运营（预览 / 导出 CSV / 批量生成 / 群发）、设备策略（在线订阅与全局模式）、流量重置（日志、统计、单用户历史、手动重置）。形状、权限、reauth、幂等 scope、校验顺序与文案照 api-contract.md 后台-03（含 R9 / R12 / R38）与 Go 处理器（usergroup.go、bulk_users.go、adminops/bulk_*.go、devices.go、billing/traffic_reset.go）；请求体按 DisallowUnknownFields 拒绝未知字段。与 users.ts 共用同一份用户数组，生成的账号、重置清掉的用量在列表和详情里立即可见
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomInt, randomUUID } from 'node:crypto'
import type { Json, MockContext, MockResult, MockRoute } from '../types.ts'
import type { Group, Sub, User } from './users.ts'

export interface UsersStore {
  users: User[]
  groups: Group[]
  /** 当前订阅：与后端 currentSubscriptionSQL 同一挑法 */
  current(u: User): Sub | undefined
  hasSubState(u: User, state: string): boolean
  effectiveLimit(s: Sub): number
}

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------
const DAY = 86_400_000
const GiB = 1024 ** 3
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const runes = (v: string) => [...v].length

function envelope(code: string, message: string, fields?: Record<string, string>) {
  return { error: { code, message, ...(fields ? { fields } : {}) } }
}
const err = (status: number, code: string, message: string, fields?: Record<string, string>): MockResult => ({ status, body: envelope(code, message, fields) })

/** 与后端 DisallowUnknownFields 一致：多一个字段就 400 */
function unknownField(body: Json, allowed: readonly string[]): string | null {
  return Object.keys(body).find((k) => !allowed.includes(k)) ?? null
}

// ---------------------------------------------------------------------------
// 批量筛选：预览、导出、群发共用一份（后端 buildFilterSQL）
// ---------------------------------------------------------------------------
interface Filter {
  status: string
  group_id: string
  query: string
  has_active_sub: boolean | null
  plan_id: string
  expires_within_days: number
  sub_state: string
}
const FILTER_KEYS = ['status', 'group_id', 'query', 'has_active_sub', 'plan_id', 'expires_within_days', 'sub_state'] as const

function filterFromBody(b: Json): Filter {
  const str = (k: string) => (typeof b[k] === 'string' ? (b[k] as string) : '')
  const days = b.expires_within_days
  return {
    status: str('status'),
    group_id: str('group_id'),
    query: str('query'),
    has_active_sub: typeof b.has_active_sub === 'boolean' ? b.has_active_sub : null,
    plan_id: str('plan_id'),
    expires_within_days: typeof days === 'number' && Number.isInteger(days) ? days : days === undefined || days === null ? 0 : -1,
    sub_state: str('sub_state'),
  }
}

function filterFromQuery(q: URLSearchParams): Filter {
  const raw = q.get('expires_within_days')
  const n = raw ? Number(raw) : 0
  const has = q.get('has_active_sub')
  return {
    status: q.get('status') ?? '',
    group_id: q.get('group_id') ?? '',
    query: q.get('query') ?? '',
    has_active_sub: has === 'true' ? true : has === 'false' ? false : null,
    plan_id: q.get('plan_id') ?? '',
    // 不是整数时按越界处理，交给同一处校验回 422
    expires_within_days: Number.isInteger(n) ? n : -1,
    sub_state: q.get('sub_state') ?? '',
  }
}

/** 校验顺序同 BulkFilter.validate：status 不合法单独先回，其余字段合并 */
function filterProblem(f: Filter): MockResult | null {
  if (!['', 'pending', 'active', 'suspended', 'banned', 'deletion_scheduled'].includes(f.status)) return err(422, 'validation_failed', '请求参数校验未通过', { status: '不支持的用户状态' })
  const fields: Record<string, string> = {}
  if (f.group_id && f.group_id.length !== 36) fields.group_id = '分组标识格式不正确'
  if (f.plan_id && !UUID.test(f.plan_id)) fields.plan_id = '套餐标识格式不正确'
  if (f.expires_within_days < 0 || f.expires_within_days > 365) fields.expires_within_days = '到期天数只能是 1–365'
  if (!['', 'active', 'expired', 'none'].includes(f.sub_state)) fields.sub_state = '订阅状态只能是 active、expired 或 none'
  return Object.keys(fields).length ? err(422, 'validation_failed', '请求参数校验未通过', fields) : null
}

function matcher(store: UsersStore, f: Filter) {
  const now = Date.now()
  const q = f.query.trim().toLowerCase()
  return (u: User) => {
    if (u.status === 'anonymized') return false
    if (f.status && u.status !== f.status) return false
    if (f.group_id && u.group_id !== f.group_id) return false
    if (q && !u.email.toLowerCase().includes(q)) return false
    if (f.has_active_sub !== null && u.subs.some((s) => s.status === 'active') !== f.has_active_sub) return false
    const cs = store.current(u)
    if (f.plan_id && cs?.plan_id !== f.plan_id) return false
    if (f.expires_within_days > 0) {
      const end = cs?.current_period_end ? Date.parse(cs.current_period_end) : NaN
      if (!(end >= now && end < now + f.expires_within_days * DAY)) return false
    }
    if (f.sub_state && !store.hasSubState(u, f.sub_state)) return false
    return true
  }
}

const byNewest = (a: User, b: User) => b.created_at.localeCompare(a.created_at)

/** Go 的 csv.Writer：含逗号、引号、换行的格加引号 */
function csvLine(cells: readonly string[]): string {
  return cells.map((c) => (/[",\r\n]/.test(c) ? `"${c.replace(/"/g, '""')}"` : c)).join(',')
}
const goTime = (iso: string) => iso.slice(0, 16).replace('T', ' ')

// ---------------------------------------------------------------------------
// 流量重置日志：确定性种子，user_id 只在内部用来按人筛
// ---------------------------------------------------------------------------
const REASONS = ['renewal', 'cycle_roll', 'manual', 'gift_card', 'plan_change'] as const
type Reason = (typeof REASONS)[number]

interface ResetLog {
  id: string
  user_id: string
  user_email: string
  plan_name: string
  reason: Reason
  consumed_before: number
  actor_email: string
  note: string
  created_at: string
}

/** 响应形状：plan_name / actor_email / note 是 omitempty，空时整键省略 */
function logView(l: ResetLog) {
  return {
    id: l.id,
    user_email: l.user_email,
    ...(l.plan_name ? { plan_name: l.plan_name } : {}),
    metric: 'traffic.bytes',
    reason: l.reason,
    consumed_before: l.consumed_before,
    ...(l.actor_email ? { actor_email: l.actor_email } : {}),
    ...(l.note ? { note: l.note } : {}),
    created_at: l.created_at,
  }
}

function seedLogs(users: readonly User[]): ResetLog[] {
  const order: Reason[] = ['renewal', 'cycle_roll', 'renewal', 'manual', 'gift_card', 'cycle_roll', 'plan_change']
  const out: ResetLog[] = []
  for (let i = 0; i < 72; i++) {
    const u = users[(i * 7) % users.length]!
    const sub = u.subs[0]
    const reason = order[i % order.length]!
    out.push({
      id: randomUUID(),
      user_id: u.id,
      user_email: u.email,
      plan_name: sub?.plan_name ?? '',
      reason,
      consumed_before: (((i * 37) % 480) + 12) * GiB + ((i * 7919) % 1000) * 1024 ** 2,
      actor_email: reason === 'manual' ? (i % 2 ? 'admin@pandora.dev' : 'zhou.min@pandora.dev') : '',
      note: reason === 'manual' ? `工单 #${4700 + i} 用户申诉，补偿断线时长` : '',
      created_at: new Date(Date.now() - i * 0.85 * DAY - ((i * 13) % 24) * 3_600_000).toISOString(),
    })
  }
  return out
}

// ---------------------------------------------------------------------------
// 路由
// ---------------------------------------------------------------------------
export function opsRoutes(store: UsersStore): Record<string, MockRoute> {
  const { users, groups } = store
  const logs = seedLogs(users)
  const device = { mode: 'loose' as 'loose' | 'strict', grace: 1 }

  const slug = (name: string) => {
    const s = name
      .toLowerCase()
      .replace(/[ _-]/g, '-')
      .replace(/[^a-z0-9-]/g, '')
      .replace(/^-+|-+$/g, '')
    return s || `group-${randomUUID().slice(0, 8)}`
  }

  return {
    // ---- 用户组：iam.user.write，无 reauth、无幂等 -----------------------------
    'POST /v1/user-groups': async (ctx) => {
      if (!ctx.requirePermission('iam.user.write')) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const extra = unknownField(body, ['name', 'code', 'description'])
      if (extra) return ctx.fail(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
      const name = typeof body.name === 'string' ? body.name.trim() : ''
      if (!name) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { name: '分组名必填' })
      const code = (typeof body.code === 'string' ? body.code.trim() : '') || slug(name)
      if (groups.some((g) => g.code === code)) return ctx.fail(409, 'conflict', '这个分组标识已存在')
      const id = randomUUID()
      groups.push({ id, code, name, description: typeof body.description === 'string' ? body.description : '', plans: 0, prices: 0, coupons: 0 })
      ctx.send(200, { id })
    },

    // 编辑：code 被忽略，不允许改
    'POST /v1/user-groups/:id': async (ctx) => {
      if (!ctx.requirePermission('iam.user.write')) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const extra = unknownField(body, ['name', 'code', 'description'])
      if (extra) return ctx.fail(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
      const name = typeof body.name === 'string' ? body.name.trim() : ''
      if (!name) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { name: '分组名必填' })
      const g = groups.find((x) => x.id === ctx.params.id)
      if (!g) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      g.name = name
      g.description = typeof body.description === 'string' ? body.description : ''
      ctx.send(200, { id: g.id })
    },

    // 删除：成员、套餐可见性、专属价格、优惠券限定任一不为 0 都回 409，message 写明是哪一种
    'DELETE /v1/user-groups/:id': (ctx) => {
      if (!ctx.requirePermission('iam.user.write')) return
      const k = groups.findIndex((x) => x.id === ctx.params.id)
      if (k < 0) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      const g = groups[k]!
      if (users.some((u) => u.group_id === g.id)) return ctx.fail(409, 'conflict', '这个分组下还有用户，先把他们移出去')
      if (g.plans > 0) return ctx.fail(409, 'conflict', '还有套餐按这个分组控制可见性，先解除')
      if (g.prices > 0) return ctx.fail(409, 'conflict', '还有分组专属价格挂在这里，先删掉那些价格')
      if (g.coupons > 0) return ctx.fail(409, 'conflict', '还有优惠券限定了这个分组，先解除')
      groups.splice(k, 1)
      ctx.send(200, { ok: true })
    },

    // ---- 批量运营 -----------------------------------------------------------
    // 预览：只要读权限，鼓励多用
    'POST /v1/users/bulk/preview': async (ctx) => {
      if (!ctx.requirePermission('iam.user.read')) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const extra = unknownField(body, FILTER_KEYS)
      if (extra) return ctx.fail(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
      const f = filterFromBody(body)
      const bad = filterProblem(f)
      if (bad) return ctx.send(bad.status, bad.body)
      const hits = users.filter(matcher(store, f)).sort(byNewest)
      const sample = hits.slice(0, 10)
      ctx.send(200, {
        total: hits.length,
        samples: sample.map((u) => u.email),
        sample_rows: sample.map((u) => {
          const cs = store.current(u)
          return { email: u.email, plan_name: cs?.plan_name ?? null, current_period_end: cs?.current_period_end ?? null }
        }),
      })
    },

    // 导出：iam.user.write + reauth（R9），无幂等；limit 默认 10000、最大 50000，超出静默截断
    'GET /v1/users/bulk/export': (ctx) => {
      if (!ctx.requirePermission('iam.user.write') || !ctx.requireReauth()) return
      const f = filterFromQuery(ctx.query)
      const bad = filterProblem(f)
      if (bad) return ctx.send(bad.status, bad.body)
      let limit = Number.parseInt(ctx.query.get('limit') ?? '', 10)
      if (!(limit > 0 && limit <= 50000)) limit = 10000
      const groupName = (id: string | null) => groups.find((g) => g.id === id)?.name ?? ''
      const lines = users
        .filter(matcher(store, f))
        .sort(byNewest)
        .slice(0, limit)
        .map((u) =>
          csvLine([
            u.email,
            u.status,
            groupName(u.group_id),
            String(u.subs.filter((s) => s.status === 'active').length),
            String(u.subs.length),
            (u.subs.reduce((sum, s) => sum + s.amount, 0) / 100).toFixed(2),
            goTime(u.created_at),
            u.last_login_at ? goTime(u.last_login_at) : '',
          ]),
        )
      const text = '\uFEFF' + [csvLine(['邮箱', '状态', '分组', '生效订阅', '订单数', '累计实付', '注册时间', '最近登录']), ...lines].join('\n') + '\n'
      ctx.sendRaw(200, { contentType: 'text/csv; charset=utf-8', text, headers: { 'Content-Disposition': 'attachment; filename="users.csv"' } })
    },

    // 批量生成：iam.user.write + reauth（R9）+ 幂等 user_bulk_generate；口令明文只回这一次
    'POST /v1/users/bulk/generate': async (ctx) => {
      if (!ctx.requirePermission('iam.user.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      await ctx.idempotent('user_bulk_generate', () => {
        if (!body) return err(400, 'bad_request', '请求体不是合法的 JSON')
        const extra = unknownField(body, ['count', 'email_prefix', 'email_domain', 'group_id', 'reason'])
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        const count = typeof body.count === 'number' ? body.count : 0
        const prefix = typeof body.email_prefix === 'string' ? body.email_prefix.trim().toLowerCase() : ''
        const domain = typeof body.email_domain === 'string' ? body.email_domain.trim().toLowerCase() : ''
        const reason = typeof body.reason === 'string' ? body.reason.trim() : ''
        const groupId = typeof body.group_id === 'string' ? body.group_id : ''
        const invalid = (fields: Record<string, string>) => err(422, 'validation_failed', '请求参数校验未通过', fields)
        if (!Number.isInteger(count) || count < 1 || count > 500) return invalid({ count: '一次生成 1 到 500 个。更多请分批 —— 单次几千个会把事务拖很久' })
        if (!/^[a-z0-9-]{1,20}$/.test(prefix)) return invalid({ email_prefix: '前缀只能用小写字母、数字和短横线，1 到 20 位' })
        if (domain.length < 4 || domain.length > 63 || !domain.includes('.') || !/^[a-z0-9.-]+$/.test(domain)) return invalid({ email_domain: '域名格式不正确' })
        if (runes(reason) < 5 || runes(reason) > 500) return invalid({ reason: '请写清生成原因，5 到 500 个字' })
        if (groupId && groupId.length !== 36) return invalid({ group_id: '分组标识格式不正确' })
        if (groupId && !groups.some((g) => g.id === groupId)) return invalid({ group_id: '分组不存在' })
        const alphabet = 'abcdefghijkmnpqrstuvwxyz23456789'
        const pick = (n: number, from: string) => Array.from({ length: n }, () => from[randomInt(from.length)]).join('')
        const made: Array<{ email: string; password: string }> = []
        while (made.length < count) {
          const email = `${prefix}-${pick(8, alphabet)}@${domain}`
          if (users.some((u) => u.email === email)) continue
          const password = pick(16, 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789!@#%')
          users.push({
            id: randomUUID(),
            email,
            display_name: null,
            status: 'active',
            risk_level: 'normal',
            group_id: groupId || null,
            created_at: new Date().toISOString(),
            last_login_at: null,
            email_verified: true,
            balance: 0,
            currency: 'CNY',
            subs: [],
            referrer: null,
            telegram: null,
            roles: [],
          })
          made.push({ email, password })
        }
        return { status: 200, body: { count: made.length, users: made, warning: '口令只在这一次返回，关闭后无法再查。请立即保存。' } }
      })
    },

    // 群发：ops.notification.write + reauth（R9）+ 幂等 user_bulk_mail；筛选字段在请求体顶层
    'POST /v1/users/bulk/mail': async (ctx) => {
      if (!ctx.requirePermission('ops.notification.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      await ctx.idempotent('user_bulk_mail', () => {
        if (!body) return err(400, 'bad_request', '请求体不是合法的 JSON')
        const extra = unknownField(body, [...FILTER_KEYS, 'subject', 'body'])
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        const f = filterFromBody(body)
        const bad = filterProblem(f)
        if (bad) return bad
        const subject = typeof body.subject === 'string' ? body.subject.trim() : ''
        const text = typeof body.body === 'string' ? body.body.trim() : ''
        if (runes(subject) < 1 || runes(subject) > 200) return err(422, 'validation_failed', '请求参数校验未通过', { subject: '主题必填，不超过 200 字' })
        if (runes(text) < 1 || runes(text) > 20000) return err(422, 'validation_failed', '请求参数校验未通过', { body: '正文必填，不超过 20000 字' })
        const hits = users.filter(matcher(store, f))
        if (hits.length === 0) return err(422, 'validation_failed', '这个筛选条件下没有用户，先用预览确认一下')
        if (hits.length > 20000) return err(422, 'validation_failed', '一次最多群发 20000 人，请缩小范围分批发送')
        // 关掉营销邮件偏好的人被跳过：种子里每 9 个有一个
        const skipped = hits.filter((u) => users.indexOf(u) % 9 === 4).length
        return { status: 200, body: { queued: hits.length - skipped, skipped } }
      })
    },

    // ---- 设备策略 -----------------------------------------------------------
    'GET /v1/devices': (ctx) => {
      if (!ctx.requirePermission('iam.user.read')) return
      const rows = users
        .flatMap((u) => u.subs.filter((s) => s.status === 'active' || s.status === 'trialing' || s.status === 'grace').map((s) => ({ u, s })))
        .sort((a, b) => b.s.online_devices - a.s.online_devices || b.s.created_at.localeCompare(a.s.created_at))
        .slice(0, 200)
        .map(({ u, s }, k) => {
          const limit = store.effectiveLimit(s)
          return {
            subscription_id: s.id,
            email: u.email,
            plan: s.plan_name,
            limit,
            online: s.online_devices,
            nodes: s.online_devices ? 1 + (k % Math.min(3, s.online_devices)) : 0,
            overridden: s.device_limit_override !== null,
            exceeded: limit > 0 && s.online_devices > limit + device.grace,
            last_seen_at: s.online_devices ? new Date(Date.now() - ((k * 37) % 290) * 1000).toISOString() : null,
          }
        })
      ctx.send(200, { devices: rows, mode: device.mode, grace: device.grace })
    },

    // 全局模式：iam.user.write + reauth（R9），无幂等；grace 只在传了时写
    'POST /v1/settings/device-limit': async (ctx) => {
      if (!ctx.requirePermission('iam.user.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const extra = unknownField(body, ['mode', 'grace'])
      if (extra) return ctx.fail(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
      if (body.mode !== 'loose' && body.mode !== 'strict') return ctx.fail(422, 'validation_failed', '模式只能是 loose 或 strict')
      const grace = body.grace
      if (grace !== undefined && grace !== null && !(typeof grace === 'number' && Number.isInteger(grace) && grace >= 0 && grace <= 5)) return ctx.fail(422, 'validation_failed', '宽容值需在 0 到 5 之间')
      device.mode = body.mode
      if (typeof grace === 'number') device.grace = grace
      ctx.send(200, { ok: true })
    },

    // ---- 流量重置 -----------------------------------------------------------
    'GET /v1/traffic-resets': (ctx) => {
      if (!ctx.requirePermission('metering.reset.read')) return
      const reason = ctx.query.get('reason') ?? ''
      const userId = ctx.query.get('user_id') ?? ''
      if (reason && !REASONS.includes(reason as Reason)) return ctx.fail(400, 'bad_request', '不支持的重置原因')
      if (userId && !UUID.test(userId)) return ctx.fail(400, 'bad_request', '用户标识格式不正确')
      let limit = Number.parseInt(ctx.query.get('limit') ?? '', 10)
      if (!(limit > 0 && limit <= 200)) limit = 50
      const offset = Math.max(0, Number.parseInt(ctx.query.get('offset') ?? '', 10) || 0)
      const hits = logs.filter((l) => (!reason || l.reason === reason) && (!userId || l.user_id === userId))
      ctx.send(200, { logs: hits.slice(offset, offset + limit).map(logView), total: hits.length })
    },

    'GET /v1/traffic-resets/stats': (ctx) => {
      if (!ctx.requirePermission('metering.reset.read')) return
      const since = Date.now() - 30 * DAY
      const recent = logs.filter((l) => Date.parse(l.created_at) > since)
      const byReason: Partial<Record<Reason, number>> = {}
      for (const l of recent) byReason[l.reason] = (byReason[l.reason] ?? 0) + 1
      ctx.send(200, {
        last_30_days: recent.length,
        by_reason: byReason,
        freed_bytes: recent.reduce((sum, l) => sum + l.consumed_before, 0),
        manual_count: byReason.manual ?? 0,
      })
    },

    // 单用户历史：固定最近 50 条，不接受分页
    'GET /v1/users/:id/traffic-resets': (ctx) => {
      if (!ctx.requirePermission('metering.reset.read')) return
      if (!UUID.test(ctx.params.id!)) return ctx.fail(400, 'bad_request', '用户标识格式不正确')
      const hits = logs.filter((l) => l.user_id === ctx.params.id)
      ctx.send(200, { logs: hits.slice(0, 50).map(logView), total: hits.length })
    },

    // 手动重置：metering.reset.write + reauth + 幂等 traffic_manual_reset；只清 status=active、到期最晚那条订阅的本期已用
    'POST /v1/users/:id/traffic-reset': async (ctx) => {
      if (!ctx.requirePermission('metering.reset.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      await ctx.idempotent('traffic_manual_reset', () => {
        if (!body) return err(400, 'bad_request', '请求体不是合法的 JSON')
        const extra = unknownField(body, ['note'])
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        if (!UUID.test(ctx.params.id!)) return err(404, 'not_found', '资源不存在或无权访问')
        const note = typeof body.note === 'string' ? body.note.trim() : ''
        if (runes(note) < 5 || runes(note) > 500) return err(422, 'validation_failed', '请求参数校验未通过', { note: '请写清重置原因，5 到 500 个字' })
        // 用户不存在时后端也是「没有生效中的订阅」：查的是订阅表
        const u = users.find((x) => x.id === ctx.params.id)
        const end = (s: Sub) => (s.current_period_end ? Date.parse(s.current_period_end) : -Infinity)
        const sub = u?.subs.filter((s) => s.status === 'active').sort((a, b) => end(b) - end(a))[0]
        if (!u || !sub) return err(422, 'validation_failed', '这个用户没有生效中的订阅')
        const freed = sub.traffic_used
        sub.traffic_used = 0
        logs.unshift({
          id: randomUUID(),
          user_id: u.id,
          user_email: u.email,
          plan_name: sub.plan_name,
          reason: 'manual',
          consumed_before: freed,
          actor_email: ctx.user.email,
          note,
          created_at: new Date().toISOString(),
        })
        return { status: 200, body: { reset: true, freed_bytes: freed } }
      })
    },
  } satisfies Record<string, (ctx: MockContext) => void | Promise<void>>
}
