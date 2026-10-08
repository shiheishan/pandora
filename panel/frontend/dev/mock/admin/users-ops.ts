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
  /** 把这个组列入「仅限用户组」名单的节点池（R104，来自节点假后端） */
  exclusivePools(groupId: string): Array<{ id: string; name: string }>
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
// 批量生成任务（adminops.UserGenerationJob）：进度按时间往前走，结果 24 小时后清除
interface GenerationJob {
  id: string
  actor_id: string
  total: number
  email_prefix: string
  email_domain: string
  group_id: string | null
  reason: string
  created: number
  users: Array<{ email: string; password: string }>
}
const JOB_STEP_MS = 60
const RESULT_TTL_MS = DAY

function jobView(j: GenerationJob) {
  const elapsed = Date.now() - j.created
  const completed = Math.min(j.total, Math.floor(elapsed / JOB_STEP_MS))
  const done = completed === j.total
  const finishedAt = done ? j.created + j.total * JOB_STEP_MS : null
  const expires = finishedAt === null ? null : finishedAt + RESULT_TTL_MS
  return {
    id: j.id,
    actor_id: j.actor_id,
    status: done ? 'succeeded' : completed > 0 ? 'running' : 'queued',
    total: j.total,
    completed,
    failed: 0,
    email_prefix: j.email_prefix,
    email_domain: j.email_domain,
    group_id: j.group_id,
    reason: j.reason,
    error: null,
    result_available: completed > 0 && (expires === null || Date.now() < expires),
    result_expires_at: expires === null ? null : new Date(expires).toISOString(),
    created_at: new Date(j.created).toISOString(),
    started_at: completed > 0 ? new Date(j.created).toISOString() : null,
    finished_at: finishedAt === null ? null : new Date(finishedAt).toISOString(),
  }
}

