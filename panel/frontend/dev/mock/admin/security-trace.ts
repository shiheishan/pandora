import { Buffer } from 'node:buffer'
import type { MockContext, MockResult, MockRoute } from '../types.ts'

// 溯源总览（identity/service/trace.go）的假后端：每次读时由 security.ts 传进来的审计行与订阅拉取日志现算，
// 口径照 trace.go / trace_merge.go 与 audit、subscription 两路的 trace_reads.go；与 Go 的不同处写在 .claude/rules/frontend-mock.md。

export interface TraceAuditEvent {
  occurred_at: string
  actor_kind: string
  actor_id: string | null
  action: string
  outcome: string
  source_ip: string | null
  ip_hash: string | null
  user_agent: string | null
}
export interface TraceFetchEvent {
  at: string
  user: { id: string } | null
  ip: string
  ua: string
  result: string
}
export interface TraceUserInfo {
  id: string
  email: string
  status: string
}
export interface TraceDeps {
  audit: readonly TraceAuditEvent[]
  fetches: readonly TraceFetchEvent[]
  geoOf: (ip: string) => { geo: string; kind: string }
  uaFamily: (ua: string) => string
  users: () => readonly TraceUserInfo[]
  onReveal: (ctx: MockContext) => void
}

const RANK_LIMIT = 100
const IP_ACCOUNTS = 20
const USER_IPS = 100
const REVEAL_TTL_MS = 2 * 3_600_000
const TRACE_WINDOWS = new Set(['24h', '7d', '30d'])
const TRACE_SUB_SORTS = new Set(['fetches', 'ips', 'ua_kinds', 'failures', 'last'])
const TRACE_ACC_SORTS = new Set(['linked_ips', 'logins', 'login_ips', 'fetch_ips', 'shared_ips', 'ua_kinds', 'datacenter_hits'])

const err = (status: number, code: string, message: string, fields?: Record<string, string>): MockResult => ({
  status,
  body: { error: { code, message, ...(fields ? { fields } : {}) } },
})
const invalid = (fields: Record<string, string>) => err(422, 'validation_failed', '请求参数校验未通过', fields)
const reply = (ctx: MockContext, r: MockResult) => ctx.send(r.status, r.body)

/** 规整明文 IP 作合并键（照 Go normTraceIP，mock 不做 netip 解析） */
function normIP(ip: string): string {
  return ip.trim()
}

/** 地区：模拟 Go Country + Region（归属地至少三段时取前两段） */
function regionOf(geo: string): string {
  const g = geo.trim()
  if (!g) return '未知'
  const parts = g.split(/\s+/)
  if (parts.length >= 3) return `${parts[0]} ${parts[1]}`
  return g
}

/** 与 identity/service/trace.go 的 maskIP 同口径 */
function traceMaskIp(ip: string): string {
  let s = ip.trim()
  if (!s) return ''
  if (s.toLowerCase().startsWith('::ffff:')) s = s.slice(7)
  if (s.includes('.') && /^\d+\.\d+\.\d+\.\d+$/.test(s)) {
    const [a, b] = s.split('.')
    return `${a}.${b}.*.*`
  }
  if (s.includes(':')) {
    const parts = s.split(':')
    if (parts.length >= 2) return `${parts[0]}:${parts[1]}:*`
  }
  return '*'
}

function parseWindow(q: URLSearchParams): { window: string; since: string; sinceMs: number } | MockResult {
  const window = (q.get('window') ?? '').trim() || '24h'
  if (!TRACE_WINDOWS.has(window)) return invalid({ window: '只支持 24h、7d 或 30d' })
  const h = window === '7d' ? 7 * 24 : window === '30d' ? 30 * 24 : 24
  const sinceMs = Date.now() - h * 3_600_000
  return { window, since: new Date(sinceMs).toISOString(), sinceMs }
}

function inWindow(iso: string, sinceMs: number): boolean {
  return Date.parse(iso) > sinceMs
}

function auditTraceRows(deps: TraceDeps, sinceMs: number): TraceAuditEvent[] {
  return deps.audit.filter(
    (r) =>
      r.ip_hash !== null &&
      (r.actor_kind === 'user' || r.actor_kind === 'anonymous') &&
      inWindow(r.occurred_at, sinceMs),
  )
}

