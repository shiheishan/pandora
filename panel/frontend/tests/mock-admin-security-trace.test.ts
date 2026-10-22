import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { userStore } from '../dev/mock/admin/users'
import { auditResponse } from '../src/admin/screens/security/schemas'
import {
  traceAccountsResponse,
  traceIPsResponse,
  traceRevealResponse,
  traceSubscribersResponse,
  type TraceIP,
} from '../src/admin/screens/security/trace'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · admin trace overview（由审计与拉取日志现算）', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>

  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
  })
  afterAll(() => close(server))

  const get = (path: string) => mockFetch(base, auth, 'GET', path)
  const json = async (res: Response) => (await res.json()) as Record<string, unknown>

  async function fetchAllAudit() {
    const events: (ReturnType<typeof auditResponse.parse>['events'][number])[] = []
    let offset = 0
    let total = Number.POSITIVE_INFINITY
    while (offset < total) {
      const page = auditResponse.parse(await json(await get(`/v1/audit?limit=200&offset=${offset}`)))
      total = page.total
      events.push(...page.events)
      if (page.events.length === 0) break
      offset += page.events.length
    }
    return events
  }

  function loginCountsSince(
    events: ReturnType<typeof fetchAllAudit> extends Promise<infer E> ? E : never,
    since: string,
  ) {
    const sinceMs = Date.parse(since)
    let logins = 0
    let login_failures = 0
    for (const e of events) {
      if (Date.parse(e.occurred_at) <= sinceMs) continue
      if (e.source_ip === null) continue
      if (e.actor_kind !== 'user' && e.actor_kind !== 'anonymous') continue
      if (e.action === 'user.login' && e.outcome === 'success') logins++
      if (e.action === 'user.login_failed') login_failures++
    }
    return { logins, login_failures }
  }

  async function revealRow(row: TraceIP) {
    const res = await get(`/v1/trace/reveal?token=${encodeURIComponent(row.token)}`)
    expect(res.status).toBe(200)
    return traceRevealResponse.parse(await res.json())
  }

  async function rowByPlainIp(masked: string, plain: string) {
    const trace = traceIPsResponse.parse(await json(await get('/v1/trace/ips')))
    for (const row of trace.ips.filter((r) => r.ip_masked === masked)) {
      const rev = await revealRow(row)
      if (rev.ip === plain) return row
    }
    return undefined
  }

  it('accepts trace list schemas and rejects bad window / sort', async () => {
    const ipsDefault = await get('/v1/trace/ips')
    expect(ipsDefault.status).toBe(200)
    const ips24 = traceIPsResponse.parse(await ipsDefault.json())
    expect(ips24.window).toBe('24h')

    const ips7 = await get('/v1/trace/ips?window=7d')
    expect(ips7.status).toBe(200)
    traceIPsResponse.parse(await ips7.json())

    const subs = await get('/v1/trace/subscribers?window=30d&sort=last')
    expect(subs.status).toBe(200)
    traceSubscribersResponse.parse(await subs.json())

    const acc = await get('/v1/trace/accounts?sort=logins')
    expect(acc.status).toBe(200)
    traceAccountsResponse.parse(await acc.json())

    const badWindow = await get('/v1/trace/ips?window=1h')
    expect(badWindow.status).toBe(422)
    const wBody = (await badWindow.json()) as { error: { fields?: Record<string, string> } }
    expect(wBody.error.fields?.window).toBeDefined()

    const badSubSort = await get('/v1/trace/subscribers?sort=nope')
    expect(badSubSort.status).toBe(422)
    const sBody = (await badSubSort.json()) as { error: { fields?: Record<string, string> } }
    expect(sBody.error.fields?.sort).toBeDefined()

    const badAccSort = await get('/v1/trace/accounts?sort=nope')
    expect(badAccSort.status).toBe(422)
    const aBody = (await badAccSort.json()) as { error: { fields?: Record<string, string> } }
    expect(aBody.error.fields?.sort).toBeDefined()
  })

  it('overview login counts match audit inside the trace window', async () => {
    const auditEvents = await fetchAllAudit()
    const trace = traceIPsResponse.parse(await json(await get('/v1/trace/ips')))
    const { logins, login_failures } = loginCountsSince(auditEvents, trace.since)
    expect(trace.overview.logins).toBe(logins)
    expect(trace.overview.login_failures).toBe(login_failures)
  })

  it('flags credential-stuffing, scan and shared-datacenter IPs in the 24h ranking', async () => {
    const trace = traceIPsResponse.parse(await json(await get('/v1/trace/ips')))
    const clusterIds = [userStore[5]!.id, userStore[25]!.id, userStore[45]!.id].sort()

    let stuffing: TraceIP | undefined
    let scanner: TraceIP | undefined
    let shared: TraceIP | undefined

    for (const row of trace.ips.filter((r) => r.ip_masked === '203.0.*.*')) {
      const rev = await revealRow(row)
      if (rev.ip === '203.0.113.48') stuffing = row
      if (rev.ip === '203.0.113.14') scanner = row
      if (rev.ip === '203.0.113.45') shared = row
    }

    expect(stuffing).toBeDefined()
    expect(stuffing!.events).toBeGreaterThanOrEqual(8)
    expect(stuffing!.fetches).toBe(0)
    expect(stuffing!.accounts).toBe(0)
    expect(stuffing!.users).toEqual([])
    expect(stuffing!.network_kind).toBe('datacenter')

    expect(scanner).toBeDefined()
    expect(scanner!.fetches).toBeGreaterThanOrEqual(4)
    expect(scanner!.events).toBe(0)
    expect(scanner!.accounts).toBe(0)

    expect(shared).toBeDefined()
    expect(shared!.accounts).toBe(3)
    expect([...shared!.users.map((u) => u.id)].sort()).toEqual(clusterIds)
  })

  it('subscriber fetch totals reconcile with overview and sort by fetches desc', async () => {
    const [ipsRes, subsRes] = await Promise.all([
      get('/v1/trace/ips'),
      get('/v1/trace/subscribers?sort=fetches'),
    ])
    const trace = traceIPsResponse.parse(await json(ipsRes))
    const subs = traceSubscribersResponse.parse(await json(subsRes))

    const scanner = await rowByPlainIp('203.0.*.*', '203.0.113.14')
    expect(scanner).toBeDefined()
    const subscriberFetches = subs.subscribers.reduce((n, s) => n + s.fetches, 0)
    expect(subscriberFetches + scanner!.fetches).toBe(trace.overview.fetches)

    for (let i = 1; i < subs.subscribers.length; i++) {
      expect(subs.subscribers[i - 1]!.fetches).toBeGreaterThanOrEqual(subs.subscribers[i]!.fetches)
    }
  })

  it('ranks datacenter-heavy accounts in the top three', async () => {
    const body = traceAccountsResponse.parse(await json(await get('/v1/trace/accounts?sort=datacenter_hits')))
    const clusterIds = new Set([userStore[5]!.id, userStore[25]!.id, userStore[45]!.id])
    const top3 = body.accounts.slice(0, 3)
    expect(new Set(top3.map((a) => a.user_id))).toEqual(clusterIds)
    expect(top3.every((a) => a.datacenter_hits >= 1)).toBe(true)
    if (body.accounts[3]) expect(body.accounts[3].datacenter_hits).toBe(0)
  })

  it('reveal audits only on valid tokens', async () => {
    const viewedBefore = auditResponse.parse(await json(await get('/v1/audit?action=security.source_ip_viewed&limit=1'))).total

    for (const token of ['mock-x', 'abc']) {
      const bad = await get(`/v1/trace/reveal?token=${encodeURIComponent(token)}`)
      expect(bad.status).toBe(422)
      const err = (await bad.json()) as { error: { fields?: Record<string, string> } }
      expect(err.error.fields?.token).toBe('已失效，请刷新列表后再试')
    }
    const still = auditResponse.parse(await json(await get('/v1/audit?action=security.source_ip_viewed&limit=1'))).total
    expect(still).toBe(viewedBefore)

    const trace = traceIPsResponse.parse(await json(await get('/v1/trace/ips')))
    const first = trace.ips[0]
    expect(first?.token).toBeTruthy()
    const ok = await get(`/v1/trace/reveal?token=${encodeURIComponent(first!.token)}`)
    expect(ok.status).toBe(200)
    traceRevealResponse.parse(await ok.json())

    const viewedAfter = auditResponse.parse(await json(await get('/v1/audit?action=security.source_ip_viewed&limit=1'))).total
    expect(viewedAfter).toBe(viewedBefore + 1)

    const latest = auditResponse.parse(await json(await get('/v1/audit?action=security.source_ip_viewed&limit=1'))).events[0]
    expect(latest?.actor_email).toBe(MOCK_ACCOUNTS.admin.email)
  })
})
