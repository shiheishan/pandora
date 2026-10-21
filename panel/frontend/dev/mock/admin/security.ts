import { createHash, randomUUID } from 'node:crypto'
import type { Json, MockContext, MockModule, MockResult } from '../types.ts'
import { plans } from './plans-store.ts'
import { traceRoutes } from './security-trace.ts'
import { setProfileSource, userStore, type User } from './users.ts'

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------
const MIN = 60_000
const DAY = 86_400_000
const err = (status: number, code: string, message: string, fields?: Record<string, string>): MockResult => ({ status, body: { error: { code, message, ...(fields ? { fields } : {}) } } })
const invalid = (fields: Record<string, string>) => err(422, 'validation_failed', '请求参数校验未通过', fields)
const NOT_FOUND = err(404, 'not_found', '资源不存在或无权访问')
const reply = (ctx: MockContext, r: MockResult) => ctx.send(r.status, r.body)
const iso = (msAgo: number) => new Date(Date.now() - msAgo).toISOString()
const runes = (s: string) => [...s].length
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

type Shape = Record<string, 'string' | 'boolean' | 'strings'>
/** httpx.DecodeJSON：非法 JSON、未知字段、类型不符都回 400 */
async function decode(ctx: MockContext, shape: Shape): Promise<{ ok: true; body: Json } | { ok: false; result: MockResult }> {
  const body = await ctx.body()
  if (!body) return { ok: false, result: err(400, 'bad_request', '请求体不是合法的 JSON') }
  for (const [k, v] of Object.entries(body)) {
    const want = shape[k]
    if (!want) return { ok: false, result: err(400, 'bad_request', `请求体包含未知字段 "${k}"`) }
    const ok = want === 'strings' ? v === null || (Array.isArray(v) && v.every((x) => typeof x === 'string')) : v === null || typeof v === want
    if (!ok) return { ok: false, result: err(400, 'bad_request', `字段 "${k}" 类型不正确`) }
  }
  return { ok: true, body }
}

/** 归属地：Go 在读时用 GeoIP 现算，这里按前缀给固定结果（与 geoip.NetworkKind 同值） */
const GEO: ReadonlyArray<readonly [string, string, string]> = [
  ['192.0.2.', '中国 浙江 电信', 'residential'],
  ['10.', '内网地址', 'private'],
  ['192.168.', '内网地址', 'private'],
  ['127.', '本机回环地址', 'loopback'],
  ['198.51.100.1', '中国 广东 移动', 'mobile'],
  ['198.51.100.2', '中国 江苏 移动', 'mobile'],
  ['198.51.100.3', '中国 北京 移动', 'mobile'],
  ['198.51.100.4', '中国 上海 联通', 'residential'],
  ['198.51.100.5', '中国 台湾 中华电信', 'residential'],
  ['198.51.100.6', '中国 上海 教育网', 'education'],
  ['203.0.113.4', '日本 东京 Vultr', 'datacenter'],
  ['203.0.113.1', '美国 洛杉矶 Vultr', 'datacenter'],
]
function geoOf(ip: string): { geo: string; kind: string } {
  const hit = ip ? GEO.find(([p]) => ip.startsWith(p)) : undefined
  return hit ? { geo: hit[1], kind: hit[2] } : { geo: '', kind: '' }
}

/** 每个用户固定的家里宽带与手机出口：各人不同，所以不会凭空形成共享 IP 聚类 */
const homeIp = (i: number) => `192.0.2.${10 + (i % 48)}`
const mobileIp = (i: number) => `198.51.100.${200 + (i % 48)}`
/** 00080 之前的存量审计没有明文来源 IP 与 auth_context（ip_hash 仍在）；种子里早于 30 天的行按存量写 */
const LEGACY_MS = 30 * DAY
const MOBILE_UA = 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148'
const ATTACK_UA = 'python-requests/2.31'
/** 当前登录的这位管理员（ctx.user）写审计时用的来源 IP */
const ADMIN_IP = '10.8.0.9'
const indexOf = (u: User) => userStore.indexOf(u)

const userAt = (i: number): User => userStore[i % userStore.length]!
const findUser = (id: string) => userStore.find((u) => u.id === id)
const activePlan = (u: User) =>
  [...u.subs]
    .filter((s) => s.status === 'active')
    .sort((a, b) => b.created_at.localeCompare(a.created_at))[0]?.plan_name ?? null

// ---------------------------------------------------------------------------
// 审计日志（audit/handler auditEventResponse；按时间倒序存）
// ---------------------------------------------------------------------------
interface AuditRow {
  id: string
  occurred_at: string
  actor_kind: string
  actor_id: string | null
  actor_email: string | null
  action: string
  resource_type: string | null
  resource_id: string | null
  resource_label: string | null
  api_domain: string | null
  outcome: string
  reason: string | null
  auth_context: 'session' | 'reauth' | null
  source_ip: string | null
  user_agent: string | null
  /** 同一来源 IP 的稳定键（Go 是 HMAC，这里直接用 IP 串）；存量行 source_ip 为 null 但 ip_hash 仍在；不出现在任何响应里 */
  ip_hash: string | null
}
const audit: AuditRow[] = []

/** 写过审计的其它操作者（当前登录的管理员等）：id → 邮箱 */
const seenActors = new Map<string, string>()
function emailOf(id: string | null): string | null {
  if (!id) return null
  return userStore.find((u) => u.id === id)?.email ?? STAFF.find((s) => s.id === id)?.email ?? seenActors.get(id) ?? null
}
/** IDsByEmailLike：邮箱包含 q（不分大小写）的账号 id */
function idsByEmail(q: string): Set<string> {
  const needle = q.toLowerCase()
  const out = new Set<string>()
  for (const u of userStore) if (u.email.toLowerCase().includes(needle)) out.add(u.id)
  for (const s of STAFF) if (s.email.toLowerCase().includes(needle)) out.add(s.id)
  for (const [id, email] of seenActors) if (email.toLowerCase().includes(needle)) out.add(id)
  return out
}
/** 对象可读名：与 Go 登记的解析器一致——用户（邮箱）、工单（工单号）、套餐（套餐名）、套餐版本（套餐名 · v版本号），其余为 null */
function labelOf(r: AuditRow): string | null {
  if (r.resource_type === 'user') return emailOf(r.resource_id)
  if (r.resource_type === 'ticket') return r.resource_label
  if (r.resource_type === 'plan') return plans.find((p) => p.id === r.resource_id)?.name ?? null
  if (r.resource_type === 'plan_version') {
    for (const p of plans) {
      const v = p.versions.find((x) => x.id === r.resource_id)
      if (v) return `${p.name} · v${v.version}`
    }
    return null
  }
  return null
}