interface AuditIPBucket {
  events: number
  ua: Set<string>
  accountIds: string[]
  accounts: number
  last: string
  ip: string
}

/** 审计一路按 ip_hash 聚合（照 audit trace_reads） */
function auditByIPHash(rows: TraceAuditEvent[]): Map<string, AuditIPBucket> {
  const map = new Map<string, AuditIPBucket>()
  for (const r of rows) {
    const hash = r.ip_hash!
    let g = map.get(hash)
    if (!g) {
      g = { events: 0, ua: new Set(), accountIds: [], accounts: 0, last: r.occurred_at, ip: '' }
      map.set(hash, g)
    }
    g.events++
    if (r.user_agent?.trim()) g.ua.add(r.user_agent)
    if (r.occurred_at > g.last) g.last = r.occurred_at
    if (r.source_ip?.trim() && !g.ip) g.ip = r.source_ip.trim()
  }
  for (const [hash, g] of map) {
    const ids = new Set<string>()
    for (const r of rows) {
      if (r.ip_hash !== hash || r.actor_kind !== 'user' || !r.actor_id) continue
      ids.add(r.actor_id)
    }
    const sorted = [...ids].sort()
    g.accounts = sorted.length
    g.accountIds = sorted.slice(0, IP_ACCOUNTS)
    if (!g.ip) {
      for (const r of rows) {
        if (r.ip_hash === hash && r.source_ip?.trim()) {
          g.ip = r.source_ip.trim()
          break
        }
      }
    }
    map.set(hash, g)
  }
  return map
}

interface FetchIPBucket {
  fetches: number
  ua: Set<string>
  accountIds: string[]
  accounts: number
  last: string
}

/** 拉取一路按 IP 聚合 */
function fetchByIP(deps: TraceDeps, sinceMs: number): Map<string, FetchIPBucket> {
  const map = new Map<string, FetchIPBucket>()
  for (const f of deps.fetches) {
    if (!inWindow(f.at, sinceMs)) continue
    const ip = normIP(f.ip)
    if (!ip) continue
    let g = map.get(ip)
    if (!g) {
      g = { fetches: 0, ua: new Set(), accountIds: [], accounts: 0, last: f.at }
      map.set(ip, g)
    }
    g.fetches++
    if (f.ua.trim()) g.ua.add(f.ua)
    if (f.at > g.last) g.last = f.at
  }
  for (const [ip, g] of map) {
    const ids = new Set<string>()
    for (const f of deps.fetches) {
      if (!inWindow(f.at, sinceMs) || normIP(f.ip) !== ip || !f.user?.id) continue
      ids.add(f.user.id)
    }
    const sorted = [...ids].sort()
    g.accounts = sorted.length
    g.accountIds = sorted.slice(0, IP_ACCOUNTS)
    map.set(ip, g)
  }
  return map
}

interface MergedIP {
  ip: string
  events: number
  fetches: number
  auditAccounts: number
  fetchAccounts: number
  auditUA: number
  fetchUA: number
  ids: Set<string>
  last: string
}

/** 按明文 IP 合并两路（照 mergeTraceIPs） */
function mergeTraceIPs(auditBuckets: Map<string, AuditIPBucket>, fetchBuckets: Map<string, FetchIPBucket>): Map<string, MergedIP> {
  const merged = new Map<string, MergedIP>()
  const seenAudit = new Set<string>()
  for (const g of auditBuckets.values()) {
    const key = normIP(g.ip)
    if (!key || seenAudit.has(key)) continue
    seenAudit.add(key)
    let m = merged.get(key)
    if (!m) {
      m = { ip: key, events: 0, fetches: 0, auditAccounts: 0, fetchAccounts: 0, auditUA: 0, fetchUA: 0, ids: new Set(), last: g.last }
      merged.set(key, m)
    }
    m.events = g.events
    m.auditAccounts = g.accounts
    m.auditUA = g.ua.size
    for (const id of g.accountIds) m.ids.add(id)
    if (g.last > m.last) m.last = g.last
  }
  const seenFetch = new Set<string>()
  for (const [ip, g] of fetchBuckets) {
    const key = normIP(ip)
    if (!key || seenFetch.has(key)) continue
    seenFetch.add(key)
    let m = merged.get(key)
    if (!m) {
      m = { ip: key, events: 0, fetches: 0, auditAccounts: 0, fetchAccounts: 0, auditUA: 0, fetchUA: 0, ids: new Set(), last: g.last }
      merged.set(key, m)
    }
    m.fetches = g.fetches
    m.fetchAccounts = g.accounts
    m.fetchUA = g.ua.size
    for (const id of g.accountIds) m.ids.add(id)
    if (g.last > m.last) m.last = g.last
  }
  return merged
}

