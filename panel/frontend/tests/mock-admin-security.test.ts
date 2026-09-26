/**
 * [INPUT]: 依赖 vitest，依赖 ./mock-helpers，依赖 ../dev/mock-api 的 MOCK_ACCOUNTS，依赖 ../src/admin/screens/security/schemas 的审计 / 访问日志 / 聚类 / 开关 schema
 * [OUTPUT]: 对外提供安全与运维（后台-09 后半）假接口的测试
 * [POS]: tests 的安全假后端守卫：只读账号整块 404（停用先 404 不弹 reauth）；审计能被页面 schema 接住、存量行无认证与 IP、q / 前缀 / 类型筛选与 limit 越界回 50；导出先 reauth、日期 422、BOM 与防公式、导出本身记审计；访问日志分类表（payment_provider 归管理端）、未知分类与结果 422、仅错误不含成功、IP 与账号筛选；聚类默认不列标记正常的、机房判高风险、标记正常 note 上限与坏 key 404；批量停用 reauth、422 字段、跳过后台账号 / 已停用 / 非成员、同键重放、用户模块看到已停用；开关八行（R102 删去三个未接入的）、排序、核心项与缺原因 409 数据库原文、切换记审计
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { accessResponse, auditResponse, clusterDisabled, clustersResponse, switchesResponse, type Cluster } from '../src/admin/screens/security/schemas'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · admin security and operations', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
  })
  afterAll(() => close(server))

  const call = (method: string, path: string, body?: unknown, key?: string) => mockFetch(base, auth, method, path, body, key)
  const json = async (res: Response) => (await res.json()) as Record<string, unknown>
  const expireAndReauth = async () => {
    await fetch(`${base}/__mock/expire-reauth`, { method: 'POST' })
    return async () => {
      const res = await call('POST', '/v1/auth/reauth', { password: MOCK_ACCOUNTS.admin.password })
      auth = bearer(((await res.json()) as { access_token: string }).access_token)
    }
  }
  const audit = async (query = '') => auditResponse.parse(await json(await call('GET', `/v1/audit${query}`)))
  const clusters = async (all = false) => clustersResponse.parse(await json(await call('GET', `/v1/ip-clusters${all ? '?include_reviewed=1' : ''}`))).clusters

  it('hides the whole module from the viewer, permission before reauth', async () => {
    const viewer = bearer((await loginAs(base, MOCK_ACCOUNTS.viewer)).access_token)
    for (const path of ['/v1/audit', '/v1/audit/export', '/v1/access-log', '/v1/ip-clusters', '/v1/switches']) expect((await fetch(`${base}${path}`, { headers: viewer })).status, path).toBe(404)
    await fetch(`${base}/__mock/expire-reauth`, { method: 'POST' })
    const res = await mockFetch(base, viewer, 'POST', '/v1/ip-clusters/ab/disable-accounts', { user_ids: [], reason: '' }, 'viewer-1')
    expect(res.status).toBe(404)
    expect((await mockFetch(base, viewer, 'POST', '/v1/switches/billing.checkout', { enabled: false, reason: 'x' })).status).toBe(404)
    // 管理员这边要重新认证一次，后面的用例才能直接写
    const res2 = await call('POST', '/v1/auth/reauth', { password: MOCK_ACCOUNTS.admin.password })
    auth = bearer(((await res2.json()) as { access_token: string }).access_token)
  })

  it('serves audit rows the page schema accepts, with legacy rows and filters that match Go', async () => {
    const page = await audit()
    expect(page.events).toHaveLength(50)
    expect(page.total).toBeGreaterThan(100)
    expect(page.events.every((e, i) => i === 0 || page.events[i - 1]!.occurred_at >= e.occurred_at)).toBe(true)
    const all = await audit('?limit=200')
    expect(all.events).toHaveLength(Math.min(200, all.total))
    expect((await audit('?limit=500')).events).toHaveLength(50)
    expect(all.events.some((e) => e.auth_context === null && e.source_ip === null && e.actor_kind === 'admin')).toBe(true)
    expect(all.events.some((e) => e.actor_kind === 'node')).toBe(true)

    const orders = await audit('?action=order.&limit=200')
    expect(orders.events.length).toBeGreaterThan(0)
    expect(orders.events.every((e) => e.action.startsWith('order.'))).toBe(true)
    const failed = await audit('?outcome=failure&actor_kind=anonymous&limit=200')
    expect(failed.events.every((e) => e.outcome === 'failure' && e.actor_kind === 'anonymous')).toBe(true)
    const byEmail = await audit('?q=zhou.min&limit=200')
    expect(byEmail.events.length).toBeGreaterThan(0)
    expect(byEmail.events.every((e) => e.actor_email === 'zhou.min@pandora.dev' || e.action.includes('zhou.min'))).toBe(true)
    const target = all.events.find((e) => e.resource_id)!
    expect((await audit(`?q=${target.resource_id}`)).events.map((e) => e.id)).toContain(target.id)
  })

  it('exports CSV after reauth, validates dates and records the export itself', async () => {
    const reauth = await expireAndReauth()
    expect(await json(await call('GET', '/v1/audit/export'))).toMatchObject({ error: { code: 'reauth_required' } })
    await reauth()
    expect(await json(await call('GET', '/v1/audit/export?from=2026-13-01'))).toMatchObject({ error: { code: 'validation_failed', fields: { from: '日期格式应为 YYYY-MM-DD' } } })
    expect(await json(await call('GET', '/v1/audit/export?from=2026-09-02&to=2026-09-01'))).toMatchObject({ error: { fields: { to: '结束日期不能早于开始日期' } } })

    const res = await call('GET', '/v1/audit/export?action=user.')
    expect(res.status).toBe(200)
    expect(res.headers.get('content-type')).toBe('text/csv; charset=utf-8')
    expect(res.headers.get('content-disposition')).toMatch(/^attachment; filename="audit-\d{8}-\d{6}\.csv"$/)
    const bytes = new Uint8Array(await res.clone().arrayBuffer())
    expect([...bytes.slice(0, 3)]).toEqual([0xef, 0xbb, 0xbf])
    const lines = (await res.text()).replace(/^\ufeff/, '').trim().split('\n')
    expect(lines[0]).toBe('occurred_at,actor_kind,actor_email,action,resource_type,resource_id,resource_label,api_domain,outcome,reason,source_ip,auth_context')
    expect(lines.slice(1).every((l) => l.split(',')[3]!.startsWith('user.'))).toBe(true)

    const latest = (await audit('?action=audit.export')).events[0]!
    expect(latest).toMatchObject({ actor_email: MOCK_ACCOUNTS.admin.email, auth_context: 'reauth' })
  })

  it('merges audit and fetch rows into the access log with Go categories and validation', async () => {
    const all = accessResponse.parse(await json(await call('GET', '/v1/access-log?limit=200'))).items
    expect(all.some((i) => i.category === 'subscribe') && all.some((i) => i.category === 'login')).toBe(true)
    expect(all.find((i) => i.action?.startsWith('payment_provider.'))?.category).toBe('admin')
    expect(all.find((i) => i.action === 'payment.succeeded')?.category).toBe('payment')
    // omitempty：系统动作没有账号与 IP，键不出现
    expect(all.find((i) => i.action === 'order.expired')).not.toHaveProperty('ip')

    expect(await json(await call('GET', '/v1/access-log?category=http'))).toMatchObject({ error: { fields: { category: '不认识的分类' } } })
    expect(await json(await call('GET', '/v1/access-log?outcome=ok'))).toMatchObject({ error: { fields: { outcome: expect.any(String) } } })

    const errors = accessResponse.parse(await json(await call('GET', '/v1/access-log?outcome=error&limit=200'))).items
    expect(errors.length).toBeGreaterThan(0)
    expect(errors.every((i) => i.outcome !== 'success' && i.outcome !== 'ok')).toBe(true)
    const admin = accessResponse.parse(await json(await call('GET', '/v1/access-log?category=admin&limit=200'))).items
    expect(admin.every((i) => i.category === 'admin')).toBe(true)
    const subs = accessResponse.parse(await json(await call('GET', '/v1/access-log?category=subscribe&limit=200'))).items
    expect(subs.every((i) => i.category === 'subscribe' && i.action === 'subscription.fetch')).toBe(true)

    const ip = accessResponse.parse(await json(await call('GET', '/v1/access-log?ip=223.104.63.18&limit=200'))).items
    expect(ip.length).toBeGreaterThan(0)
    expect(ip.every((i) => i.ip === '223.104.63.18' && i.geo === '中国 广东 移动')).toBe(true)
    const someone = all.find((i) => i.user_id)!
    const mine = accessResponse.parse(await json(await call('GET', `/v1/access-log?user=${someone.user_id}&limit=200`))).items
    expect(mine.every((i) => i.user_id === someone.user_id)).toBe(true)
    expect((accessResponse.parse(await json(await call('GET', '/v1/access-log?limit=2&offset=1')))).items).toHaveLength(2)
  })

  it('lists clusters without fresh normal reviews, rates datacenters high and marks clusters normal', async () => {
    const open = await clusters()
    const everything = await clusters(true)
    expect(everything.length).toBe(open.length + 1)
    const hidden = everything.find((c) => !open.some((o) => o.key === c.key))!
    expect(hidden.review).toMatchObject({ decision: 'normal' })
    expect(open.find((c) => c.network_kind === 'datacenter')).toMatchObject({ risk: 'high', accounts: 3 })
    expect(open.find((c) => c.ip === '')).toMatchObject({ geo: '', network_kind: '', risk: 'low' })
    expect(open.find((c) => c.review?.decision === 'normal')?.review?.expires_at).toBeTruthy()
    expect(open.every((c, i) => i === 0 || open[i - 1]!.accounts >= c.accounts)).toBe(true)

    expect((await call('POST', '/v1/ip-clusters/zz/review', {})).status).toBe(404)
    expect((await call('POST', `/v1/ip-clusters/${'ab'.repeat(32)}/review`, {})).status).toBe(404)
    const low = open.find((c) => c.risk === 'low' && c.ip !== '' && !c.review)!
    expect(await json(await call('POST', `/v1/ip-clusters/${low.key}/review`, { note: 'x'.repeat(501) }))).toMatchObject({ error: { fields: { note: '备注不能超过 500 字' } } })
    expect((await call('POST', `/v1/ip-clusters/${low.key}/review`, { note: '', extra: 1 })).status).toBe(400)
    const marked = await json(await call('POST', `/v1/ip-clusters/${low.key}/review`, { note: '同一家庭' }))
    expect(marked).toMatchObject({ key: low.key, decision: 'normal', expires_at: expect.stringMatching(/Z$/) })
    expect((await clusters()).some((c) => c.key === low.key)).toBe(false)
  })

  it('disables cluster members after reauth, skips staff and the already disabled, and replays', async () => {
    const withStaff = (await clusters()).find((c) => c.ip === '117.136.2.40')!
    const big = (await clusters()).find((c) => c.ip === '223.104.63.18')!
    const ids = (c: Cluster) => c.users.map((u) => u.id)
    const path = (c: Cluster) => `/v1/ip-clusters/${c.key}/disable-accounts`

    const reauth = await expireAndReauth()
    expect(await json(await call('POST', path(big), { user_ids: ids(big), reason: '同一出口批量注册' }, 'dis-1'))).toMatchObject({ error: { code: 'reauth_required' } })
    await reauth()
    expect((await call('POST', path(big), { user_ids: ids(big), reason: '同一出口批量注册' }))).toHaveProperty('status', 400)
    expect(await json(await call('POST', path(big), { user_ids: ['nope'], reason: '短' }, 'dis-bad'))).toMatchObject({ error: { fields: { reason: '原因必须为 5 到 500 字', user_ids: '包含无效的账号 ID' } } })
    expect(await json(await call('POST', path(big), { user_ids: [], reason: '同一出口批量注册' }, 'dis-empty'))).toMatchObject({ error: { fields: { user_ids: '请选择 1 到 200 个账号' } } })
    expect((await call('POST', '/v1/ip-clusters/xyz/disable-accounts', { user_ids: ids(big), reason: '同一出口批量注册' }, 'dis-key')).status).toBe(404)

    const stranger = ids(withStaff)[0]!
    const body = { user_ids: [...ids(big), stranger], reason: '同一出口批量注册' }
    const first = clusterDisabled.parse(await json(await call('POST', path(big), body, 'dis-2')))
    const suspendedBefore = big.users.filter((u) => u.status === 'suspended' || u.status === 'banned').length
    expect(first.disabled).toBe(big.users.length - suspendedBefore)
    expect(first.skipped).toEqual(expect.arrayContaining([{ user_id: stranger, reason: 'not_member' }, expect.objectContaining({ reason: 'already_disabled' })]))
    expect(await json(await call('POST', path(big), body, 'dis-2'))).toEqual(first)
    expect((await call('POST', path(big), { ...body, reason: '换了原因再发一次' }, 'dis-2')).status).toBe(409)

    const after = (await clusters()).find((c) => c.key === big.key)!
    expect(after.review).toMatchObject({ decision: 'disabled', expires_at: null })
    expect(after.users.every((u) => u.status === 'suspended' || u.status === 'banned')).toBe(true)
    const user = await json(await call('GET', `/v1/users/${big.users[0]!.id}`))
    expect(user).toMatchObject({ status: expect.stringMatching(/^(suspended|banned)$/) })
    expect((await audit('?action=risk.ip_cluster.disable')).events[0]).toMatchObject({ outcome: 'partial', reason: '同一出口批量注册' })

    // 只剩后台账号可选：一个都没停成，不写结论
    const staffOnly = withStaff.users.find((u) => u.email.startsWith('zhang.wei'))!
    const none = clusterDisabled.parse(await json(await call('POST', path(withStaff), { user_ids: [staffOnly.id], reason: '同一出口批量注册' }, 'dis-3')))
    expect(none).toEqual({ disabled: 0, skipped: [{ user_id: staffOnly.id, reason: 'administrator' }] })
    expect((await clusters()).find((c) => c.key === withStaff.key)!.review).toBeNull()
  })

  it('orders switches essential first and refuses what the database constraints refuse', async () => {
    const list = switchesResponse.parse(await json(await call('GET', '/v1/switches'))).switches
    expect(list).toHaveLength(8)
    expect(list.slice(0, 3).every((s) => s.essential)).toBe(true)
    expect(list.slice(3).map((s) => s.code)).toEqual([...list.slice(3).map((s) => s.code)].sort())
    // R102：三个没有代码读取的开关已从种子删除
    expect(list.some((s) => ['ops.bulk_export', 'ops.reports', 'node.autoscale'].includes(s.code))).toBe(false)

    const reauth = await expireAndReauth()
    expect(await json(await call('POST', '/v1/switches/billing.checkout', { enabled: false, reason: '渠道故障' }))).toMatchObject({ error: { code: 'reauth_required' } })
    await reauth()
    expect(await json(await call('POST', '/v1/switches/auth.login', { enabled: false, reason: '演练' }))).toMatchObject({ error: { code: 'conflict', message: expect.stringContaining('feature_switches_essential_stays_on') } })
    expect(await json(await call('POST', '/v1/switches/billing.checkout', { enabled: false, reason: '  ' }))).toMatchObject({ error: { code: 'conflict', message: expect.stringContaining('feature_switches_disable_needs_reason') } })
    expect((await call('POST', '/v1/switches/nope', { enabled: true, reason: '' })).status).toBe(404)
    expect((await call('POST', '/v1/switches/billing.checkout', { enabled: false, reason: '渠道故障', note: 1 })).status).toBe(400)

    expect(await json(await call('POST', '/v1/switches/billing.checkout', { enabled: false, reason: '渠道故障' }))).toEqual({ ok: true, enabled: false })
    expect(switchesResponse.parse(await json(await call('GET', '/v1/switches'))).switches.find((s) => s.code === 'billing.checkout')).toMatchObject({ enabled: false, reason: '渠道故障' })
    expect(await json(await call('POST', '/v1/switches/billing.checkout', { enabled: true, reason: '' }))).toEqual({ ok: true, enabled: true })
    expect(switchesResponse.parse(await json(await call('GET', '/v1/switches'))).switches.find((s) => s.code === 'billing.checkout')).toMatchObject({ enabled: true, reason: null })
    expect((await audit('?action=feature_switch.toggle')).events.slice(0, 2).map((e) => e.reason)).toEqual([null, '渠道故障'])
  })
})