type AuditSeedInput = Omit<AuditRow, 'id' | 'occurred_at' | 'ip_hash'> & { at?: string; ip_hash?: string | null }

function record(row: AuditSeedInput): AuditRow {
  const { at, ip_hash, actor_id, actor_email, ...rest } = row
  const hash = ip_hash !== undefined ? ip_hash : rest.source_ip
  const r: AuditRow = {
    id: randomUUID(),
    occurred_at: at ?? new Date().toISOString(),
    actor_id,
    actor_email,
    ip_hash: hash,
    ...rest,
  }
  if (actor_id && actor_email) seenActors.set(actor_id, actor_email)
  audit.unshift(r)
  audit.sort((a, b) => b.occurred_at.localeCompare(a.occurred_at))
  return r
}

const seedRows: AuditRow[] = []
function seed(row: AuditSeedInput): void {
  const { at, ip_hash, actor_id, actor_email, ...rest } = row
  const occurred_at = at ?? new Date().toISOString()
  const hash = ip_hash !== undefined ? ip_hash : rest.source_ip
  const legacy = Date.now() - Date.parse(occurred_at) > LEGACY_MS
  const r: AuditRow = {
    id: randomUUID(),
    occurred_at,
    actor_id,
    actor_email,
    ip_hash: hash,
    ...rest,
    source_ip: legacy ? null : rest.source_ip,
    auth_context: legacy ? null : rest.auth_context,
  }
  if (actor_id && actor_email) seenActors.set(actor_id, actor_email)
  seedRows.push(r)
}

interface Staff {
  id: string
  email: string
  ip: string
}
const STAFF: readonly Staff[] = [
  { id: '5a1f0000-0000-4000-8000-000000000001', email: 'linzhou@example.test', ip: '10.8.0.12' },
  { id: '5a1f0000-0000-4000-8000-000000000002', email: 'zhou.min@example.test', ip: '10.8.0.31' },
  { id: '5a1f0000-0000-4000-8000-000000000003', email: 'tang.yiming@example.test', ip: '10.8.0.44' },
  { id: '5a1f0000-0000-4000-8000-000000000004', email: 'ops@example.test', ip: '10.8.0.20' },
  { id: '5a1f0000-0000-4000-8000-000000000005', email: 'support@example.test', ip: '10.8.0.52' },
]
const staffByEmail = (email: string | null) => STAFF.find((s) => s.email === email) ?? STAFF[3]!
const BROWSER = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 14_6) AppleWebKit/605.1.15 Safari/605.1.15'
const ua = (i: number) => (i % 3 === 0 ? MOBILE_UA : BROWSER)

/** 别的模块（套餐等）的后台写操作经这里记审计：操作者是当前登录的管理员，来源 IP 与 UA 同本文件的后台写；
 *  审计只归本文件所有，调用方单向 import（本文件不 import 调用方，不成环） */
export function recordAdminAudit(
  ctx: MockContext,
  ev: { action: string; resource_type: string | null; resource_id: string | null; reason?: string | null; outcome?: string; auth_context?: 'session' | 'reauth' },
): void {
  record({
    actor_kind: 'admin',
    actor_id: ctx.user.userId,
    actor_email: ctx.user.email,
    action: ev.action,
    resource_type: ev.resource_type,
    resource_id: ev.resource_id,
    resource_label: null,
    api_domain: 'admin',
    outcome: ev.outcome ?? 'success',
    reason: ev.reason ?? null,
    auth_context: ev.auth_context ?? (ctx.reauthed ? 'reauth' : 'session'),
    source_ip: ADMIN_IP,
    user_agent: BROWSER,
  })
}

const sessionId = () => randomUUID()
/** 工单号照迁移 00027 的 CHECK：TK + 8 位日期 + '-' + 8 位 [A-Z2-7] */
const B32 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
function ticketNo(k: number, at: number): string {
  let n = k + 1000
  let s = ''
  for (let i = 0; i < 8; i++) {
    s = B32[n % 32] + s
    n = Math.floor(n / 32)
  }
  return `TK${new Date(at).toISOString().slice(0, 10).replace(/-/g, '')}-${s}`
}

/** 共享 IP 场景：成员挑的是真实种子——0 号是管理员（停用时跳过），9 号已停用，24 / 39 已停用、40 已封禁
 *  （停用前登录过）；pending 账号从没登录成功过，不当成员。198.51.100.71 这组全是存量行（没有明文 IP），
 *  聚类里 IP 显示为「无法解密」。风控聚类与用户画像都由这些审计行现算 */
const CLUSTER_SCENES: ReadonlyArray<{ ip: string; members: number[]; lastMin: number; legacy?: boolean }> = [
  { ip: '198.51.100.18', members: [3, 9, 13, 18, 23], lastMin: 6 },
  { ip: '203.0.113.45', members: [5, 25, 45], lastMin: 20 },
  { ip: '198.51.100.24', members: [0, 7, 27], lastMin: 45 },
  { ip: '198.51.100.42', members: [1, 21], lastMin: 3 * 24 * 60 },
  { ip: '198.51.100.51', members: [15, 35], lastMin: 180 },
  { ip: '198.51.100.61', members: [12, 31], lastMin: 300 },
  { ip: '198.51.100.31', members: [24, 39, 40], lastMin: 900 },
  { ip: '198.51.100.71', members: [19, 29], lastMin: 40 * 24 * 60, legacy: true },
]