/** traceAccountsOf */
function traceAccountsOf(g: MergedIP): { accounts: number; capped: boolean } {
  const union = g.ids.size
  const capped = g.auditAccounts > IP_ACCOUNTS || g.fetchAccounts > IP_ACCOUNTS
  if (capped) return { accounts: Math.max(union, g.auditAccounts, g.fetchAccounts), capped: true }
  return { accounts: union, capped: false }
}

function traceTokenFor(ip: string, adminId: string): string {
  if (!normIP(ip)) return ''
  const sec = Math.floor(Date.now() / 1000)
  return `mock-${Buffer.from(`${sec}|${adminId}|${ip}`, 'utf8').toString('base64url')}`
}

function openTraceToken(token: string, adminId: string): string | null {
  const t = token.trim()
  if (!t.startsWith('mock-')) return null
  try {
    const raw = Buffer.from(t.slice(5), 'base64url').toString('utf8')
    const parts = raw.split('|')
    if (parts.length !== 3) return null
    const [secStr, issuer, ip = ''] = parts
    if (issuer !== adminId) return null
    if (!normIP(ip)) return null
    const sec = Number(secStr)
    if (!Number.isFinite(sec)) return null
    const issued = sec * 1000
    const now = Date.now()
    if (issued > now + 60_000) return null
    if (now - issued > REVEAL_TTL_MS) return null
    return ip
  } catch {
    return null
  }
}

function fetchWindowTotals(deps: TraceDeps, sinceMs: number) {
  let fetches = 0
  let failures = 0
  for (const f of deps.fetches) {
    if (!inWindow(f.at, sinceMs)) continue
    fetches++
    if (f.result !== 'ok') failures++
  }
  return { fetches, failures }
}

function auditWindowTotals(rows: TraceAuditEvent[]) {
  let logins = 0
  let loginFailures = 0
  for (const r of rows) {
    if (r.action === 'user.login' && r.outcome === 'success') logins++
    if (r.action === 'user.login_failed') loginFailures++
  }
  return { logins, loginFailures }
}

/** traceOverview */
function traceOverview(
  merged: Map<string, MergedIP>,
  auditTotals: ReturnType<typeof auditWindowTotals>,
  fetchTotals: ReturnType<typeof fetchWindowTotals>,
  geoOf: TraceDeps['geoOf'],
) {
  const regionHits = new Map<string, { ips: number; hits: number }>()
  let datacenter_ips = 0
  for (const g of merged.values()) {
    const hits = g.events + g.fetches
    const { geo, kind } = geoOf(g.ip)
    if (kind === 'datacenter') datacenter_ips++
    const region = regionOf(geo)
    const cur = regionHits.get(region) ?? { ips: 0, hits: 0 }
    cur.ips++
    cur.hits += hits
    regionHits.set(region, cur)
  }
  const top_regions = [...regionHits.entries()]
    .map(([region, v]) => ({ region, ips: v.ips, hits: v.hits }))
    .sort((a, b) => b.hits - a.hits || b.ips - a.ips || a.region.localeCompare(b.region))
    .slice(0, 5)
  const n = merged.size
  return {
    distinct_ips: n,
    distinct_ips_approx: false,
    fetches: fetchTotals.fetches,
    fetch_failures: fetchTotals.failures,
    logins: auditTotals.logins,
    login_failures: auditTotals.loginFailures,
    analyzed_ips: n,
    datacenter_ips,
    top_regions,
  }
}

