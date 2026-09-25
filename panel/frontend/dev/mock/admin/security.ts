/**
 * [INPUT]: 依赖 node:crypto 的 createHash / randomUUID，依赖 ../types 的 Json / MockContext / MockModule / MockResult，依赖 ./users 的 userStore 与 User（聚类成员、审计与访问日志的账号都是真实种子用户，停用直接改同一份用户数组）
 * [OUTPUT]: 对外提供 security 模块的假接口 MockModule、adminWritesEnabled（外壳的只读门读它）、onSwitchChanged（外壳的 SSE 订阅它，推 switches.changed）
 * [POS]: dev/mock/admin 的「安全与运维（后台-09 后半）」假接口，归后台前端二；形状、权限、reauth、幂等与文案照 api-contract.md（含 R23 R40 R41 R44 R58）与 Go 的 audit_log.go + adminops/audit.go、access_log.go、risk.go + adminops/risk.go、handlers.go setSwitch + adminops SetSwitch：
 *        审计（q / action 前缀 / actor_kind / outcome、limit 越界回 50、存量行无 auth_context 与来源 IP）与导出（两个权限 + reauth、日期 422、5 万行上限、BOM 与防公式、导出本身记审计）；访问日志（审计与订阅拉取两路归并、分类表与 Go 同一张、category / outcome 未知 422、IP 精确、账号 UUID 或邮箱片段，每次读按流逝时间补几条新事件让「实时尾随」有动静）；
 *        IP 聚类（成员取真实种子用户、风险按 Go 的 clusterRisk、标记正常 30 天内默认不列）、标记正常（note ≤ 500）、批量停用（两个权限 + reauth + 幂等 ip_cluster_disable，逐个跳过自己 / 非成员 / 后台账号 / 已停用，一个都没停成不写结论）；降级开关（八行种子（R102 删去三个未接入的）、核心项与关闭不给原因回 409 数据库原文、切换记审计）。按 DisallowUnknownFields 拒绝未知字段
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { createHash, randomUUID } from 'node:crypto'
import type { Json, MockContext, MockModule, MockResult } from '../types.ts'
import { userStore, type User } from './users.ts'

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
  ['10.', '内网地址', 'private'],
  ['192.168.', '内网地址', 'private'],
  ['127.', '本机回环地址', 'loopback'],
  ['223.104.', '中国 广东 移动', 'mobile'],
  ['117.136.', '中国 江苏 移动', 'mobile'],
  ['39.144.', '中国 北京 移动', 'mobile'],
  ['112.64.', '中国 上海 联通', 'residential'],
  ['61.216.', '中国 台湾 中华电信', 'residential'],
  ['202.120.', '中国 上海 教育网', 'education'],
  ['45.76.', '日本 东京 Vultr', 'datacenter'],
  ['149.28.', '美国 洛杉矶 Vultr', 'datacenter'],
]
function geoOf(ip: string): { geo: string; kind: string } {
  const hit = ip ? GEO.find(([p]) => ip.startsWith(p)) : undefined
  return hit ? { geo: hit[1], kind: hit[2] } : { geo: '', kind: '' }
}

const userAt = (i: number): User => userStore[i % userStore.length]!
const findUser = (id: string) => userStore.find((u) => u.id === id)
const activePlan = (u: User) =>
  [...u.subs]
    .filter((s) => s.status === 'active' || s.status === 'trialing')
    .sort((a, b) => b.created_at.localeCompare(a.created_at))[0]?.plan_name ?? null

// ---------------------------------------------------------------------------
// 审计日志（adminops.AuditRow；按时间倒序存）
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
}
const audit: AuditRow[] = []

function record(row: Omit<AuditRow, 'id' | 'occurred_at'> & { at?: string }): AuditRow {
  const { at, ...rest } = row
  const r: AuditRow = { id: randomUUID(), occurred_at: at ?? new Date().toISOString(), ...rest }
  // 新行放最前再做稳定排序：同一毫秒写的两条，后写的仍排在前面
  audit.unshift(r)
  audit.sort((a, b) => b.occurred_at.localeCompare(a.occurred_at))
  return r
}

const STAFF: ReadonlyArray<readonly [string, string]> = [
  ['linzhou@pandora.dev', '10.8.0.12'],
  ['zhou.min@pandora.dev', '10.8.0.31'],
  ['tang.yiming@pandora.dev', '10.8.0.44'],
]
const BROWSER = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 14_6) AppleWebKit/605.1.15 Safari/605.1.15'
const PUBLIC_IPS = ['112.64.28.201', '39.144.8.21', '117.136.2.40', '61.216.4.19', '223.104.6.9']

// 种子：14 天，约 140 条；10 天以前的是 00080 之前的存量行（没有 auth_context 与来源 IP）
;(function seedAudit() {
  for (let k = 0; k < 140; k++) {
    const ago = (k * 137 + 3) * MIN
    const legacy = ago > 10 * DAY
    const [email, staffIp] = STAFF[k % STAFF.length]!
    const u = userAt(k * 5 + 2)
    const pub = PUBLIC_IPS[k % PUBLIC_IPS.length]!
    const staff = { actor_kind: 'admin', actor_id: randomUUID(), actor_email: email, api_domain: 'admin', source_ip: legacy ? null : staffIp, user_agent: BROWSER }
    const user = { actor_kind: 'user', actor_id: u.id, actor_email: u.email, api_domain: 'public', source_ip: legacy ? null : pub, user_agent: BROWSER }
    const base = { resource_type: null, resource_id: null, resource_label: null, outcome: 'success', reason: null, auth_context: null as AuditRow['auth_context'] }
    const auth = (c: 'session' | 'reauth') => (legacy ? null : c)
    const rows: Array<Omit<AuditRow, 'id' | 'occurred_at'>> = [
      { ...base, ...staff, action: 'user.balance.adjust', resource_type: 'user', resource_id: u.id, resource_label: u.email, reason: '线路故障补偿', auth_context: auth('reauth') },
      { ...base, actor_kind: 'system', actor_id: null, actor_email: null, api_domain: null, source_ip: null, user_agent: null, action: 'order.expired', resource_type: 'order', resource_id: randomUUID(), resource_label: `PD${2609 + (k % 20)}-${3300 + k}` },
      { ...base, ...user, action: 'user.login', auth_context: null },
      { ...base, ...staff, action: 'ticket.assign', resource_type: 'ticket', resource_id: randomUUID(), resource_label: `TK20260923-${(1000 + k).toString(36).toUpperCase()}`, auth_context: auth('session') },
      { ...base, ...staff, action: 'node.update', resource_type: 'node', resource_id: randomUUID(), resource_label: `香港 0${(k % 4) + 1}`, auth_context: auth('session') },
      { ...base, actor_kind: 'anonymous', actor_id: null, actor_email: null, api_domain: 'public', source_ip: legacy ? null : '223.104.63.18', user_agent: 'python-requests/2.31', action: 'user.login', outcome: 'failure' },
      { ...base, ...staff, action: 'gift_card.batch.export', resource_type: 'gift_card_batch', resource_id: randomUUID(), auth_context: auth('reauth') },
      { ...base, ...user, action: 'user.registered', resource_type: 'user', resource_id: u.id, resource_label: u.email },
      { ...base, ...staff, action: 'plan.version.publish', resource_type: 'plan', resource_id: randomUUID(), resource_label: '专业版', auth_context: auth('reauth') },
      { ...base, actor_kind: 'system', actor_id: null, actor_email: null, api_domain: null, source_ip: null, user_agent: null, action: 'payment.succeeded', resource_type: 'order', resource_id: randomUUID(), resource_label: `PD2609-${3400 + k}` },
      { ...base, actor_kind: 'agent', actor_id: randomUUID(), actor_email: 'support@pandora.dev', api_domain: 'admin', source_ip: legacy ? null : '10.8.0.52', user_agent: BROWSER, action: 'ticket.reply', resource_type: 'ticket', resource_id: randomUUID(), resource_label: `TK20260922-${(2000 + k).toString(36).toUpperCase()}`, auth_context: auth('session') },
      { ...base, ...staff, action: 'payment_provider.toggle', resource_type: 'payment_provider', auth_context: auth('reauth'), outcome: k % 4 === 0 ? 'denied' : 'success', reason: '渠道维护' },
      { ...base, ...user, action: 'user.password_changed', resource_type: 'user', resource_id: u.id, resource_label: u.email, auth_context: auth('session') },
      { ...base, actor_kind: 'node', actor_id: null, actor_email: null, api_domain: 'node', source_ip: legacy ? null : '149.28.72.190', user_agent: 'pdnd/1.4.2', action: 'node.enroll', resource_type: 'node', resource_id: randomUUID(), resource_label: `us-lax-edge-${(k % 3) + 1}`, outcome: k % 5 === 0 ? 'failure' : 'success' },
      { ...base, actor_kind: 'plugin', actor_id: null, actor_email: null, api_domain: null, source_ip: null, user_agent: null, action: 'plugin.hook.deliver', resource_type: 'plugin_hook', outcome: k % 3 === 0 ? 'failure' : 'success' },
      { ...base, ...staff, action: 'withdrawal.approve', resource_type: 'withdrawal', resource_id: randomUUID(), auth_context: auth('reauth'), outcome: k % 7 === 0 ? 'partial' : 'success' },
    ]
    record({ ...rows[k % rows.length]!, at: iso(ago) })
  }
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
  resource_label: r.resource_label,
  auth_context: r.auth_context,
  source_ip: r.source_ip,
})

interface AuditQuery {
  action: string
  actorKind: string
  outcome: string
  q: string
  from: number | null
  to: number | null
}
function auditFilter(q: URLSearchParams): AuditQuery {
  return { action: q.get('action') ?? '', actorKind: q.get('actor_kind') ?? '', outcome: q.get('outcome') ?? '', q: (q.get('q') ?? '').trim(), from: null, to: null }
}
function auditMatch(r: AuditRow, f: AuditQuery): boolean {
  const q = f.q.toLowerCase()
  const t = Date.parse(r.occurred_at)
  return (
    (!f.action || r.action.startsWith(f.action)) &&
    (!f.actorKind || r.actor_kind === f.actorKind) &&
    (!f.outcome || r.outcome === f.outcome) &&
    (!q || r.action.toLowerCase().includes(q) || (r.actor_email ?? '').toLowerCase().includes(q) || r.resource_id === f.q) &&
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
}
const fetches: Fetch[] = []
const CLIENTS = ['clash-verge/2.0.4', 'sing-box/1.10.1', 'Shadowrocket/2.2.53', 'v2rayN/7.1']
;(function seedFetches() {
  for (let k = 0; k < 60; k++) {
    const scan = k % 11 === 5
    fetches.push({
      at: iso((k * 53 + 1) * MIN),
      user: scan ? null : userAt(k * 3 + 1),
      ip: scan ? '149.28.72.190' : PUBLIC_IPS[(k * 2) % PUBLIC_IPS.length]!,
      ua: scan ? 'curl/8.4.0' : CLIENTS[k % CLIENTS.length]!,
      result: scan ? 'not_found' : k % 17 === 8 ? 'rate_limited' : k % 19 === 3 ? 'expired' : 'ok',
    })
  }
})()

/** 实时尾随要看得到新行：每次读按流逝时间补事件，约 6 秒一条，一次最多 3 条 */
let lastTick = Date.now()
function tick() {
  const due = Math.min(3, Math.floor((Date.now() - lastTick) / 6000))
  for (let i = 0; i < due; i++) {
    const n = audit.length + fetches.length
    const u = userAt(n * 7)
    const ip = PUBLIC_IPS[n % PUBLIC_IPS.length]!
    const at = new Date(Date.now() - (due - 1 - i) * 1000).toISOString()
    if (n % 3 === 0) record({ at, actor_kind: 'user', actor_id: u.id, actor_email: u.email, action: 'user.login', resource_type: null, resource_id: null, resource_label: null, api_domain: 'public', outcome: n % 4 === 0 ? 'failure' : 'success', reason: null, auth_context: null, source_ip: ip, user_agent: BROWSER })
    else fetches.unshift({ at, user: u, ip, ua: CLIENTS[n % CLIENTS.length]!, result: 'ok' })
  }
  if (due > 0) lastTick = Date.now()
}