;(function seedAudit() {
  const base = { resource_type: null as string | null, resource_id: null as string | null, resource_label: null as string | null, outcome: 'success', reason: null as string | null }
  const anonFail = (over: Pick<AuditSeedInput, 'action' | 'at' | 'resource_type' | 'resource_id' | 'reason' | 'source_ip' | 'user_agent'>) =>
    seed({
      ...base,
      actor_kind: 'anonymous',
      actor_id: null,
      actor_email: null,
      api_domain: 'public',
      auth_context: null,
      outcome: 'failure',
      ...over,
    })

  // A. 每个用户
  for (let i = 0; i < 48; i++) {
    const u = userStore[i]!
    seed({
      ...base,
      actor_kind: 'user',
      actor_id: u.id,
      actor_email: u.email,
      action: 'user.registered',
      api_domain: 'public',
      auth_context: null,
      source_ip: homeIp(i),
      user_agent: ua(i),
      at: u.created_at,
    })
    // Go 只让 active 账号登录成功：pending 没有登录行（停用、封禁的是停用前登录的）
    if (u.last_login_at && u.status !== 'pending') {
      const lastMs = Date.parse(u.last_login_at)
      const createdMs = Date.parse(u.created_at)
      const loginTimes = [lastMs, lastMs - 3 * DAY, lastMs - 11 * DAY]
      for (let j = 0; j < loginTimes.length; j++) {
        const t = loginTimes[j]!
        if (t < createdMs || t < Date.now() - 90 * DAY) continue
        const ip = j === 1 && i % 3 === 0 ? mobileIp(i) : homeIp(i)
        seed({
          ...base,
          actor_kind: 'user',
          actor_id: u.id,
          actor_email: u.email,
          action: 'user.login',
          resource_type: 'session',
          resource_id: sessionId(),
          api_domain: 'public',
          auth_context: 'session',
          source_ip: ip,
          user_agent: ua(i),
          at: new Date(t).toISOString(),
        })
      }
      if (i % 5 === 2) {
        const pwdAt = Math.min(lastMs + 20 * MIN, Date.now() - MIN)
        if (pwdAt > createdMs) {
          seed({
            ...base,
            actor_kind: 'user',
            actor_id: u.id,
            actor_email: u.email,
            action: 'user.password_changed',
            resource_type: 'user',
            resource_id: u.id,
            api_domain: 'public',
            auth_context: 'session',
            source_ip: homeIp(i),
            user_agent: ua(i),
            at: new Date(pwdAt).toISOString(),
          })
        }
      }
    }
  }

  // B. 后台与系统动作
  for (let k = 0; k < 140; k++) {
    const ago = (k * 137 + 3) * MIN
    const at = iso(ago)
    const atMs = Date.now() - ago
    const s = STAFF[k % 4]!
    const t = userAt(k * 5 + 2)
    const staffRow = {
      actor_kind: 'admin' as const,
      actor_id: s.id,
      actor_email: s.email,
      api_domain: 'admin' as const,
      source_ip: s.ip,
      user_agent: BROWSER,
    }
    const mod = k % 12
    if (mod === 0) {
      seed({ ...base, ...staffRow, action: 'balance.adjusted', resource_type: 'user', resource_id: t.id, reason: '线路故障补偿', auth_context: 'reauth', at })
    } else if (mod === 1) {
      seed({ ...base, actor_kind: 'system', actor_id: null, actor_email: null, api_domain: null, auth_context: null, source_ip: null, user_agent: null, action: 'order.expired', resource_type: 'order', resource_id: randomUUID(), at })
    } else if (mod === 2) {
      seed({ ...base, ...staffRow, action: 'ticket.assign', resource_type: 'ticket', resource_id: randomUUID(), resource_label: ticketNo(k, atMs), auth_context: 'session', at })
    } else if (mod === 3) {
      seed({ ...base, ...staffRow, action: 'node.update', resource_type: 'node', resource_id: randomUUID(), auth_context: 'session', at })
    } else if (mod === 4) {
      seed({ ...base, ...staffRow, action: 'gift_card.batch_exported', resource_type: 'gift_card_batch', resource_id: randomUUID(), auth_context: 'reauth', at })
    } else if (mod === 5) {
      seed({ ...base, actor_kind: 'system', actor_id: null, actor_email: null, api_domain: null, auth_context: null, source_ip: null, user_agent: null, action: 'payment.succeeded', resource_type: 'order', resource_id: randomUUID(), at })
    } else if (mod === 6) {
      const sup = STAFF[4]!
      seed({
        ...base,
        actor_kind: 'admin',
        actor_id: sup.id,
        actor_email: sup.email,
        api_domain: 'admin',
        source_ip: sup.ip,
        user_agent: BROWSER,
        action: 'ticket.reply',
        resource_type: 'ticket',
        resource_id: randomUUID(),
        resource_label: ticketNo(k + 500, atMs),
        auth_context: 'session',
        at,
      })
    } else if (mod === 7) {
      seed({ ...base, ...staffRow, action: 'payment_provider.toggle', resource_type: 'payment_provider', resource_id: randomUUID(), reason: '渠道维护', auth_context: 'reauth', at })
    } else if (mod === 8) {
      seed({ ...base, ...staffRow, action: 'user.status_change', resource_type: 'user', resource_id: t.id, reason: '多次恶意退单', auth_context: 'reauth', at })
    } else if (mod === 9) {
      seed({ ...base, ...staffRow, action: 'withdrawal.approve', resource_type: 'withdrawal', resource_id: randomUUID(), auth_context: 'reauth', outcome: k % 7 === 0 ? 'partial' : 'success', at })
    } else if (mod === 10) {
      seed({ ...base, ...staffRow, action: 'subscription.extended', resource_type: 'subscription', resource_id: randomUUID(), reason: '线路故障补偿', auth_context: 'reauth', at })
    } else {
      // 溯源「显示」明文 IP（Go 只这一处记 source_ip_viewed，对象为空）
      seed({ ...base, ...staffRow, action: 'security.source_ip_viewed', resource_type: null, resource_id: null, auth_context: 'session', at })
    }
  }

  // C. 套餐
  for (const p of plans) {
    for (const v of p.versions) {
      const op = staffByEmail(v.created_by_email)
      const staffRow = {
        actor_kind: 'admin' as const,
        actor_id: op.id,
        actor_email: op.email,
        api_domain: 'admin' as const,
        source_ip: op.ip,
        user_agent: BROWSER,
      }
      if (v.status === 'published' && v.published_at) {
        seed({
          ...base,
          ...staffRow,
          action: 'plan_version.publish',
          resource_type: 'plan_version',
          resource_id: v.id,
          auth_context: 'reauth',
          at: v.published_at,
        })
      } else if (v.status === 'draft') {
        seed({
          ...base,
          ...staffRow,
          action: 'plan.usage_update',
          resource_type: 'plan',
          resource_id: p.id,
          auth_context: 'reauth',
          at: v.created_at,
        })
      }
    }
  }

  // D. k.liu 撞库
  const kl = userStore[1]!
  for (let j = 0; j < 15; j++) {
    anonFail({
      action: 'user.login_failed',
      at: iso((7 + j * 12) * MIN),
      resource_type: 'user',
      resource_id: kl.id,
      reason: 'invalid_credentials',
      source_ip: j % 2 === 0 ? '203.0.113.48' : '203.0.113.19',
      user_agent: ATTACK_UA,
    })
  }
  if (kl.last_login_at) {
    anonFail({
      action: 'user.login_failed',
      at: new Date(Date.parse(kl.last_login_at) - MIN).toISOString(),
      resource_type: 'user',
      resource_id: kl.id,
      reason: 'invalid_credentials',
      source_ip: homeIp(1),
      user_agent: BROWSER,
    })
  }

  // E. 其它登录失败
  for (let k = 0; k < 40; k++) {
    const at = iso((k * 503 + 11) * MIN)
    const target = k % 4 === 0 ? userAt(k * 3 + 13) : null
    const inactive = k % 10 === 7
    anonFail({
      action: 'user.login_failed',
      at,
      resource_type: inactive || target ? 'user' : null,
      resource_id: inactive ? userAt(9).id : target?.id ?? null,
      reason: inactive ? 'account_inactive' : 'invalid_credentials',
      source_ip: '198.51.100.18',
      user_agent: ATTACK_UA,
    })
  }

  // F. 聚类登录行：第 m 个成员在 lastMin + m * 7 分钟前、再早 2 天、再早 9 天各登录一次
  for (const scene of CLUSTER_SCENES) {
    scene.members.forEach((member, m) => {
      const u = userStore[member]!
      for (const extraMin of [0, 2 * 24 * 60, 9 * 24 * 60]) {
        const tMin = scene.lastMin + m * 7 + extraMin
        seed({
          ...base,
          actor_kind: 'user',
          actor_id: u.id,
          actor_email: u.email,
          action: 'user.login',
          resource_type: 'session',
          resource_id: sessionId(),
          api_domain: 'public',
          auth_context: scene.legacy ? null : 'session',
          source_ip: scene.legacy ? null : scene.ip,
          ip_hash: scene.ip,
          user_agent: ua(member),
          at: iso(tMin * MIN),
        })
      }
    })
  }

  // Go 每次登录成功都写 users.last_login_at：聚类场景里的登录比用户种子的 last_login_at 新时跟上
  for (const r of seedRows) {
    if (r.action !== 'user.login' || !r.actor_id) continue
    const u = findUser(r.actor_id)
    if (u && (!u.last_login_at || u.last_login_at < r.occurred_at)) u.last_login_at = r.occurred_at
  }

  audit.push(...seedRows)
  audit.sort((a, b) => b.occurred_at.localeCompare(a.occurred_at))
})()