function rankMergedIPs(merged: Map<string, MergedIP>): MergedIP[] {
  return [...merged.values()]
    .sort((a, b) => {
      const ha = a.events + a.fetches
      const hb = b.events + b.fetches
      if (ha !== hb) return hb - ha
      if (a.last !== b.last) return b.last.localeCompare(a.last)
      return a.ip.localeCompare(b.ip)
    })
    .slice(0, RANK_LIMIT)
}

function traceRowUsers(ids: Set<string>, users: () => readonly TraceUserInfo[]) {
  const out: TraceUserInfo[] = []
  for (const id of ids) {
    const u = users().find((x) => x.id === id)
    if (u) out.push({ id: u.id, email: u.email, status: u.status })
  }
  out.sort((a, b) => a.email.localeCompare(b.email))
  return out
}

function buildIPsResponse(deps: TraceDeps, w: { window: string; since: string; sinceMs: number }, adminId: string) {
  const auditRows = auditTraceRows(deps, w.sinceMs)
  const auditBuckets = auditByIPHash(auditRows)
  const fetchBuckets = fetchByIP(deps, w.sinceMs)
  const merged = mergeTraceIPs(auditBuckets, fetchBuckets)
  const overview = traceOverview(merged, auditWindowTotals(auditRows), fetchWindowTotals(deps, w.sinceMs), deps.geoOf)
  const top = rankMergedIPs(merged)
  const ips = top.map((g) => {
    const { geo, kind } = deps.geoOf(g.ip)
    const { accounts, capped } = traceAccountsOf(g)
    return {
      token: traceTokenFor(g.ip, adminId),
      ip_masked: traceMaskIp(g.ip),
      geo,
      network_kind: kind,
      hits: g.events + g.fetches,
      fetches: g.fetches,
      events: g.events,
      accounts,
      accounts_capped: capped,
      ua_kinds: g.auditUA + g.fetchUA,
      last_seen: g.last,
      users: traceRowUsers(g.ids, deps.users),
    }
  })
  return { window: w.window, since: w.since, overview, ips }
}

interface AuditUserAgg {
  logins: number
  loginIPs: number
  ips: number
  sharedIPs: number
  uaKinds: number
  last: string
  ipList: Array<{ ip: string; count: number; shared: boolean }>
}

/** 审计一路按用户（照 TraceUsers） */
function buildAuditUsers(deps: TraceDeps, sinceMs: number): Map<string, AuditUserAgg> {
  const rows = deps.audit.filter(
    (r) => r.actor_kind === 'user' && r.actor_id !== null && r.ip_hash !== null && inWindow(r.occurred_at, sinceMs),
  )
  const usersOnHash = new Map<string, Set<string>>()
  for (const r of rows) {
    const h = r.ip_hash!
    let s = usersOnHash.get(h)
    if (!s) {
      s = new Set()
      usersOnHash.set(h, s)
    }
    s.add(r.actor_id!)
  }
  const sharedHash = new Set<string>()
  for (const [h, s] of usersOnHash) if (s.size > 1) sharedHash.add(h)

  type PairKey = string
  const pairKey = (uid: string, hash: string) => `${uid}\0${hash}`
  const pairs = new Map<PairKey, { n: number; logins: number; last: string; ip: string }>()
  for (const r of rows) {
    const uid = r.actor_id!
    const hash = r.ip_hash!
    const k = pairKey(uid, hash)
    let p = pairs.get(k)
    if (!p) {
      p = { n: 0, logins: 0, last: r.occurred_at, ip: '' }
      pairs.set(k, p)
    }
    p.n++
    if (r.action === 'user.login' && r.outcome === 'success') p.logins++
    if (r.occurred_at > p.last) p.last = r.occurred_at
    if (r.source_ip?.trim()) p.ip = r.source_ip.trim()
  }

  const byUser = new Map<string, AuditUserAgg>()
  const uaByUser = new Map<string, Set<string>>()
  for (const r of rows) {
    const uid = r.actor_id!
    if (r.user_agent?.trim()) {
      let s = uaByUser.get(uid)
      if (!s) {
        s = new Set()
        uaByUser.set(uid, s)
      }
      s.add(r.user_agent)
    }
  }

  for (const [k, p] of pairs) {
    const uid = k.split('\0')[0]!
    let u = byUser.get(uid)
    if (!u) {
      u = { logins: 0, loginIPs: 0, ips: 0, sharedIPs: 0, uaKinds: 0, last: p.last, ipList: [] }
      byUser.set(uid, u)
    }
    u.logins += p.logins
    if (p.last > u.last) u.last = p.last
  }

  for (const [uid, u] of byUser) {
    const hashes = new Set<string>()
    let loginIpCount = 0
    for (const [k, p] of pairs) {
      const [id, hash] = k.split('\0')
      if (id !== uid) continue
      hashes.add(hash!)
      if (p.logins > 0) loginIpCount++
    }
    u.ips = hashes.size
    u.loginIPs = loginIpCount
    u.sharedIPs = [...hashes].filter((h) => sharedHash.has(h)).length
    u.uaKinds = uaByUser.get(uid)?.size ?? 0
    const listWithLast: Array<{ ip: string; count: number; shared: boolean; last: string }> = []
    for (const [k, p] of pairs) {
      const [id, hash] = k.split('\0')
      if (id !== uid) continue
      listWithLast.push({ ip: p.ip, count: p.n, shared: sharedHash.has(hash!), last: p.last })
    }
    u.ipList = listWithLast
      .sort((a, b) => b.count - a.count || b.last.localeCompare(a.last))
      .slice(0, USER_IPS)
      .map(({ ip, count, shared }) => ({ ip, count, shared }))
  }
  return byUser
}