/** accessCategoryRules：先命中者胜，admin 排在 payment 前面 */
const CATEGORY_RULES: ReadonlyArray<readonly [string, readonly string[]]> = [
  ['login', ['user.login']],
  ['register', ['user.registered']],
  ['reset_password', ['user.password', 'user.reset']],
  ['order', ['order.']],
  ['admin', ['node.', 'adminctl.', 'server.', 'plan.', 'payment_provider.']],
  ['payment', ['payment']],
  ['ticket', ['ticket.']],
]
const categoryOf = (action: string) => CATEGORY_RULES.find(([, ps]) => ps.some((p) => action.startsWith(p)))?.[0] ?? 'other'
const KNOWN_CATEGORIES = new Set([...CATEGORY_RULES.map(([c]) => c), 'other', 'subscribe'])

type AccessItem = Record<string, string>
/** omitempty：空串的键不出现 */
const compact = (o: Record<string, string>): AccessItem => Object.fromEntries(Object.entries(o).filter(([, v]) => v !== ''))

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
  const byId = UUID.test(who)
  const userOk = (id: string | null, email: string | null) => !who || (byId ? id === who.toLowerCase() : (email ?? '').toLowerCase().includes(who.toLowerCase()))

  const items: Array<AccessItem & { occurred_at: string }> = []
  if (category !== 'subscribe') {
    for (const r of audit) {
      if (category && categoryOf(r.action) !== category) continue
      if (ip && r.source_ip !== ip) continue
      if (!userOk(r.actor_id, r.actor_email)) continue
      if (outcome && !(outcome === 'error' ? r.outcome !== 'success' : r.outcome === outcome)) continue
      const g = geoOf(r.source_ip ?? '')
      items.push({ ...compact({ category: categoryOf(r.action), action: r.action, user_id: r.actor_id ?? '', user_email: r.actor_email ?? '', ip: r.source_ip ?? '', geo: g.geo, network_kind: g.kind, user_agent: r.user_agent ?? '', outcome: r.outcome }), occurred_at: r.occurred_at })
    }
  }
  if ((category === '' || category === 'subscribe') && ['', 'success', 'error'].includes(outcome)) {
    for (const f of fetches) {
      if (ip && f.ip !== ip) continue
      if (!userOk(f.user?.id ?? null, f.user?.email ?? null)) continue
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
  ip: string
  members: string[]
  events: number
  first: string
  last: string
  review: Review | null
}
const NORMAL_TTL = 30 * DAY
const hashKey = (seed: string) => createHash('sha256').update(seed).digest('hex')
const cluster = (ip: string, idx: number[], lastMin: number, review: Review | null = null): MockCluster => ({
  key: hashKey(ip || `legacy-${idx.join(',')}`),
  ip,
  members: idx.map((i) => userAt(i).id),
  events: idx.length * 7 + 3,
  first: iso(80 * DAY - idx[0]! * DAY),
  last: iso(lastMin * MIN),
  review,
})
// 成员挑的是真实种子：0 号是后台账号（roles 非空，停用时跳过），9 号已停用，24 / 39 已停用、40 已封禁
const clusters: MockCluster[] = [
  cluster('223.104.63.18', [3, 9, 13, 18, 23], 6),
  cluster('45.76.9.102', [5, 25, 45], 20),
  cluster('117.136.2.40', [0, 7, 27], 45),
  cluster('112.64.28.201', [1, 21], 60),
  cluster('61.216.4.19', [15, 35], 180, { decision: 'normal', decided_at: iso(32 * DAY), expires_at: iso(2 * DAY) }),
  cluster('202.120.1.9', [11, 31], 300, { decision: 'normal', decided_at: iso(10 * DAY), expires_at: iso(-20 * DAY) }),
  cluster('39.144.8.21', [24, 39, 40], 900, { decision: 'disabled', decided_at: iso(3 * DAY), expires_at: null }),
  cluster('', [19, 29], 2000),
]

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
const clusterBy = (key: string) => (/^([0-9a-f]{2})+$/i.test(key.trim()) ? clusters.find((c) => c.key === key.trim().toLowerCase()) : undefined)

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
    else if (u.roles.length > 0) skipped.push({ user_id: id, reason: 'administrator' })
    else if (u.status === 'suspended' || u.status === 'banned') skipped.push({ user_id: id, reason: 'already_disabled' })
    else done.push(u)
  }
  const actor = { actor_kind: 'admin', actor_id: ctx.user.userId, actor_email: ctx.user.email, api_domain: 'admin', auth_context: 'reauth' as const, source_ip: '10.8.0.12', user_agent: BROWSER }
  for (const u of done) {
    u.status = 'suspended'
    record({ ...actor, action: 'user.status_change', resource_type: 'user', resource_id: u.id, resource_label: u.email, outcome: 'success', reason })
  }
  if (done.length > 0) c.review = { decision: 'disabled', decided_at: new Date().toISOString(), expires_at: null }
  record({ ...actor, action: 'risk.ip_cluster.disable', resource_type: done.length ? 'ip_cluster_review' : null, resource_id: null, resource_label: null, outcome: done.length === 0 ? 'failure' : skipped.length ? 'partial' : 'success', reason })
  return { status: 200, body: { disabled: done.length, skipped } }
}

