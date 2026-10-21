import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { plans } from '../dev/mock/admin/plans-store'
import { userStore } from '../dev/mock/admin/users'
import { accessResponse, auditResponse, clustersResponse } from '../src/admin/screens/security/schemas'
import { profileSchema } from '../src/admin/screens/users/schemas'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · admin security scenarios', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
  })
  afterAll(() => close(server))

  const get = async (path: string) => (await mockFetch(base, auth, 'GET', path)).json() as Promise<unknown>
  const profile = async (id: string) => profileSchema.parse(await get(`/v1/users/${id}/profile`))
  const access = async (query: string) => accessResponse.parse(await get(`/v1/access-log${query}`)).items
  const audit = async (query: string) => auditResponse.parse(await get(`/v1/audit${query}`)).events
  const allClusters = async () => clustersResponse.parse(await get('/v1/ip-clusters?include_reviewed=1')).clusters
  const at = (i: number) => userStore[i]!

  const clusterRisk = (accounts: number, network_kind: string) =>
    accounts >= 5 || network_kind === 'datacenter' ? 'high' : accounts >= 3 ? 'mid' : 'low'

  it('surfaces k.liu credential stuffing in access log and audit', async () => {
    const kl = at(1)
    const mine = await access(`?user=${kl.id}&category=login&limit=200`)
    const failedOnKl = mine.filter((i) => i.action === 'user.login_failed' && i.target_user_id === kl.id && i.target_user_email === kl.email)
    expect(failedOnKl.length).toBeGreaterThan(0)
    expect(failedOnKl.every((i) => !('user_id' in i) || i.user_id === undefined)).toBe(true)
    expect(failedOnKl.some((i) => i.network_kind === 'datacenter')).toBe(true)

    const logins = mine.filter((i) => i.action === 'user.login' && i.user_id === kl.id)
    expect(logins.length).toBeGreaterThan(0)
    const latestLogin = logins[0]!
    const latestFailed = failedOnKl[0]!
    expect(latestLogin.occurred_at < latestFailed.occurred_at).toBe(true)

    expect((await access('?user=k.liu&category=login&limit=200')).some((i) => i.target_user_id === kl.id)).toBe(true)

    const rows = await audit('?q=k.liu&limit=200')
    expect(
      rows.some(
        (r) =>
          r.action === 'user.login_failed' &&
          r.actor_kind === 'anonymous' &&
          r.resource_type === 'user' &&
          r.resource_id === kl.id &&
          r.resource_label === kl.email &&
          r.reason === 'invalid_credentials',
      ),
    ).toBe(true)

    expect(mine.every((i) => i.user_id === kl.id || i.target_user_id === kl.id)).toBe(true)
  })

  it('keeps tomato2 profile aligned with the shared-IP cluster', async () => {
    const t2 = at(25)
    const p = await profile(t2.id)
    const shared = p.ips.find((row) => row.ip === '203.0.113.45')
    expect(shared).toMatchObject({ accounts: 3, network_kind: 'datacenter', risk: 'high' })
    expect(shared!.geo).not.toBe('')
    expect(p.risk).toBe('high')
    expect(p.related.map((r) => r.email).sort()).toEqual([at(5).email, at(45).email].sort())

    const cluster = (await allClusters()).find((c) => c.ip === '203.0.113.45')!
    expect(cluster.risk).toBe('high')
    expect(cluster.accounts).toBe(3)
    expect(cluster.users.map((u) => u.id).sort()).toEqual([at(5), at(25), at(45)].map((u) => u.id).sort())
  })

  it('shows the same egress, account count, and risk on every cluster member profile', async () => {
    for (const c of await allClusters()) {
      for (const u of c.users) {
        const p = await profile(u.id)
        const row = p.ips.find((row) => row.ip === c.ip && row.accounts > 1)
        expect(row).toBeDefined()
        expect(row!.accounts).toBe(c.accounts)
        expect(row!.risk).toBe(c.risk)
        expect(p.risk).not.toBe('')
      }
    }
  })

  it('lists exactly the seeded shared-IP scenes with clusterRisk', async () => {
    const SCENES: Record<string, number[]> = {
      '198.51.100.18': [3, 9, 13, 18, 23],
      '203.0.113.45': [5, 25, 45],
      '198.51.100.24': [0, 7, 27],
      '198.51.100.42': [1, 21],
      '198.51.100.51': [15, 35],
      '198.51.100.61': [12, 31],
      '198.51.100.31': [24, 39, 40],
      '': [19, 29],
    }
    const list = await allClusters()
    expect(list).toHaveLength(8)
    const byIp = Object.fromEntries(list.map((c) => [c.ip, c.users.map((u) => u.id).sort()]))
    const expected = Object.fromEntries(Object.entries(SCENES).map(([ip, idx]) => [ip, idx.map((i) => at(i).id).sort()]))
    expect(byIp).toEqual(expected)
    for (const c of list) {
      expect(c.risk).toBe(clusterRisk(c.accounts, c.network_kind))
    }
  })

  it('leaves risk empty on profiles with no shared egress', async () => {
    const p = await profile(at(2).id)
    expect(p.risk).toBe('')
    expect(p.related).toEqual([])
    expect(p.ips.length).toBeGreaterThan(0)
    expect(p.ips.every((row) => row.accounts === 1 && row.risk === '')).toBe(true)
    expect(p.registered_ip).not.toBe('')
  })

  it('returns 404 for unknown users and logs source_ip_viewed on profile read', async () => {
    const missing = await mockFetch(base, auth, 'GET', '/v1/users/00000000-0000-4000-8000-000000000000/profile')
    expect(missing.status).toBe(404)
    await profile(at(2).id)
    expect((await audit('?action=security.source_ip_viewed&limit=5'))[0]).toMatchObject({
      actor_email: MOCK_ACCOUNTS.admin.email,
      resource_type: 'user',
      resource_id: at(2).id,
      resource_label: at(2).email,
    })
  })

  it('matches plan publish audit rows to plans-store published versions', async () => {
    const rows = await audit('?action=plan_version.publish&limit=200')
    const published = plans.flatMap((p) => p.versions).filter((v) => v.status === 'published' && v.published_at)
    expect(rows.map((r) => r.resource_id).sort()).toEqual(published.map((v) => v.id).sort())
    for (const r of rows) {
      const v = published.find((x) => x.id === r.resource_id)!
      expect(r).toMatchObject({
        resource_type: 'plan_version',
        resource_label: null,
        actor_kind: 'admin',
        actor_email: v.created_by_email,
      })
    }
    const actions = (await audit('?action=plan&limit=200')).map((r) => r.action)
    expect(actions).toContain('plan_version.publish')
    expect(actions).toContain('plan.usage_update')
  })

  it('limits subscribe access-log outcomes to the five Go values', async () => {
    const allowed = ['ok', 'expired', 'rate_limited', 'error', 'not_found']
    const subs = await access('?category=subscribe&limit=200')
    expect(subs.length).toBeGreaterThan(0)
    expect(subs.every((i) => allowed.includes(i.outcome!))).toBe(true)
    expect(subs.some((i) => i.outcome === 'not_found' && !('user_id' in i))).toBe(true)
  })

  it('keeps every seeded user email on reserved domains', () => {
    const re = /@([a-z0-9-]+\.)*example\.(com|net|org|test)$/
    expect(userStore.every((u) => re.test(u.email))).toBe(true)
  })
})