interface FetchUserAgg {
  fetches: number
  failures: number
  ips: number
  uaKinds: number
  families: string[]
  sharedIPs: number
  last: string
  ipList: Array<{ ip: string; count: number; shared: boolean }>
}

/** 拉取一路按用户（照 TraceFetchUsers） */
function buildFetchUsers(deps: TraceDeps, sinceMs: number): Map<string, FetchUserAgg> {
  const rows = deps.fetches.filter((f) => f.user?.id && inWindow(f.at, sinceMs))
  const usersOnIP = new Map<string, Set<string>>()
  for (const f of rows) {
    const ip = normIP(f.ip)
    if (!ip) continue
    let s = usersOnIP.get(ip)
    if (!s) {
      s = new Set()
      usersOnIP.set(ip, s)
    }
    s.add(f.user!.id)
  }
  const sharedIP = new Set<string>()
  for (const [ip, s] of usersOnIP) if (s.size > 1) sharedIP.add(ip)

  const byUser = new Map<string, FetchUserAgg>()
  const uaSet = new Map<string, Set<string>>()
  const famSet = new Map<string, Set<string>>()
  const ipAgg = new Map<string, Map<string, { count: number; last: string }>>()

  for (const f of rows) {
    const uid = f.user!.id
    let u = byUser.get(uid)
    if (!u) {
      u = { fetches: 0, failures: 0, ips: 0, uaKinds: 0, families: [], sharedIPs: 0, last: f.at, ipList: [] }
      byUser.set(uid, u)
    }
    u.fetches++
    if (f.result !== 'ok') u.failures++
    if (f.at > u.last) u.last = f.at
    if (f.ua.trim()) {
      let s = uaSet.get(uid)
      if (!s) {
        s = new Set()
        uaSet.set(uid, s)
      }
      s.add(f.ua)
      let fs = famSet.get(uid)
      if (!fs) {
        fs = new Set()
        famSet.set(uid, fs)
      }
      fs.add(deps.uaFamily(f.ua))
    }
    const ip = normIP(f.ip)
    if (!ip) continue
    let per = ipAgg.get(uid)
    if (!per) {
      per = new Map()
      ipAgg.set(uid, per)
    }
    let g = per.get(ip)
    if (!g) {
      g = { count: 0, last: f.at }
      per.set(ip, g)
    }
    g.count++
    if (f.at > g.last) g.last = f.at
  }

  for (const [uid, u] of byUser) {
    const per = ipAgg.get(uid) ?? new Map()
    u.ips = per.size
    u.uaKinds = uaSet.get(uid)?.size ?? 0
    u.families = [...(famSet.get(uid) ?? new Set())].sort()
    u.sharedIPs = [...per.keys()].filter((ip) => sharedIP.has(ip)).length
    u.ipList = [...per.entries()]
      .map(([ip, g]) => ({ ip, count: g.count, shared: sharedIP.has(ip), last: g.last }))
      .sort((a, b) => b.count - a.count || b.last.localeCompare(a.last) || a.ip.localeCompare(b.ip))
      .slice(0, USER_IPS)
      .map(({ ip, count, shared }) => ({ ip, count, shared }))
  }
  return byUser
}