// ---------------------------------------------------------------------------
// 降级开关（feature_switches：00010 七行 + 00085 四行，R102 的新迁移删去 ops.bulk_export / ops.reports / node.autoscale）
// ---------------------------------------------------------------------------
interface Switch {
  code: string
  enabled: boolean
  essential: boolean
  reason: string | null
}
const switches: Switch[] = [
  { code: 'auth.login', enabled: true, essential: true, reason: null },
  { code: 'subscription.renewal', enabled: true, essential: true, reason: null },
  { code: 'client.config_sync', enabled: true, essential: true, reason: null },
  { code: 'auth.registration', enabled: true, essential: false, reason: null },
  { code: 'billing.checkout', enabled: true, essential: false, reason: null },
  { code: 'marketing.giftcard.redeem', enabled: true, essential: false, reason: null },
  { code: 'notify.email', enabled: true, essential: false, reason: null },
  { code: 'admin.writes', enabled: true, essential: false, reason: null },
]
/** admin.writes 缺行视为开启（R58）；外壳 mock-api.ts 的只读门按它把非豁免写请求回 503 */
export const adminWritesEnabled = () => switches.find((s) => s.code === 'admin.writes')?.enabled ?? true

/** 切换成功后通知外壳，外壳向后台所有 SSE 连接推 switches.changed { code, enabled }（只发管理端，与 Go 一致） */
const switchListeners = new Set<(payload: { code: string; enabled: boolean }) => void>()
export function onSwitchChanged(listener: (payload: { code: string; enabled: boolean }) => void): () => void {
  switchListeners.add(listener)
  return () => switchListeners.delete(listener)
}