export function opsRoutes(store: UsersStore): Record<string, MockRoute> {
  const { users, groups } = store
  const generationJobs: GenerationJob[] = []
  const logs = seedLogs(users)
  // window_minutes：R103 设备识别窗口（5 / 10 / 30 / 60，缺省 5）
  const device = { mode: 'loose' as 'loose' | 'strict', grace: 1, window_minutes: 5 }

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
      // R104：先看节点池的限定名单（否则名单变空会让池悄悄对所有人开放），再看成员与各类引用
      const pool = store.exclusivePools(g.id)[0]
      if (pool) return ctx.fail(409, 'conflict', `节点池「${pool.name}」限定了这个分组，先在节点池里把它移出名单再删`)
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

    // 批量生成：iam.user.write + reauth（R9）+ 幂等 user_bulk_generate。是后台任务（adminops
    // SubmitGenerateUsers）：回 202 与任务；账号在这里一次建好，进度按时间往前走（每 60ms 一个）
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
        const groupId = typeof body.group_id === 'string' ? body.group_id.trim() : ''
        const invalid = (fields: Record<string, string>) => err(422, 'validation_failed', '请求参数校验未通过', fields)
        if (!Number.isInteger(count) || count < 1 || count > 500) return invalid({ count: '一次生成 1 到 500 个。更多请分批提交' })
        if (!/^[a-z0-9-]{1,20}$/.test(prefix)) return invalid({ email_prefix: '前缀只能用小写字母、数字和短横线，1 到 20 位' })
        if (domain.length < 4 || domain.length > 63 || !domain.includes('.') || !/^[a-z0-9.-]+$/.test(domain)) return invalid({ email_domain: '域名格式不正确' })
        if (runes(reason) < 5 || runes(reason) > 500) return invalid({ reason: '请写清生成原因，5 到 500 个字' })
        if (groupId && !UUID.test(groupId)) return invalid({ group_id: '分组标识格式不正确' })
        if (groupId && !groups.some((g) => g.id === groupId)) return invalid({ group_id: '分组不存在' })
        const alphabet = 'abcdefghijkmnpqrstuvwxyz23456789'
        const pick = (n: number, from: string) => Array.from({ length: n }, () => from[randomInt(from.length)]).join('')
        const made: Array<{ email: string; password: string }> = []
        while (made.length < count) {
          const email = `${prefix}-${pick(8, alphabet)}@${domain}`
          if (users.some((u) => u.email === email)) continue
          const password = pick(16, 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789@#%+=')
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
            unattached_bytes: 0,
            referrer: null,
            telegram: null,
            roles: [],
          })
          made.push({ email, password })
        }
        const job: GenerationJob = {
          id: randomUUID(),
          actor_id: ctx.user.userId,
          total: count,
          email_prefix: prefix,
          email_domain: domain,
          group_id: groupId || null,
          reason,
          created: Date.now(),
          users: made,
        }
        generationJobs.unshift(job)
        return { status: 202, body: jobView(job) }
      })
    },
    'GET /v1/users/bulk/generate/jobs': (ctx) => {
      if (!ctx.requirePermission('iam.user.write')) return
      ctx.send(200, { jobs: generationJobs.slice(0, 20).map(jobView) })
    },
    'GET /v1/users/bulk/generate/jobs/:id/result': (ctx) => {
      if (!ctx.requirePermission('iam.user.write') || !ctx.requireReauth()) return
      const job = generationJobs.find((j) => j.id === ctx.params.id)
      // 别人的任务与不存在的任务同一个 404（只有提交人能下载）
      if (!job || job.actor_id !== ctx.user.userId) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      const view = jobView(job)
      if (!view.result_available) return ctx.fail(409, 'conflict', '结果不可下载：任务还没生成出账号，或结果已超过 24 小时被清除')
      const lines = job.users.slice(0, view.completed).map((u) => csvLine([u.email, u.password]))
      const text = '\uFEFF' + [csvLine(['邮箱', '初始密码']), ...lines].join('\n') + '\n'
      ctx.sendRaw(200, { contentType: 'text/csv; charset=utf-8', text, headers: { 'Content-Disposition': 'attachment; filename="generated-users.csv"' } })
    },
    'GET /v1/users/bulk/generate/jobs/:id': (ctx) => {
      if (!ctx.requirePermission('iam.user.write')) return
      const job = generationJobs.find((j) => j.id === ctx.params.id)
      if (!job) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      ctx.send(200, jobView(job))
    },
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
      ctx.send(200, { devices: rows, mode: device.mode, grace: device.grace, window_minutes: device.window_minutes })
    },

    // 全局模式：iam.user.write + reauth（R9），无幂等；grace 只在传了时写
    'POST /v1/settings/device-limit': async (ctx) => {
      if (!ctx.requirePermission('iam.user.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const extra = unknownField(body, ['mode', 'grace', 'window_minutes'])
      if (extra) return ctx.fail(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
      if (body.mode !== 'loose' && body.mode !== 'strict') return ctx.fail(422, 'validation_failed', '模式只能是 loose 或 strict')
      const grace = body.grace
      if (grace !== undefined && grace !== null && !(typeof grace === 'number' && Number.isInteger(grace) && grace >= 0 && grace <= 5)) return ctx.fail(422, 'validation_failed', '宽容值需在 0 到 5 之间')
      // R103：省略 = 不改，其它值 422
      const win = body.window_minutes
      if (win !== undefined && win !== null && ![5, 10, 30, 60].includes(win as number)) return ctx.fail(422, 'validation_failed', '设备识别窗口只能是 5、10、30 或 60 分钟')
      device.mode = body.mode
      if (typeof grace === 'number') device.grace = grace
      if (typeof win === 'number') device.window_minutes = win
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

    // 手动重置（按份）：metering.reset.write + reauth + 幂等 traffic_manual_reset；
    // path 是订阅 id，只清这一份的本期已用，且这一份必须 status=active（试用不行）
    'POST /v1/subscriptions/:id/traffic-reset': async (ctx) => {
      if (!ctx.requirePermission('metering.reset.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      await ctx.idempotent('traffic_manual_reset', () => {
        if (!body) return err(400, 'bad_request', '请求体不是合法的 JSON')
        const extra = unknownField(body, ['note'])
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        if (!UUID.test(ctx.params.id!)) return err(404, 'not_found', '资源不存在或无权访问')
        const note = typeof body.note === 'string' ? body.note.trim() : ''
        if (runes(note) < 5 || runes(note) > 500) return err(422, 'validation_failed', '请求参数校验未通过', { note: '请写清重置原因，5 到 500 个字' })
        const u = users.find((x) => x.subs.some((s) => s.id === ctx.params.id))
        const sub = u?.subs.find((s) => s.id === ctx.params.id)
        if (!u || !sub) return err(404, 'not_found', '资源不存在或无权访问')
        if (sub.status !== 'active') return err(422, 'validation_failed', '这份订阅没有在生效中，不能重置流量')
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

    // 加时长：billing.adjustment.write + reauth + 幂等 subscription_admin_extend（billing.ExtendSubscriptionAsAdmin）。
    // 先解码（多余字段 400），再校验天数与原因（422 带 fields），再找订阅（404）、判状态（409）与到期时间（422 无 fields）
    'POST /v1/subscriptions/:id/extend': async (ctx) => {
      if (!ctx.requirePermission('billing.adjustment.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      await ctx.idempotent('subscription_admin_extend', () => {
        if (!body) return err(400, 'bad_request', '请求体不是合法的 JSON')
        const extra = unknownField(body, ['days', 'reason'])
        if (extra) return err(400, 'bad_request', '请求体不是合法的 JSON')
        const days = typeof body.days === 'number' && Number.isInteger(body.days) ? body.days : 0
        const reason = typeof body.reason === 'string' ? body.reason.trim() : ''
        const fields: Record<string, string> = {}
        if (days < 1 || days > 3650) fields.days = '天数是 1 到 3650 之间的整数'
        if (runes(reason) < 5 || runes(reason) > 500) fields.reason = '请写清加时长的原因，5 到 500 个字。这条会进审计'
        if (Object.keys(fields).length) return err(422, 'validation_failed', '请求参数校验未通过', fields)
        const owner = UUID.test(ctx.params.id!) ? users.find((u) => u.subs.some((s) => s.id === ctx.params.id)) : undefined
        const sub = owner?.subs.find((s) => s.id === ctx.params.id)
        if (!owner || !sub) return err(404, 'not_found', '资源不存在或无权访问')
        if (sub.status !== 'active') return err(409, 'conflict', '只有生效中的订阅可以延长时长')
        if (!sub.current_period_end) return err(422, 'validation_failed', '这条订阅没有到期时间，不需要延长')
        const previous = sub.current_period_end
        const end = new Date(Math.max(Date.parse(previous), Date.now()) + days * DAY).toISOString()
        sub.current_period_end = end
        return { status: 200, body: { subscription_id: sub.id, user_email: owner.email, days, previous_end: previous, period_end: end } }
      })
    },
    // 加流量包：billing.adjustment.write + reauth + 幂等 subscription_admin_traffic_grant；
    // 流量包挂在这一份订阅上（billing.GrantTrafficPackAsAdmin），回执里的剩余量是这一份的余量
    'POST /v1/subscriptions/:id/traffic-pack': async (ctx) => {
      if (!ctx.requirePermission('billing.adjustment.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      await ctx.idempotent('subscription_admin_traffic_grant', () => {
        if (!body) return err(400, 'bad_request', '请求体不是合法的 JSON')
        const extra = unknownField(body, ['bytes', 'reason'])
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        const bytes = typeof body.bytes === 'number' && Number.isInteger(body.bytes) ? body.bytes : 0
        const reason = typeof body.reason === 'string' ? body.reason.trim() : ''
        const fields: Record<string, string> = {}
        if (bytes < 1 || bytes > 10 * 1024 * GiB) fields.bytes = '流量要大于 0，一次最多 10240 GB'
        if (runes(reason) < 5 || runes(reason) > 500) fields.reason = '请写清加流量的原因，5 到 500 个字。这条会进审计'
        if (Object.keys(fields).length) return err(422, 'validation_failed', '请求参数校验未通过', fields)
        const owner = UUID.test(ctx.params.id!) ? users.find((u) => u.subs.some((s) => s.id === ctx.params.id)) : undefined
        const sub = owner?.subs.find((s) => s.id === ctx.params.id)
        if (!owner || !sub) return err(404, 'not_found', '资源不存在或无权访问')
        sub.pack_bytes += bytes
        return {
          status: 200,
          body: { subscription_id: sub.id, user_id: owner.id, user_email: owner.email, grant_id: randomUUID(), granted_bytes: bytes, remaining_bytes_total: sub.pack_bytes },
        }
      })
    },
  } satisfies Record<string, (ctx: MockContext) => void | Promise<void>>
}