function userRef(deps: TraceDeps, id: string) {
  const u = deps.users().find((x) => x.id === id)
  return { email: u?.email ?? '', status: u?.status ?? '' }
}

function regionCountForFetchUser(deps: TraceDeps, ipList: Array<{ ip: string }>) {
  const s = new Set<string>()
  for (const row of ipList) s.add(regionOf(deps.geoOf(row.ip).geo))
  return s.size
}

function buildSubscribers(deps: TraceDeps, w: { window: string; since: string; sinceMs: number }, sort: string) {
  const users = buildFetchUsers(deps, w.sinceMs)
  const rows = [...users.entries()].map(([user_id, u]) => {
    const ref = userRef(deps, user_id)
    return {
      user_id,
      email: ref.email,
      status: ref.status,
      fetches: u.fetches,
      failures: u.failures,
      ips: u.ips,
      ua_kinds: u.uaKinds,
      regions: regionCountForFetchUser(deps, u.ipList),
      families: u.families,
      last_fetch: u.last,
    }
  })
  const metric = (r: (typeof rows)[number]) =>
    sort === 'ips' ? r.ips : sort === 'ua_kinds' ? r.ua_kinds : sort === 'failures' ? r.failures : r.fetches
  rows.sort((a, b) => {
    const primary = sort === 'last' ? b.last_fetch.localeCompare(a.last_fetch) : metric(b) - metric(a)
    if (primary !== 0) return primary
    const tie = b.last_fetch.localeCompare(a.last_fetch)
    if (tie !== 0) return tie
    return a.user_id.localeCompare(b.user_id)
  })
  return {
    window: w.window,
    since: w.since,
    sort,
    subscribers: rows.slice(0, RANK_LIMIT),
  }
}

function plainIPSet(list: Array<{ ip: string; shared?: boolean }>, onlyShared: boolean) {
  const out = new Set<string>()
  for (const row of list) {
    if (onlyShared && !row.shared) continue
    const ip = normIP(row.ip)
    if (ip) out.add(ip)
  }
  return out
}

function overlapCount(a: Set<string>, b: Set<string>) {
  let n = 0
  for (const k of a) if (b.has(k)) n++
  return n
}

function datacenterHits(deps: TraceDeps, list: Array<{ ip: string; count: number }>) {
  let n = 0
  for (const row of list) {
    const ip = normIP(row.ip)
    if (!ip) continue
    if (deps.geoOf(ip).kind === 'datacenter') n += row.count
  }
  return n
}

function combineOneAccount(
  userId: string,
  a: AuditUserAgg | undefined,
  f: FetchUserAgg | undefined,
  deps: TraceDeps,
) {
  const aList = a?.ipList ?? []
  const fList = f?.ipList ?? []
  const linked = (a?.ips ?? 0) + (f?.ips ?? 0) - overlapCount(plainIPSet(aList, false), plainIPSet(fList, false))
  const shared = (a?.sharedIPs ?? 0) + (f?.sharedIPs ?? 0) - overlapCount(plainIPSet(aList, true), plainIPSet(fList, true))
  let last = a?.last ?? ''
  if (f?.last && f.last > last) last = f.last
  return {
    user_id: userId,
    email: '',
    status: '',
    linked_ips: Math.max(linked, 0),
    logins: a?.logins ?? 0,
    login_ips: a?.loginIPs ?? 0,
    fetch_ips: f?.ips ?? 0,
    shared_ips: Math.max(shared, 0),
    ua_kinds: (a?.uaKinds ?? 0) + (f?.uaKinds ?? 0),
    datacenter_hits: datacenterHits(deps, aList) + datacenterHits(deps, fList),
    last_seen: last,
  }
}