const CHECK = (name: string) => `new row for relation "feature_switches" violates check constraint "${name}"`

async function setSwitch(ctx: MockContext): Promise<MockResult> {
  const d = await decode(ctx, { enabled: 'boolean', reason: 'string' })
  if (!d.ok) return d.result
  const s = switches.find((x) => x.code === ctx.params.code)
  if (!s) return NOT_FOUND
  const enabled = d.body.enabled === true
  const reason = typeof d.body.reason === 'string' ? d.body.reason : ''
  const stored = reason.trim() ? reason : null
  // 两条都是数据库 CHECK，Go 翻成 409 并带上数据库原文
  if (s.essential && !enabled) return err(409, 'conflict', `开关 ${s.code} 不允许该操作：${CHECK('feature_switches_essential_stays_on')}`)
  if (!enabled && stored === null) return err(409, 'conflict', `开关 ${s.code} 不允许该操作：${CHECK('feature_switches_disable_needs_reason')}`)
  s.enabled = enabled
  s.reason = stored
  record({ actor_kind: 'admin', actor_id: ctx.user.userId, actor_email: ctx.user.email, action: 'feature_switch.toggle', resource_type: 'feature_switch', resource_id: null, resource_label: null, api_domain: 'admin', outcome: 'success', reason: reason || null, auth_context: 'reauth', source_ip: '10.8.0.12', user_agent: BROWSER })
  switchListeners.forEach((fn) => fn({ code: s.code, enabled }))
  return { status: 200, body: { ok: true, enabled } }
}