const csvSafe = (s: string) => (s && '=+-@\t\r'.includes(s[0]!) ? `'${s}` : s)
const csvCell = (s: string) => (/[",\r\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s)
/** AuditRow 的 JSON：actor_id 与 user_agent 只给访问日志用，审计列表不出 */
const auditJson = (r: AuditRow) => ({
  id: r.id,
  occurred_at: r.occurred_at,
  actor_kind: r.actor_kind,
  actor_email: r.actor_email,
  action: r.action,
  resource_type: r.resource_type,
  resource_id: r.resource_id,
  api_domain: r.api_domain,
  outcome: r.outcome,
  reason: r.reason,
  resource_label: labelOf(r),
  auth_context: r.auth_context,
  source_ip: r.source_ip,
})

interface AuditQuery {
  action: string
  actorKind: string
  outcome: string
  q: string
  ids: Set<string>
  from: number | null
  to: number | null
}
function auditFilter(q: URLSearchParams): AuditQuery {
  const text = (q.get('q') ?? '').trim()
  return {
    action: q.get('action') ?? '',
    actorKind: q.get('actor_kind') ?? '',
    outcome: q.get('outcome') ?? '',
    q: text,
    ids: text ? idsByEmail(text) : new Set(),
    from: null,
    to: null,
  }
}
function auditMatch(r: AuditRow, f: AuditQuery): boolean {
  const q = f.q.toLowerCase()
  const t = Date.parse(r.occurred_at)
  const searchHit =
    !q ||
    r.action.toLowerCase().includes(q) ||
    (r.actor_id !== null && f.ids.has(r.actor_id)) ||
    (r.resource_type === 'user' && r.resource_id !== null && f.ids.has(r.resource_id)) ||
    r.resource_id === f.q ||
    r.actor_id === f.q
  return (
    (!f.action || r.action.startsWith(f.action)) &&
    (!f.actorKind || r.actor_kind === f.actorKind) &&
    (!f.outcome || r.outcome === f.outcome) &&
    searchHit &&
    (f.from === null || t >= f.from) &&
    (f.to === null || t < f.to)
  )
}
/** atoiDefault：空或非数字取默认；ListAudit 再把 <= 0 或 > 200 的 limit 改回 50 */
const atoi = (v: string | null, def: number) => {
  if (!v) return def
  const n = Number(v)
  return Number.isInteger(n) ? n : def
}

/** auditExportRange：YYYY-MM-DD，半开区间 [from, to + 1 天)，格式错 422 */
function exportRange(q: URLSearchParams): { from: number | null; to: number | null } | MockResult {
  const parse = (field: string): number | null | MockResult => {
    const raw = (q.get(field) ?? '').trim()
    if (!raw) return null
    const t = Date.parse(`${raw}T00:00:00Z`)
    if (!/^\d{4}-\d{2}-\d{2}$/.test(raw) || Number.isNaN(t) || !new Date(t).toISOString().startsWith(raw)) return invalid({ [field]: '日期格式应为 YYYY-MM-DD' })
    return t
  }
  const from = parse('from')
  if (from !== null && typeof from === 'object') return from
  const to = parse('to')
  if (to !== null && typeof to === 'object') return to
  const next = to === null ? null : to + DAY
  if (from !== null && next !== null && from >= next) return invalid({ to: '结束日期不能早于开始日期' })
  return { from, to: next }
}

// ---------------------------------------------------------------------------
// 访问日志：审计与订阅拉取两路归并（access_log.go）
// ---------------------------------------------------------------------------
interface Fetch {
  at: string
  user: User | null
  ip: string
  ua: string
  result: string
  format: string
}
const fetches: Fetch[] = []
const CLIENTS = ['clash-verge/2.0.4', 'sing-box/1.10.1', 'Shadowrocket/2.2.53', 'v2rayN/7.1']

/** 照 Go render.UAFamily：按顺序匹配，先命中者胜 */
const UA_FAMILIES: ReadonlyArray<readonly [string, readonly string[]]> = [
  ['sing-box', ['sing-box', 'sfi/', 'sfa/']],
  ['shadowrocket', ['shadowrocket']],
  ['stash', ['stash']],
  ['loon', ['loon']],
  ['surfboard', ['surfboard']],
  ['clash', ['clash', 'mihomo']],
  ['quantumult', ['quantumult']],
  ['surge', ['surge']],
  ['v2ray', ['v2ray']],
  ['shadowsocks', ['shadowsocks']],
]
function uaFamily(ua: string): string {
  const s = ua.toLowerCase()
  if (!s) return 'none'
  return UA_FAMILIES.find(([, needles]) => needles.some((n) => s.includes(n)))?.[0] ?? 'other'
}

function fetchFormat(ua: string): string {
  const fam = uaFamily(ua)
  if (fam === 'clash') return 'clash'
  if (fam === 'sing-box') return 'singbox'
  return 'uri'
}

const userHasLiveSub = (u: User) => u.subs.some((s) => s.status === 'active')

;(function seedFetches() {
  for (let k = 0; k < 120; k++) {
    const scan = k % 11 === 5
    if (scan) {
      fetches.push({ at: iso((k * 37 + 1) * MIN), user: null, ip: '203.0.113.14', ua: 'curl/8.4.0', result: 'not_found', format: '' })
      continue
    }
    const idx = (k * 7 + 1) % 48
    const user = userStore[idx]!
    const ip = k % 3 === 0 ? mobileIp(idx) : homeIp(idx)
    const uaStr = CLIENTS[idx % CLIENTS.length]!
    const live = userHasLiveSub(user)
    const result = !live ? 'expired' : k % 17 === 8 ? 'rate_limited' : k % 23 === 11 ? 'error' : 'ok'
    fetches.push({ at: iso((k * 37 + 1) * MIN), user, ip, ua: uaStr, result, format: fetchFormat(uaStr) })
  }
})()

/** 最近一条冲着 k.liu 的匿名登录失败是否在 10 分钟内 */
function recentKlLoginFailed(): boolean {
  const kl = userStore[1]!
  for (const r of audit) {
    if (
      r.action === 'user.login_failed' &&
      r.actor_id === null &&
      r.resource_type === 'user' &&
      r.resource_id === kl.id
    ) {
      return Date.now() - Date.parse(r.occurred_at) < 10 * MIN
    }
  }
  return false
}

/** 实时监控要看得到新行：每次读按流逝时间补事件，约 6 秒一条，一次最多 3 条 */
let lastTick = Date.now()
function tick() {
  const due = Math.min(3, Math.floor((Date.now() - lastTick) / 6000))
  for (let i = 0; i < due; i++) {
    const n = audit.length + fetches.length
    const u = userAt(n * 7)
    const ui = indexOf(u)
    const ip = homeIp(ui)
    const at = new Date(Date.now() - (due - 1 - i) * 1000).toISOString()
    if (n % 3 === 0) {
      if (ui === 1) {
        const kl = userStore[1]!
        if (!recentKlLoginFailed()) {
          record({
            at,
            actor_kind: 'anonymous',
            actor_id: null,
            actor_email: null,
            action: 'user.login_failed',
            resource_type: 'user',
            resource_id: kl.id,
            resource_label: null,
            api_domain: 'public',
            outcome: 'failure',
            reason: 'invalid_credentials',
            auth_context: null,
            source_ip: n % 2 === 0 ? '203.0.113.48' : '203.0.113.19',
            user_agent: ATTACK_UA,
          })
        } else {
          const uaStr = CLIENTS[ui % CLIENTS.length]!
          fetches.unshift({ at, user: u, ip, ua: uaStr, result: 'ok', format: fetchFormat(uaStr) })
        }
      } else if (u.status === 'active') {
        // Go 只让 active 账号登录成功：停用、待激活的这一拍什么都不写
        record({
          at,
          actor_kind: 'user',
          actor_id: u.id,
          actor_email: u.email,
          action: 'user.login',
          resource_type: 'session',
          resource_id: sessionId(),
          resource_label: null,
          api_domain: 'public',
          outcome: 'success',
          reason: null,
          auth_context: 'session',
          source_ip: ip,
          user_agent: ua(ui),
        })
        u.last_login_at = at
      }
    } else {
      const uaStr = CLIENTS[ui % CLIENTS.length]!
      fetches.unshift({ at, user: u, ip, ua: uaStr, result: 'ok', format: fetchFormat(uaStr) })
    }
  }
  if (due > 0) lastTick = Date.now()
}

/** accessCategoryRules：先命中者胜，admin 排在 payment 前面 */
const CATEGORY_RULES: ReadonlyArray<readonly [string, readonly string[]]> = [
  ['login', ['user.login', 'auth.quick_login', 'auth.mail_link_login']],
  ['register', ['user.registered']],
  ['reset_password', ['user.password', 'user.reset']],
  ['order', ['order.']],
  ['admin', ['node.', 'route_group.', 'admin.', 'server.', 'plan.', 'payment_provider.']],
  ['payment', ['payment']],
  ['ticket', ['ticket.']],
]
const categoryOf = (action: string) => CATEGORY_RULES.find(([, ps]) => ps.some((p) => action.startsWith(p)))?.[0] ?? 'other'
const KNOWN_CATEGORIES = new Set([...CATEGORY_RULES.map(([c]) => c), 'other', 'subscribe'])

type AccessItem = Record<string, string>
/** omitempty：空串的键不出现 */
const compact = (o: Record<string, string>): AccessItem => Object.fromEntries(Object.entries(o).filter(([, v]) => v !== ''))

function accessAuditItem(r: AuditRow): AccessItem & { occurred_at: string } {
  const g = geoOf(r.source_ip ?? '')
  const target =
    r.actor_id === null && r.action === 'user.login_failed' && r.resource_type === 'user' && r.resource_id
      ? { target_user_id: r.resource_id, target_user_email: emailOf(r.resource_id) ?? '' }
      : { target_user_id: '', target_user_email: '' }
  return {
    ...compact({
      category: categoryOf(r.action),
      action: r.action,
      user_id: r.actor_id ?? '',
      user_email: emailOf(r.actor_id) ?? '',
      ...target,
      ip: r.source_ip ?? '',
      geo: g.geo,
      network_kind: g.kind,
      user_agent: r.user_agent ?? '',
      outcome: r.outcome,
    }),
    occurred_at: r.occurred_at,
  }
}

function accessList(ctx: MockContext): MockResult {
  const q = ctx.query
  const lim = Number((q.get('limit') ?? '').trim())
  const limit = Number.isInteger(lim) && lim > 0 && lim <= 200 ? lim : 50
  const off = Number((q.get('offset') ?? '').trim())
  const offset = Number.isInteger(off) && off > 0 ? off : 0
  const category = (q.get('category') ?? '').trim().toLowerCase()
  if (category && !KNOWN_CATEGORIES.has(category)) return invalid({ category: '不认识的分类' })
  const outcome = (q.get('outcome') ?? '').trim().toLowerCase()
  if (!['', 'success', 'failure', 'denied', 'partial', 'error'].includes(outcome)) return invalid({ outcome: '只支持 success、failure、denied、partial 或 error' })
  const ip = (q.get('ip') ?? '').trim()
  const who = (q.get('user') ?? '').trim()
  const ids: Set<string> | null = !who ? null : UUID.test(who) ? new Set([who.toLowerCase()]) : idsByEmail(who)
  const auditUserHit = (r: AuditRow) =>
    ids === null || (r.actor_id !== null && ids.has(r.actor_id)) || (r.action === 'user.login_failed' && r.resource_type === 'user' && r.resource_id !== null && ids.has(r.resource_id))
  const fetchUserHit = (userId: string | undefined) => ids === null || (userId !== undefined && ids.has(userId))

  const items: Array<AccessItem & { occurred_at: string }> = []
  if (category !== 'subscribe') {
    for (const r of audit) {
      if (category && categoryOf(r.action) !== category) continue
      if (ip && r.source_ip !== ip) continue
      if (!auditUserHit(r)) continue
      if (outcome && !(outcome === 'error' ? r.outcome !== 'success' : r.outcome === outcome)) continue
      items.push(accessAuditItem(r))
    }
  }
  if ((category === '' || category === 'subscribe') && ['', 'success', 'error'].includes(outcome)) {
    for (const f of fetches) {
      if (ip && f.ip !== ip) continue
      if (!fetchUserHit(f.user?.id)) continue
      if (outcome === 'success' && f.result !== 'ok') continue
      if (outcome === 'error' && f.result === 'ok') continue
      const g = geoOf(f.ip)
      items.push({ ...compact({ category: 'subscribe', action: 'subscription.fetch', user_id: f.user?.id ?? '', user_email: f.user?.email ?? '', ip: f.ip, geo: g.geo, network_kind: g.kind, user_agent: f.ua, outcome: f.result }), occurred_at: f.at })
    }
  }
  items.sort((a, b) => b.occurred_at.localeCompare(a.occurred_at))
  return { status: 200, body: { items: items.slice(offset, offset + limit) } }
}

// ---------------------------------------------------------------------------
// 共享 IP 聚类（audit_ip_clusters 视图 + ip_cluster_reviews）
// ---------------------------------------------------------------------------
interface Review {
  decision: 'normal' | 'disabled'
  decided_at: string
  expires_at: string | null
}
interface MockCluster {
  key: string
  hash: string
  ip: string
  members: string[]
  events: number
  first: string
  last: string
  review: Review | null
}
const NORMAL_TTL = 30 * DAY
const CLUSTER_WINDOW = 90 * DAY
const hashKey = (seed: string) => createHash('sha256').update(seed).digest('hex')

/** ip_cluster_reviews：按 ip_hash 存复核结论；种子与原来的三条一致 */
const reviews = new Map<string, Review>([
  ['198.51.100.51', { decision: 'normal', decided_at: iso(32 * DAY), expires_at: iso(2 * DAY) }],
  ['198.51.100.61', { decision: 'normal', decided_at: iso(10 * DAY), expires_at: iso(-20 * DAY) }],
  ['198.51.100.31', { decision: 'disabled', decided_at: iso(3 * DAY), expires_at: null }],
])

/** 照 Go audit/repo Clusters：近 90 天多账号同源 IP */
function computeClusters(): MockCluster[] {
  const since = Date.now() - CLUSTER_WINDOW
  const byHash = new Map<string, { members: Set<string>; events: number; first: string; last: string }>()
  for (const r of audit) {
    if (!r.ip_hash || r.actor_kind !== 'user' || !r.actor_id) continue
    if (Date.parse(r.occurred_at) <= since) continue
    let g = byHash.get(r.ip_hash)
    if (!g) {
      g = { members: new Set(), events: 0, first: r.occurred_at, last: r.occurred_at }
      byHash.set(r.ip_hash, g)
    }
    g.members.add(r.actor_id)
    g.events++
    if (r.occurred_at < g.first) g.first = r.occurred_at
    if (r.occurred_at > g.last) g.last = r.occurred_at
  }
  const out: MockCluster[] = []
  for (const [hash, g] of byHash) {
    if (g.members.size <= 1) continue
    const members = [...g.members].sort()
    let ip = ''
    let ipAt = ''
    for (const r of audit) {
      if (r.ip_hash !== hash || !r.source_ip) continue
      if (!ipAt || r.occurred_at > ipAt) {
        ipAt = r.occurred_at
        ip = r.source_ip
      }
    }
    out.push({
      key: hashKey(hash),
      hash,
      ip,
      members,
      events: g.events,
      first: g.first,
      last: g.last,
      review: reviews.get(hash) ?? null,
    })
  }
  out.sort((a, b) => b.members.length - a.members.length || b.last.localeCompare(a.last) || a.hash.localeCompare(b.hash))
  return out
}

/** clusterRisk：机房出口或账号 ≥ 5 为高，≥ 3 为中 */
const riskOf = (accounts: number, kind: string) => (accounts >= 5 || kind === 'datacenter' ? 'high' : accounts >= 3 ? 'mid' : 'low')

function clusterJson(c: MockCluster) {
  const users = c.members
    .map(findUser)
    .filter((u): u is User => !!u)
    .map((u) => ({ id: u.id, email: u.email, status: u.status, active_plan: activePlan(u) }))
    .sort((a, b) => a.email.localeCompare(b.email))
  const g = geoOf(c.ip)
  return { ip: c.ip, geo: g.geo, network_kind: g.kind, risk: riskOf(c.members.length, g.kind), key: c.key, accounts: c.members.length, events: c.events, first: c.first, last: c.last, emails: users.map((u) => u.email), users, review: c.review }
}

/** ParseClusterKey：不是非空 hex 即 404；视图里没有也是 404 */
const clusterBy = (key: string) => {
  const k = key.trim().toLowerCase()
  if (!/^([0-9a-f]{2})+$/i.test(k)) return undefined
  return computeClusters().find((c) => c.key === k)
}

async function disableAccounts(ctx: MockContext): Promise<MockResult> {
  const d = await decode(ctx, { user_ids: 'strings', reason: 'string' })
  if (!d.ok) return d.result
  if (!/^([0-9a-f]{2})+$/i.test(ctx.params.key!)) return NOT_FOUND
  const reason = String(d.body.reason ?? '').trim()
  const raw = Array.isArray(d.body.user_ids) ? (d.body.user_ids as string[]) : []
  const fields: Record<string, string> = {}
  if (runes(reason) < 5 || runes(reason) > 500) fields.reason = '原因必须为 5 到 500 字'
  if (raw.some((id) => !UUID.test(id.trim()))) fields.user_ids = '包含无效的账号 ID'
  const ids = [...new Set(raw.map((id) => id.trim().toLowerCase()))].sort()
  if (!fields.user_ids && (ids.length === 0 || ids.length > 200)) fields.user_ids = '请选择 1 到 200 个账号'
  if (Object.keys(fields).length > 0) return invalid(fields)
  const c = clusterBy(ctx.params.key!)
  if (!c) return NOT_FOUND

  const skipped: Array<{ user_id: string; reason: string }> = []
  const done: User[] = []
  for (const id of ids) {
    const u = findUser(id)
    if (id === ctx.user.userId) skipped.push({ user_id: id, reason: 'self' })
    else if (!c.members.includes(id) || !u) skipped.push({ user_id: id, reason: 'not_member' })
    else if (u.is_admin) skipped.push({ user_id: id, reason: 'administrator' })
    else if (u.status === 'suspended' || u.status === 'banned') skipped.push({ user_id: id, reason: 'already_disabled' })
    else done.push(u)
  }
  const actor = { actor_kind: 'admin', actor_id: ctx.user.userId, actor_email: ctx.user.email, api_domain: 'admin', auth_context: 'reauth' as const, source_ip: ADMIN_IP, user_agent: BROWSER }
  for (const u of done) {
    u.status = 'suspended'
    record({ ...actor, action: 'user.status_change', resource_type: 'user', resource_id: u.id, resource_label: null, outcome: 'success', reason })
  }
  if (done.length > 0) reviews.set(c.hash, { decision: 'disabled', decided_at: new Date().toISOString(), expires_at: null })
  record({ ...actor, action: 'risk.ip_cluster.disable', resource_type: done.length ? 'ip_cluster_review' : null, resource_id: null, resource_label: null, outcome: done.length === 0 ? 'failure' : skipped.length ? 'partial' : 'success', reason })
  return { status: 200, body: { disabled: done.length, skipped } }
}

// ---------------------------------------------------------------------------
// 用户画像（GET /v1/users/:id/profile，照 identity/service UserProfile）
// ---------------------------------------------------------------------------
const riskRank = (r: string) => (r === 'high' ? 3 : r === 'mid' ? 2 : r === 'low' ? 1 : 0)
const higherRisk = (a: string, b: string) => (riskRank(a) >= riskRank(b) ? a : b)

/** 近窗内同一 ip_hash 上出现过的不重复用户 id */
function coAccountIds(ipHash: string, sinceMs: number): Set<string> {
  const ids = new Set<string>()
  for (const r of audit) {
    if (r.ip_hash !== ipHash || r.actor_kind !== 'user' || !r.actor_id) continue
    if (Date.parse(r.occurred_at) <= sinceMs) continue
    ids.add(r.actor_id)
  }
  return ids
}

function profileOf(u: User) {
  const sinceMs = Date.now() - CLUSTER_WINDOW
  const mine = audit.filter((r) => r.actor_id === u.id)

  const events = mine.slice(0, 80).map((r) => ({
    action: r.action,
    outcome: r.outcome,
    ip: r.source_ip ?? '',
    ua: r.user_agent ?? '',
    domain: r.api_domain ?? '',
    at: r.occurred_at,
  }))

  let registered_ip = ''
  let regAt = ''
  for (const r of mine) {
    if (r.action !== 'user.registered' || !r.source_ip) continue
    if (!regAt || r.occurred_at < regAt) {
      regAt = r.occurred_at
      registered_ip = r.source_ip
    }
  }

  type IpAgg = { ip_hash: string; count: number; first: string; last: string; ip: string; latestIpAt: string; accountIds: Set<string> }
  const ipMap = new Map<string, IpAgg>()
  for (const r of mine) {
    if (!r.ip_hash) continue
    let g = ipMap.get(r.ip_hash)
    if (!g) {
      g = { ip_hash: r.ip_hash, count: 0, first: r.occurred_at, last: r.occurred_at, ip: '', latestIpAt: '', accountIds: new Set() }
      ipMap.set(r.ip_hash, g)
    }
    g.count++
    if (r.occurred_at < g.first) g.first = r.occurred_at
    if (r.occurred_at > g.last) g.last = r.occurred_at
    if (r.source_ip && (!g.latestIpAt || r.occurred_at > g.latestIpAt)) {
      g.latestIpAt = r.occurred_at
      g.ip = r.source_ip
    }
  }
  for (const g of ipMap.values()) {
    const ids = coAccountIds(g.ip_hash, sinceMs)
    g.accountIds = ids
  }

  const ipRows = [...ipMap.values()].sort(
    (a, b) => b.count - a.count || b.last.localeCompare(a.last) || a.ip_hash.localeCompare(b.ip_hash),
  )
  let risk = ''
  const relatedSet = new Set<string>()
  const ips = ipRows.slice(0, 20).map((g) => {
    const accounts = g.accountIds.size > 1 ? g.accountIds.size : 1
    const { geo, kind } = geoOf(g.ip)
    const ipRisk = accounts > 1 ? riskOf(accounts, kind) : ''
    risk = higherRisk(risk, ipRisk)
    if (accounts > 1) {
      for (const id of g.accountIds) if (id !== u.id) relatedSet.add(id)
    }
    return {
      ip: g.ip,
      count: g.count,
      first: g.first,
      last: g.last,
      accounts,
      geo,
      network_kind: kind,
      risk: ipRisk,
    }
  })

  const related = [...relatedSet]
    .sort()
    .slice(0, 50)
    .map((id) => findUser(id))
    .filter((x): x is User => !!x)
    .map((x) => ({ id: x.id, email: x.email }))
    .sort((a, b) => a.email.localeCompare(b.email))

  const myFetches = fetches.filter((f) => f.user?.id === u.id)
  const fetchesOut = myFetches.slice(0, 50).map((f) => ({
    ip: f.ip,
    ua: f.ua,
    family: uaFamily(f.ua),
    result: f.result,
    format: f.format,
    at: f.at,
  }))

  const weekAgo = Date.now() - 7 * DAY
  const fetch_sources_7d = new Set(myFetches.filter((f) => Date.parse(f.at) > weekAgo).map((f) => f.ip)).size

  return {
    events,
    ips,
    related,
    fetches: fetchesOut,
    fetch_sources_7d,
    registered_ip,
    risk,
  }
}

setProfileSource(profileOf)

// ---------------------------------------------------------------------------
// 降级开关（feature_switches：00002 种下、00064 删去三个核心项后的五行；与 settings/service SeedSwitches 一致）
// ---------------------------------------------------------------------------
interface Switch {
  code: string
  enabled: boolean
  reason: string | null
}
const switches: Switch[] = [
  { code: 'auth.registration', enabled: true, reason: null },
  { code: 'billing.checkout', enabled: true, reason: null },
  { code: 'marketing.giftcard.redeem', enabled: true, reason: null },
  { code: 'notify.email', enabled: true, reason: null },
  { code: 'admin.writes', enabled: true, reason: null },
]
/** admin.writes 缺行视为开启（R58）；外壳 mock-api.ts 的只读门按它把非豁免写请求回 503 */
export const adminWritesEnabled = () => switches.find((s) => s.code === 'admin.writes')?.enabled ?? true

/** 切换成功后通知外壳，外壳向后台所有 SSE 连接推 switches.changed { code, enabled }（只发管理端，与 Go 一致） */
const switchListeners = new Set<(payload: { code: string; enabled: boolean }) => void>()
export function onSwitchChanged(listener: (payload: { code: string; enabled: boolean }) => void): () => void {
  switchListeners.add(listener)
  return () => switchListeners.delete(listener)
}

async function setSwitch(ctx: MockContext): Promise<MockResult> {
  const d = await decode(ctx, { enabled: 'boolean', reason: 'string' })
  if (!d.ok) return d.result
  const s = switches.find((x) => x.code === ctx.params.code)
  if (!s) return NOT_FOUND
  const enabled = d.body.enabled === true
  const reason = typeof d.body.reason === 'string' ? d.body.reason : ''
  const stored = reason.trim() ? reason : null
  // 数据库 CHECK feature_switches_disable_needs_reason，Go 翻成 409 并换成中文原因（与 settings/service SetSwitch 同文）
  if (!enabled && stored === null) return err(409, 'conflict', `开关 ${s.code} 不允许该操作：关闭开关必须填写原因`)
  s.enabled = enabled
  s.reason = stored
  record({ actor_kind: 'admin', actor_id: ctx.user.userId, actor_email: ctx.user.email, action: 'feature_switch.toggle', resource_type: 'feature_switch', resource_id: null, resource_label: null, api_domain: 'admin', outcome: 'success', reason: reason || null, auth_context: 'reauth', source_ip: ADMIN_IP, user_agent: BROWSER })
  switchListeners.forEach((fn) => fn({ code: s.code, enabled }))
  return { status: 200, body: { ok: true, enabled } }
}

// 溯源总览在 security-trace.ts，由下面的审计行与拉取日志现算

// ---------------------------------------------------------------------------
// 路由表
// ---------------------------------------------------------------------------
export const security: MockModule = {
  routes: {
    'GET /v1/audit': (ctx) => {
      tick()
      let limit = atoi(ctx.query.get('limit'), 50)
      if (limit <= 0 || limit > 200) limit = 50
      const offset = Math.max(0, atoi(ctx.query.get('offset'), 0))
      const rows = audit.filter((r) => auditMatch(r, auditFilter(ctx.query)))
      ctx.send(200, { events: rows.slice(offset, offset + limit).map(auditJson), total: rows.length })
    },
    'GET /v1/audit/export': (ctx) => {
      if (!ctx.requireReauth()) return
      const range = exportRange(ctx.query)
      if ('status' in range) return reply(ctx, range)
      const f = { ...auditFilter(ctx.query), ...range }
      const rows = audit.filter((r) => auditMatch(r, f))
      if (rows.length > 50_000) return reply(ctx, err(422, 'validation_failed', '超过 50000 行，请缩小时间范围'))
      const s = (v: string | null) => v ?? ''
      const lines = [
        'occurred_at,actor_kind,actor_email,action,resource_type,resource_id,resource_label,api_domain,outcome,reason,source_ip,auth_context',
        ...rows.map((r) =>
          [r.occurred_at.replace(/\.\d+Z$/, 'Z'), r.actor_kind, csvSafe(s(r.actor_email)), r.action, s(r.resource_type), s(r.resource_id), csvSafe(s(labelOf(r))), s(r.api_domain), r.outcome, csvSafe(s(r.reason)), s(r.source_ip), s(r.auth_context)].map(csvCell).join(','),
        ),
      ]
      record({ actor_kind: 'admin', actor_id: ctx.user.userId, actor_email: ctx.user.email, action: 'audit.export', resource_type: null, resource_id: null, resource_label: null, api_domain: 'admin', outcome: 'success', reason: null, auth_context: 'reauth', source_ip: ADMIN_IP, user_agent: BROWSER })
      const stamp = new Date().toISOString().replace(/[-:]/g, '').replace('T', '-').slice(0, 15)
      ctx.sendRaw(200, { contentType: 'text/csv; charset=utf-8', text: `\ufeff${lines.join('\n')}\n`, headers: { 'Content-Disposition': `attachment; filename="audit-${stamp}.csv"` } })
    },

    'GET /v1/access-log': (ctx) => {
      tick()
      const r = accessList(ctx)
      reply(ctx, r)
    },

    'GET /v1/ip-clusters': (ctx) => {
      const all = ctx.query.get('include_reviewed') === '1'
      const now = Date.now()
      const rows = computeClusters()
        .filter((c) => all || c.review?.decision !== 'normal' || (c.review.expires_at !== null && Date.parse(c.review.expires_at) <= now))
        .slice(0, 50)
        .map(clusterJson)
      ctx.send(200, { clusters: rows })
    },

    ...traceRoutes({
      audit,
      fetches,
      geoOf,
      uaFamily,
      users: () => userStore,
      onReveal: (ctx) =>
        record({
          actor_kind: 'admin',
          actor_id: ctx.user.userId,
          actor_email: ctx.user.email,
          action: 'security.source_ip_viewed',
          resource_type: null,
          resource_id: null,
          resource_label: null,
          api_domain: 'admin',
          outcome: 'success',
          reason: null,
          auth_context: 'session',
          source_ip: ADMIN_IP,
          user_agent: BROWSER,
        }),
    }),
    'POST /v1/ip-clusters/:key/review': async (ctx) => {
      const d = await decode(ctx, { note: 'string' })
      if (!d.ok) return reply(ctx, d.result)
      if (!/^([0-9a-f]{2})+$/i.test(ctx.params.key!)) return reply(ctx, NOT_FOUND)
      const note = String(d.body.note ?? '').trim()
      if (runes(note) > 500) return reply(ctx, invalid({ note: '备注不能超过 500 字' }))
      const c = clusterBy(ctx.params.key!)
      if (!c) return reply(ctx, NOT_FOUND)
      const expires = new Date(Date.now() + NORMAL_TTL).toISOString()
      reviews.set(c.hash, { decision: 'normal', decided_at: new Date().toISOString(), expires_at: expires })
      record({ actor_kind: 'admin', actor_id: ctx.user.userId, actor_email: ctx.user.email, action: 'risk.ip_cluster.mark_normal', resource_type: 'ip_cluster_review', resource_id: null, resource_label: null, api_domain: 'admin', outcome: 'success', reason: null, auth_context: ctx.reauthed ? 'reauth' : 'session', source_ip: ADMIN_IP, user_agent: BROWSER })
      ctx.send(200, { key: ctx.params.key, decision: 'normal', expires_at: expires.replace(/\.\d+Z$/, 'Z') })
    },
    'POST /v1/ip-clusters/:key/disable-accounts': async (ctx) => {
      if (!ctx.requireReauth()) return
      await ctx.idempotent('ip_cluster_disable', () => disableAccounts(ctx))
    },

    'GET /v1/switches': (ctx) => {
      const rows = [...switches].sort((a, b) => a.code.localeCompare(b.code))
      ctx.send(200, { switches: rows })
    },
    'POST /v1/switches/:code': async (ctx) => {
      if (!ctx.requireReauth()) return
      reply(ctx, await setSwitch(ctx))
    },
  },
}