function accountSortPair(sort: string): { auditOnly: boolean; fetchOnly: boolean } {
  switch (sort) {
    case 'logins':
    case 'login_ips':
      return { auditOnly: true, fetchOnly: false }
    case 'fetch_ips':
      return { auditOnly: false, fetchOnly: true }
    default:
      return { auditOnly: false, fetchOnly: false }
  }
}

function accountMetric(row: ReturnType<typeof combineOneAccount>, sort: string): number {
  switch (sort) {
    case 'linked_ips':
      return row.linked_ips
    case 'logins':
      return row.logins
    case 'login_ips':
      return row.login_ips
    case 'fetch_ips':
      return row.fetch_ips
    case 'shared_ips':
      return row.shared_ips
    case 'ua_kinds':
      return row.ua_kinds
    case 'datacenter_hits':
      return row.datacenter_hits
    default:
      return 0
  }
}

function buildAccounts(deps: TraceDeps, w: { window: string; since: string; sinceMs: number }, sort: string) {
  const auditUsers = buildAuditUsers(deps, w.sinceMs)
  const fetchUsers = buildFetchUsers(deps, w.sinceMs)
  const { auditOnly, fetchOnly } = accountSortPair(sort)
  const ids = new Set<string>()
  if (auditOnly) for (const id of auditUsers.keys()) ids.add(id)
  else if (fetchOnly) for (const id of fetchUsers.keys()) ids.add(id)
  else {
    for (const id of auditUsers.keys()) ids.add(id)
    for (const id of fetchUsers.keys()) ids.add(id)
  }
  const rows = [...ids].map((id) => {
    const row = combineOneAccount(id, auditUsers.get(id), fetchUsers.get(id), deps)
    const ref = userRef(deps, id)
    row.email = ref.email
    row.status = ref.status
    return row
  })
  rows.sort((a, b) => {
    const cmp = accountMetric(b, sort) - accountMetric(a, sort)
    if (cmp !== 0) return cmp
    const t = b.last_seen.localeCompare(a.last_seen)
    if (t !== 0) return t
    return a.user_id.localeCompare(b.user_id)
  })
  return { window: w.window, since: w.since, sort, accounts: rows.slice(0, RANK_LIMIT) }
}

export function traceRoutes(deps: TraceDeps): Record<string, MockRoute> {
  return {
    'GET /v1/trace/ips': (ctx) => {
      const w = parseWindow(ctx.query)
      if ('status' in w) return reply(ctx, w)
      ctx.send(200, buildIPsResponse(deps, w, ctx.user.userId))
    },
    'GET /v1/trace/subscribers': (ctx) => {
      const w = parseWindow(ctx.query)
      if ('status' in w) return reply(ctx, w)
      const sort = (ctx.query.get('sort') ?? '').trim() || 'fetches'
      if (!TRACE_SUB_SORTS.has(sort)) return reply(ctx, invalid({ sort: '不支持的排序' }))
      ctx.send(200, buildSubscribers(deps, w, sort))
    },
    'GET /v1/trace/accounts': (ctx) => {
      const w = parseWindow(ctx.query)
      if ('status' in w) return reply(ctx, w)
      const sort = (ctx.query.get('sort') ?? '').trim() || 'linked_ips'
      if (!TRACE_ACC_SORTS.has(sort)) return reply(ctx, invalid({ sort: '不支持的排序' }))
      ctx.send(200, buildAccounts(deps, w, sort))
    },
    'GET /v1/trace/reveal': (ctx) => {
      const token = (ctx.query.get('token') ?? '').trim()
      const ip = openTraceToken(token, ctx.user.userId)
      if (!ip) return reply(ctx, invalid({ token: '已失效，请刷新列表后再试' }))
      deps.onReveal(ctx)
      const g = deps.geoOf(ip)
      ctx.send(200, { ip, geo: g.geo, network_kind: g.kind })
    },
  }
}