// ---------------------------------------------------------------------------
// 路由表
// ---------------------------------------------------------------------------
export const security: MockModule = {
  routes: {
    'GET /v1/audit': (ctx) => {
      if (!ctx.requirePermission('security.audit.read')) return
      tick()
      let limit = atoi(ctx.query.get('limit'), 50)
      if (limit <= 0 || limit > 200) limit = 50
      const offset = Math.max(0, atoi(ctx.query.get('offset'), 0))
      const rows = audit.filter((r) => auditMatch(r, auditFilter(ctx.query)))
      ctx.send(200, { events: rows.slice(offset, offset + limit).map(auditJson), total: rows.length })
    },
    'GET /v1/audit/export': (ctx) => {
      if (!ctx.requirePermission('security.audit.read') || !ctx.requirePermission('ops.export') || !ctx.requireReauth()) return
      const range = exportRange(ctx.query)
      if ('status' in range) return reply(ctx, range)
      const f = { ...auditFilter(ctx.query), ...range }
      const rows = audit.filter((r) => auditMatch(r, f))
      if (rows.length > 50_000) return reply(ctx, err(422, 'validation_failed', '超过 50000 行，请缩小时间范围'))
      const s = (v: string | null) => v ?? ''
      const lines = [
        'occurred_at,actor_kind,actor_email,action,resource_type,resource_id,resource_label,api_domain,outcome,reason,source_ip,auth_context',
        ...rows.map((r) =>
          [r.occurred_at.replace(/\.\d+Z$/, 'Z'), r.actor_kind, csvSafe(s(r.actor_email)), r.action, s(r.resource_type), s(r.resource_id), csvSafe(s(r.resource_label)), s(r.api_domain), r.outcome, csvSafe(s(r.reason)), s(r.source_ip), s(r.auth_context)].map(csvCell).join(','),
        ),
      ]
      record({ actor_kind: 'admin', actor_id: ctx.user.userId, actor_email: ctx.user.email, action: 'audit.export', resource_type: null, resource_id: null, resource_label: null, api_domain: 'admin', outcome: 'success', reason: null, auth_context: 'reauth', source_ip: '10.8.0.12', user_agent: BROWSER })
      const stamp = new Date().toISOString().replace(/[-:]/g, '').replace('T', '-').slice(0, 15)
      ctx.sendRaw(200, { contentType: 'text/csv; charset=utf-8', text: `\ufeff${lines.join('\n')}\n`, headers: { 'Content-Disposition': `attachment; filename="audit-${stamp}.csv"` } })
    },

    'GET /v1/access-log': (ctx) => {
      if (!ctx.requirePermission('security.audit.read')) return
      tick()
      reply(ctx, accessList(ctx))
    },

    'GET /v1/ip-clusters': (ctx) => {
      if (!ctx.requirePermission('security.audit.read')) return
      const all = ctx.query.get('include_reviewed') === '1'
      const now = Date.now()
      const rows = clusters
        .filter((c) => all || c.review?.decision !== 'normal' || (c.review.expires_at !== null && Date.parse(c.review.expires_at) <= now))
        .sort((a, b) => b.members.length - a.members.length || b.last.localeCompare(a.last))
        .slice(0, 50)
        .map(clusterJson)
      ctx.send(200, { clusters: rows })
    },
    'POST /v1/ip-clusters/:key/review': async (ctx) => {
      if (!ctx.requirePermission('security.risk.review')) return
      const d = await decode(ctx, { note: 'string' })
      if (!d.ok) return reply(ctx, d.result)
      if (!/^([0-9a-f]{2})+$/i.test(ctx.params.key!)) return reply(ctx, NOT_FOUND)
      const note = String(d.body.note ?? '').trim()
      if (runes(note) > 500) return reply(ctx, invalid({ note: '备注不能超过 500 字' }))
      const c = clusterBy(ctx.params.key!)
      if (!c) return reply(ctx, NOT_FOUND)
      const expires = new Date(Date.now() + NORMAL_TTL).toISOString()
      c.review = { decision: 'normal', decided_at: new Date().toISOString(), expires_at: expires }
      record({ actor_kind: 'admin', actor_id: ctx.user.userId, actor_email: ctx.user.email, action: 'risk.ip_cluster.mark_normal', resource_type: 'ip_cluster_review', resource_id: null, resource_label: null, api_domain: 'admin', outcome: 'success', reason: null, auth_context: ctx.reauthed ? 'reauth' : 'session', source_ip: '10.8.0.12', user_agent: BROWSER })
      ctx.send(200, { key: ctx.params.key, decision: 'normal', expires_at: expires.replace(/\.\d+Z$/, 'Z') })
    },
    'POST /v1/ip-clusters/:key/disable-accounts': async (ctx) => {
      if (!ctx.requirePermission('security.risk.review') || !ctx.requirePermission('iam.user.write') || !ctx.requireReauth()) return
      await ctx.idempotent('ip_cluster_disable', () => disableAccounts(ctx))
    },

    'GET /v1/switches': (ctx) => {
      if (!ctx.requirePermission('security.audit.read')) return
      const rows = [...switches].sort((a, b) => Number(b.essential) - Number(a.essential) || a.code.localeCompare(b.code))
      ctx.send(200, { switches: rows })
    },
    'POST /v1/switches/:code': async (ctx) => {
      if (!ctx.requirePermission('platform.settings.write') || !ctx.requireReauth()) return
      reply(ctx, await setSwitch(ctx))
    },
  },
}
