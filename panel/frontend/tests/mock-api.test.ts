/**
 * [INPUT]: 依赖 vitest，依赖 node:http 的 createServer，依赖 ../dev/mock-api 的 mockApi / MOCK_ACCOUNTS，依赖 ../dev/mock/types 的 matchPattern，依赖 ../src/admin/screens/nodes/schemas 的节点 / 服务器 / 节点池 / 全局路由 schema，依赖 ../src/admin/screens/content/schemas 的公告 / 内容页 / 主题 / 插槽 / 站点时区 schema，依赖 ../dev/mock/admin/plans 的 setSalesEnabled，依赖 ../src/admin/screens/plans/schemas 的套餐与流量包 schema
 * [OUTPUT]: 对外提供假后端外壳与模块分发的测试
 * [POS]: tests 的假后端守卫：把 mockApi 的中间件挂到真实的本地 HTTP 服务上，用 fetch 验证外壳接口、模块分发、权限 404、reauth 先于幂等、同键重放与换请求 409、只重放 2xx（4xx 后同 key 重新执行、条件改好后成功，R85）（调账用 users 假后端的真实种子用户，余额经详情接口核对，种子外的 id 回 404）——各页面会话往 dev/mock/ 里加接口时都依赖这几条行为；另守用户第 ④ 步（流量重置、批量、用户组、设备模式）；营销假接口的礼品卡掩码、一次性导出（非 JSON 重放不带 Content-Disposition）与未知字段 400；节点假接口的列表能被页面 schema 接住、读不回敏感键、复制出新节点、非法状态边与已部署节点迁移回 409、协议按 schema 校验；服务器假接口能被页面 schema 接住、状态机与进入 ready 的前提、PATCH 清空与容量下限、删除仅草稿或已退役并级联静默名下节点、安装令牌幂等；节点池新建 / 编辑 / 删除守卫；全局路由 revision 冲突、删除被引用出站 409、匹配类型校验与发布；内容假接口对只读账号整块 404、公告草稿 → 定时 → 发布 → 撤回的状态机与版本冲突、知识库保存新版本归档同受众旧发布版与重复归档、内置主题 43 键、插槽净化与空内容 dropped 为 null、站点时区校验；套餐假接口的目录能被页面 schema 接住、向导单事务新建与幂等重放、编辑向导的 null = 不动与开新版本、草稿版本全流程、价格与销售开关 503、流量包 updated_at 乐观锁
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS, mockApi } from '../dev/mock-api'
import { matchPattern } from '../dev/mock/types'
import { setSalesEnabled } from '../dev/mock/admin/plans'
import { announcementsResponse, annSaved, pageArchived, pageResponse, pageSaved, pagesResponse, siteSettingsSchema, slotSaved, slotsResponse, themesResponse } from '../src/admin/screens/content/schemas'
import { globalRoutingSchema, nodesResponse, poolsResponse, serverSchema, serversResponse } from '../src/admin/screens/nodes/schemas'
import {
  packResponseSchema,
  packsSchema,
  planCreatedSchema,
  planPoolsSchema,
  planResponseSchema,
  plansSchema,
  planUpdatedSchema,
  priceCreatedSchema,
  versionCreatedSchema,
} from '../src/admin/screens/plans/schemas'

type Middleware = (req: IncomingMessage, res: ServerResponse, next: () => void) => void

async function serve(app: 'admin' | 'portal'): Promise<{ server: Server; base: string }> {
  let middleware: Middleware | undefined
  const plugin = mockApi(app)
  const fakeVite = {
    middlewares: { use: (fn: Middleware) => (middleware = fn) },
    config: { logger: { info: () => {}, error: () => {} } },
  }
  ;(plugin.configureServer as (server: unknown) => void)(fakeVite)
  const server = createServer((req, res) =>
    middleware!(req, res, () => {
      res.statusCode = 418
      res.end()
    }),
  )
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  return { server, base: `http://127.0.0.1:${(server.address() as AddressInfo).port}` }
}

describe('matchPattern', () => {
  it('matches method, literal segments and :params', () => {
    expect(matchPattern('POST /v1/users/:id/balance', 'POST', '/v1/users/abc/balance')).toEqual({ id: 'abc' })
    expect(matchPattern('POST /v1/users/:id/balance', 'GET', '/v1/users/abc/balance')).toBeNull()
    expect(matchPattern('GET /v1/users/:id', 'GET', '/v1/users/')).toBeNull()
    expect(matchPattern('GET /v1/users/:id', 'GET', '/v1/users/a/b')).toBeNull()
    expect(matchPattern('GET /v1/pages/:slug', 'GET', '/v1/pages/%E5%B8%AE')).toEqual({ slug: '帮' })
  })
})

describe('mock api · admin', () => {
  let server: Server
  let base: string
  beforeAll(async () => ({ server, base } = await serve('admin')))
  afterAll(() => new Promise<void>((resolve) => server.close(() => resolve())))

  const login = async (account: { email: string; password: string }) => {
    const res = await fetch(`${base}/v1/auth/login`, { method: 'POST', body: JSON.stringify(account) })
    expect(res.status).toBe(200)
    return (await res.json()) as { access_token: string; permissions: string[] }
  }
  // dev/mock/admin/users.ts 的第 2 个种子用户（前 6 个 id 固定，与仪表盘流量排行一致），种子余额非 0
  const SEED_USER = '1a2b3c42-0000-4000-8000-000000000002'
  const adjust = (token: string, key: string | null, body: unknown, id = SEED_USER) =>
    fetch(`${base}/v1/users/${id}/balance`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}`, ...(key ? { 'Idempotency-Key': key } : {}) },
      body: JSON.stringify(body),
    })

  it('passes non-API paths through to vite', async () => {
    expect((await fetch(`${base}/index.html`)).status).toBe(418)
  })

  it('logs the viewer in with read-only permissions and reports them from GET v1/me', async () => {
    const viewer = await login(MOCK_ACCOUNTS.viewer)
    expect(viewer.permissions).toContain('node.read')
    expect(viewer.permissions).not.toContain('marketing.coupon.read')
    const me = await fetch(`${base}/v1/me`, { headers: { Authorization: `Bearer ${viewer.access_token}` } })
    expect(await me.json()).toMatchObject({ email: MOCK_ACCOUNTS.viewer.email, roles: [{ code: 'viewer' }], reauthed: true })
    const admin = await login(MOCK_ACCOUNTS.admin)
    expect(admin.permissions.length).toBeGreaterThan(viewer.permissions.length)
  })

  it('answers unknown routes with the 404 envelope and requires a token for module routes', async () => {
    const { access_token } = await login(MOCK_ACCOUNTS.admin)
    const missing = await fetch(`${base}/v1/nope`, { headers: { Authorization: `Bearer ${access_token}` } })
    expect(missing.status).toBe(404)
    expect(await missing.json()).toMatchObject({ error: { code: 'not_found' } })
    expect((await adjust('bogus', 'k', { amount: 1, reason: '测试调账理由' })).status).toBe(401)
  })

  it('checks permission first: a viewer gets 404, not a reauth prompt', async () => {
    const { access_token } = await login(MOCK_ACCOUNTS.viewer)
    const res = await adjust(access_token, 'viewer-key', { amount: 100, reason: '测试调账理由' })
    expect(res.status).toBe(404)
  })

  it('rejects with reauth_required before touching the idempotency key, then replays with the same key', async () => {
    const { access_token } = await login(MOCK_ACCOUNTS.admin)
    await fetch(`${base}/__mock/expire-reauth`, { method: 'POST' })
    const body = { amount: 5000, reason: '补偿断线时长' }
    const blocked = await adjust(access_token, 'intent-1', body)
    expect(blocked.status).toBe(403)
    expect(await blocked.json()).toMatchObject({ error: { code: 'reauth_required' } })

    const reauth = await fetch(`${base}/v1/auth/reauth`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${access_token}` },
      body: JSON.stringify({ password: MOCK_ACCOUNTS.admin.password }),
    })
    const fresh = ((await reauth.json()) as { access_token: string }).access_token
    const balanceOf = async () => {
      const res = await fetch(`${base}/v1/users/${SEED_USER}`, { headers: { Authorization: `Bearer ${fresh}` } })
      expect(res.status).toBe(200)
      return ((await res.json()) as { balance: number }).balance
    }
    const seeded = await balanceOf()
    expect(seeded).toBeGreaterThan(0)
    const first = await adjust(fresh, 'intent-1', body)
    expect(first.status).toBe(200)
    const balance = ((await first.json()) as { balance: number }).balance
    expect(balance).toBe(seeded + 5000)
    expect(await balanceOf()).toBe(balance)

    // 重放回存下的响应，不再记一笔
    const replay = await adjust(fresh, 'intent-1', body)
    expect(await replay.json()).toEqual({ balance })
    expect(await balanceOf()).toBe(balance)
    const reused = await adjust(fresh, 'intent-1', { ...body, amount: 1 })
    expect(reused.status).toBe(409)
    expect(await reused.json()).toMatchObject({ error: { code: 'idempotency_key_reuse' } })
  })

  it('requires an Idempotency-Key and re-executes a rejected request instead of replaying it', async () => {
    const { access_token } = await login(MOCK_ACCOUNTS.admin)
    expect((await adjust(access_token, null, { amount: 1, reason: '测试调账理由' })).status).toBe(400)
    const bad = await adjust(access_token, 'intent-2', { amount: 1, reason: '短' })
    expect(bad.status).toBe(422)
    expect(await bad.json()).toMatchObject({ error: { code: 'validation_failed', fields: { reason: expect.any(String) } } })
    expect((await adjust(access_token, 'intent-2', { amount: 1, reason: '短' })).status).toBe(422)
    // 失败记录同样记住了指纹：换请求体仍是 idempotency_key_reuse
    const reused = await adjust(access_token, 'intent-2', { amount: 1, reason: '测试调账理由' })
    expect(await reused.json()).toMatchObject({ error: { code: 'idempotency_key_reuse' } })
  })

  it('re-executes a 4xx under the same key and succeeds once the condition is fixed (R85)', async () => {
    const { access_token } = await login(MOCK_ACCOUNTS.admin)
    const user = '1a2b3c45-0000-4000-8000-000000000005'
    const balanceOf = async () => ((await (await fetch(`${base}/v1/users/${user}`, { headers: { Authorization: `Bearer ${access_token}` } })).json()) as { balance: number }).balance
    const start = await balanceOf()
    const debit = { amount: -(start + 10000), reason: '扣回多发的补偿' }
    const refused = await adjust(access_token, 'debit-1', debit, user)
    expect(refused.status).toBe(409)
    expect(await refused.json()).toMatchObject({ error: { code: 'conflict', message: '余额不足，无法扣减' } })
    expect(await balanceOf()).toBe(start)
    // 另一次意图把余额补上，原 key 原请求再来：重新执行而不是重放 409
    expect((await adjust(access_token, 'topup-1', { amount: 20000, reason: '补足余额以便扣减' }, user)).status).toBe(200)
    const retried = await adjust(access_token, 'debit-1', debit, user)
    expect(retried.status).toBe(200)
    const after = ((await retried.json()) as { balance: number }).balance
    expect(after).toBe(start + 20000 + debit.amount)
    // 成功之后同 key 同请求原样重放，不再扣第二次
    expect(await (await adjust(access_token, 'debit-1', debit, user)).json()).toEqual({ balance: after })
    expect(await balanceOf()).toBe(after)
  })

  it('answers 404 for a user outside the seed instead of inventing a balance', async () => {
    const { access_token } = await login(MOCK_ACCOUNTS.admin)
    const res = await adjust(access_token, 'intent-3', { amount: 100, reason: '测试调账理由' }, '00000000-0000-4000-8000-000000000000')
    expect(res.status).toBe(404)
    expect(await res.json()).toMatchObject({ error: { code: 'not_found' } })
  })
})

describe('mock api · admin users ops', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    const res = await fetch(`${base}/v1/auth/login`, { method: 'POST', body: JSON.stringify(MOCK_ACCOUNTS.admin) })
    auth = { Authorization: `Bearer ${((await res.json()) as { access_token: string }).access_token}` }
  })
  afterAll(() => new Promise<void>((resolve) => server.close(() => resolve())))

  const SEED_USER = '1a2b3c42-0000-4000-8000-000000000002'
  const get = (path: string) => fetch(`${base}${path}`, { headers: auth })
  const send = (method: string, path: string, body: unknown, key?: string) =>
    fetch(`${base}${path}`, { method, headers: { ...auth, ...(key ? { 'Idempotency-Key': key } : {}) }, body: JSON.stringify(body) })
  const trafficUsed = async (id: string) => {
    const d = (await (await get(`/v1/users/${id}`)).json()) as { subscriptions: Array<{ status: string; current_period_end: string; quotas: Array<{ consumed: number }> }> }
    const active = d.subscriptions.filter((s) => s.status === 'active').sort((a, b) => b.current_period_end.localeCompare(a.current_period_end))[0]!
    return active.quotas[0]!.consumed
  }

  it('resets traffic only after reauth, logs it, and replays the stored result', async () => {
    await fetch(`${base}/__mock/expire-reauth`, { method: 'POST' })
    const blocked = await send('POST', `/v1/users/${SEED_USER}/traffic-reset`, { note: '补偿断线时长' }, 'reset-1')
    expect(blocked.status).toBe(403)
    const reauth = await fetch(`${base}/v1/auth/reauth`, { method: 'POST', headers: auth, body: JSON.stringify({ password: MOCK_ACCOUNTS.admin.password }) })
    auth = { Authorization: `Bearer ${((await reauth.json()) as { access_token: string }).access_token}` }

    const short = await send('POST', `/v1/users/${SEED_USER}/traffic-reset`, { note: '短' }, 'reset-0')
    expect(short.status).toBe(422)
    expect(await short.json()).toMatchObject({ error: { fields: { note: expect.any(String) } } })

    const before = await trafficUsed(SEED_USER)
    expect(before).toBeGreaterThan(0)
    const done = await send('POST', `/v1/users/${SEED_USER}/traffic-reset`, { note: '补偿断线时长' }, 'reset-1')
    expect(await done.json()).toEqual({ reset: true, freed_bytes: before })
    expect(await trafficUsed(SEED_USER)).toBe(0)
    const replay = await send('POST', `/v1/users/${SEED_USER}/traffic-reset`, { note: '补偿断线时长' }, 'reset-1')
    expect(await replay.json()).toEqual({ reset: true, freed_bytes: before })

    const history = (await (await get(`/v1/users/${SEED_USER}/traffic-resets`)).json()) as { logs: Array<Record<string, unknown>> }
    expect(history.logs[0]).toMatchObject({ reason: 'manual', consumed_before: before, actor_email: MOCK_ACCOUNTS.admin.email, note: '补偿断线时长' })
    expect((await get('/v1/traffic-resets?reason=admin')).status).toBe(400)
  })

  it('answers 422 without fields for a user with no active subscription', async () => {
    const none = (await (await get('/v1/users?sub_state=none&limit=1')).json()) as { users: Array<{ id: string }> }
    const res = await send('POST', `/v1/users/${none.users[0]!.id}/traffic-reset`, { note: '补偿断线时长' }, 'reset-2')
    expect(res.status).toBe(422)
    const body = (await res.json()) as { error: { fields?: unknown } }
    expect(body.error.fields).toBeUndefined()
  })

  it('previews, exports and generates against the same user list', async () => {
    const preview = (await (await send('POST', '/v1/users/bulk/preview', { status: 'active', expires_within_days: 30 })).json()) as { total: number; sample_rows: unknown[] }
    expect(preview.total).toBeGreaterThan(0)
    expect(preview.sample_rows.length).toBe(Math.min(10, preview.total))
    expect((await send('POST', '/v1/users/bulk/preview', { status: 'active', has_active: true })).status).toBe(400)
    expect((await send('POST', '/v1/users/bulk/preview', { status: 'disabled' })).status).toBe(422)

    const csv = await get('/v1/users/bulk/export?status=active&expires_within_days=30')
    expect(csv.headers.get('Content-Disposition')).toContain('users.csv')
    const bytes = new Uint8Array(await csv.arrayBuffer())
    // 带 BOM（Excel 打开中文不乱码）；TextDecoder 默认会吃掉 BOM，所以先看字节
    expect([...bytes.slice(0, 3)]).toEqual([0xef, 0xbb, 0xbf])
    const text = new TextDecoder().decode(bytes)
    expect(text.split('\n')[0]).toBe('邮箱,状态,分组,生效订阅,订单数,累计实付,注册时间,最近登录')
    expect(text.trim().split('\n')).toHaveLength(preview.total + 1)

    const made = await send('POST', '/v1/users/bulk/generate', { count: 3, email_prefix: 'dealer', email_domain: 'example.com', reason: '线下渠道预制' }, 'gen-1')
    const body = (await made.json()) as { count: number; users: Array<{ email: string; password: string }> }
    expect(body.count).toBe(3)
    expect(body.users[0]!.email).toMatch(/^dealer-[a-z0-9]{8}@example\.com$/)
    const listed = (await (await get('/v1/users?q=dealer-')).json()) as { total: number }
    expect(listed.total).toBe(3)
    const bad = await send('POST', '/v1/users/bulk/generate', { count: 3, email_prefix: 'dealer', email_domain: 'example.com', reason: '线下渠道预制', group_id: '9c0e1a2b-2222-4b00-8000-00000000000f' }, 'gen-2')
    expect(await bad.json()).toMatchObject({ error: { fields: { group_id: '分组不存在' } } })
  })

  it('refuses to delete a group that still has members, deletes an empty one', async () => {
    const { groups } = (await (await get('/v1/user-groups')).json()) as { groups: Array<{ id: string; users: number }> }
    const busy = groups.find((g) => g.users > 0)!
    const refused = await send('DELETE', `/v1/user-groups/${busy.id}`, {})
    expect(refused.status).toBe(409)
    const created = (await (await send('POST', '/v1/user-groups', { name: '渠道测试' })).json()) as { id: string }
    expect((await send('POST', '/v1/user-groups', { name: '渠道测试', code: 'vip' })).status).toBe(409)
    expect((await send('DELETE', `/v1/user-groups/${created.id}`, {})).status).toBe(200)
  })

  it('validates the global device mode and reflects it in GET v1/devices', async () => {
    expect((await send('POST', '/v1/settings/device-limit', { mode: 'kick' })).status).toBe(422)
    expect((await send('POST', '/v1/settings/device-limit', { mode: 'strict', grace: 6 })).status).toBe(422)
    expect((await send('POST', '/v1/settings/device-limit', { mode: 'strict', grace: 0 })).status).toBe(200)
    const d = (await (await get('/v1/devices')).json()) as { mode: string; grace: number; devices: Array<{ limit: number; online: number; exceeded: boolean }> }
    expect(d).toMatchObject({ mode: 'strict', grace: 0 })
    expect(d.devices.every((x) => x.exceeded === (x.limit > 0 && x.online > x.limit))).toBe(true)
  })
})

describe('mock api · admin marketing', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    const res = await fetch(`${base}/v1/auth/login`, { method: 'POST', body: JSON.stringify(MOCK_ACCOUNTS.admin) })
    auth = { Authorization: `Bearer ${((await res.json()) as { access_token: string }).access_token}` }
  })
  afterAll(() => new Promise<void>((resolve) => server.close(() => resolve())))

  const post = (path: string, body: unknown, key?: string) =>
    fetch(`${base}${path}`, { method: 'POST', headers: { ...auth, ...(key ? { 'Idempotency-Key': key } : {}) }, body: JSON.stringify(body) })

  it('never returns plaintext gift codes outside the generate sample and the one-time export', async () => {
    const { templates } = (await (await fetch(`${base}/v1/gift-cards`, { headers: auth })).json()) as { templates: Array<{ id: string; type: string }> }
    const general = templates.find((t) => t.type === 'general')!
    const made = (await (await post(`/v1/gift-cards/${general.id}/codes`, { count: 6, prefix: 'T' }, 'gen-1')).json()) as { batch_id: string; sample: string[]; batch: { exported_at: string | null } }
    expect(made.sample).toHaveLength(4)
    expect(made.batch.exported_at).toBeNull()
    const list = (await (await fetch(`${base}/v1/gift-cards/codes?batch_id=${made.batch_id}`, { headers: auth })).json()) as { codes: Array<Record<string, unknown>>; total: number }
    expect(list.total).toBe(6)
    expect(list.codes.every((c) => !('code' in c) && /^T[A-Z0-9]{4}•{8}$/.test(String(c.code_masked)))).toBe(true)

    const first = await post(`/v1/gift-cards/batches/${made.batch_id}/export`, {}, 'exp-1')
    expect(first.headers.get('content-disposition')).toBe(`attachment; filename="gift-codes-${made.batch_id.slice(0, 8)}.csv"`)
    const csv = await first.text()
    expect(csv.split('\n').filter(Boolean)).toHaveLength(7)
    expect(csv).toContain(made.sample[0]!)
    // 同键重放：同一份 CSV，但与后端一致不带 Content-Disposition
    const replay = await post(`/v1/gift-cards/batches/${made.batch_id}/export`, {}, 'exp-1')
    expect(replay.headers.get('content-disposition')).toBeNull()
    expect(await replay.text()).toBe(csv)
    const again = await post(`/v1/gift-cards/batches/${made.batch_id}/export`, {}, 'exp-2')
    expect(again.status).toBe(409)
    expect(await again.json()).toMatchObject({ error: { code: 'conflict', message: '该批次已导出，完整卡码不可再次获取' } })
  })

  it('rejects unknown fields like the Go decoder and keeps field-level 422s', async () => {
    const extra = await post('/v1/coupons', { code: 'X1', discount_type: 'percent', discount_value: 100, batch: true })
    expect(extra.status).toBe(400)
    const dup = await post('/v1/coupons', { code: 'autumn26', discount_type: 'percent', discount_value: 100 })
    expect(dup.status).toBe(409)
    const bad = await post('/v1/commission/config', { rate_percent: 51 })
    expect(await bad.json()).toMatchObject({ error: { code: 'validation_failed', fields: { rate_percent: '佣金比例需在 0 到 50 之间' } } })
  })
})

describe('mock api · admin nodes', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    const res = await fetch(`${base}/v1/auth/login`, { method: 'POST', body: JSON.stringify(MOCK_ACCOUNTS.admin) })
    auth = { Authorization: `Bearer ${((await res.json()) as { access_token: string }).access_token}` }
  })
  afterAll(() => new Promise<void>((resolve) => server.close(() => resolve())))

  const call = (method: string, path: string, body?: unknown, key?: string) =>
    fetch(`${base}${path}`, { method, headers: { ...auth, ...(key ? { 'Idempotency-Key': key } : {}) }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) })
  const list = async () => nodesResponse.parse(await (await call('GET', '/v1/nodes')).json()).nodes

  it('serves a list the page schema accepts, retired only on request', async () => {
    const rows = await list()
    expect(rows.length).toBeGreaterThan(3)
    expect(rows.some((n) => n.serving_status === 'retired')).toBe(false)
    const all = nodesResponse.parse(await (await call('GET', '/v1/nodes?include_retired=1')).json()).nodes
    expect(all.some((n) => n.serving_status === 'retired')).toBe(true)
    // 读接口抹掉敏感键
    expect(JSON.stringify(rows.map((n) => n.protocol_config))).not.toContain('private_key')
  })

  it('copies into a new node and refuses illegal transitions and deployed moves', async () => {
    const [src] = await list()
    const copy = await call('POST', `/v1/nodes/${src!.id}/copy`, { row_version: src!.row_version, name: `${src!.name} 副本` }, 'copy-1')
    expect(copy.status).toBe(201)
    const created = (await copy.json()) as { id: string; serving_status: string }
    expect(created.id).not.toBe(src!.id)
    expect(created.serving_status).toBe('draft')
    const draft = await call('POST', '/v1/nodes/status:batch', { items: [{ id: src!.id, row_version: src!.row_version }], serving_status: 'draft' }, 'batch-1')
    expect(draft.status).toBe(409)
    expect(await draft.json()).toMatchObject({ error: { fields: { serving_status: 'active -> draft' } } })
    const disabled = (await list()).find((n) => n.serving_status === 'disabled')!
    const move = await call('POST', `/v1/nodes/${disabled.id}/move`, { server_id: src!.server_id, row_version: disabled.row_version }, 'move-1')
    expect(move.status).toBe(409)
    expect(await move.json()).toMatchObject({ error: { fields: { active_identities: '1' } } })
  })

  it('validates protocol config against the schema and rejects unknown fields', async () => {
    const [n] = await list()
    const bad = await call('PATCH', `/v1/nodes/${n!.id}`, { row_version: n!.row_version, node_type: 'shadowsocks', protocol_config: {} })
    expect(bad.status).toBe(422)
    expect(await bad.json()).toMatchObject({ error: { fields: { 'protocol_config.cipher': '必填' } } })
    expect((await call('PATCH', `/v1/nodes/${n!.id}`, { row_version: n!.row_version, sort_order: 1 })).status).toBe(400)
    const stale = await call('PATCH', `/v1/nodes/${n!.id}`, { row_version: n!.row_version - 1, name: 'x' })
    expect(stale.status).toBe(409)
  })
})

describe('mock api · admin nodes infra', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    const res = await fetch(`${base}/v1/auth/login`, { method: 'POST', body: JSON.stringify(MOCK_ACCOUNTS.admin) })
    auth = { Authorization: `Bearer ${((await res.json()) as { access_token: string }).access_token}` }
  })
  afterAll(() => new Promise<void>((resolve) => server.close(() => resolve())))

  const call = (method: string, path: string, body?: unknown, key?: string) =>
    fetch(`${base}${path}`, { method, headers: { ...auth, ...(key ? { 'Idempotency-Key': key } : {}) }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) })
  const servers = async () => serversResponse.parse(await (await call('GET', '/v1/servers')).json()).servers

  it('serves servers the page schema accepts and walks the server lifecycle', async () => {
    const list = await servers()
    expect(list.some((s) => s.cpu_bp === null)).toBe(true)
    expect((await call('GET', '/v1/servers?status=online')).status).toBe(422)
    const created = await call('POST', '/v1/servers', { name: 'mock-edge-9', notes: '测试' })
    expect(created.status).toBe(201)
    const s = serverSchema.parse(await created.json())
    expect(s).toMatchObject({ status: 'draft', capacity_nodes: 32, node_count: 0 })
    expect((await call('POST', '/v1/servers', { name: 'mock-edge-9' })).status).toBe(409)
    expect((await call('POST', '/v1/servers', { name: 'x', provider: 'aws' })).status).toBe(400)
    // 进入 ready 需要可服务节点；非法边与版本冲突各回 409
    const ready = await call('POST', `/v1/servers/${s.id}/status`, { status: 'ready', row_version: s.row_version })
    expect(await ready.json()).toMatchObject({ error: { fields: { nodes: 'requires_valid_active_node' } } })
    expect(await (await call('POST', `/v1/servers/${s.id}/status`, { status: 'draining', row_version: s.row_version })).json()).toMatchObject({ error: { fields: { status: 'draft -> draining' } } })
    const patched = serverSchema.parse(await (await call('PATCH', `/v1/servers/${s.id}`, { row_version: s.row_version, notes: '', region: '东京' })).json())
    expect(patched).toMatchObject({ notes: null, region: '东京', row_version: s.row_version + 1 })
    expect((await call('PATCH', `/v1/servers/${s.id}`, { row_version: s.row_version, region: 'x' })).status).toBe(409)
    expect((await call('PATCH', `/v1/servers/${s.id}`, { row_version: patched.row_version, name: '' })).status).toBe(422)
    // 删除：空体 400，在役 409，草稿可删
    expect((await call('DELETE', `/v1/servers/${s.id}`)).status).toBe(400)
    const busy = list.find((x) => x.status === 'ready')!
    expect((await call('DELETE', `/v1/servers/${busy.id}`, { row_version: busy.row_version })).status).toBe(409)
    expect(await (await call('DELETE', `/v1/servers/${s.id}`, { row_version: patched.row_version })).json()).toEqual({ ok: true, id: s.id })
    expect((await call('GET', `/v1/servers/${s.id}`)).status).toBe(404)
  })

  it('refuses to shrink capacity below the nodes in use and cascades on delete', async () => {
    const kr = (await servers()).find((x) => x.name === 'kr-sel-edge-1')!
    const made = await call('POST', '/v1/nodes', { name: '首尔 02', server_id: kr.id, node_type: 'shadowsocks', server_host: 'kr2.pandora.run', server_port: 8388, protocol_config: { cipher: 'aes-256-gcm' } }, 'kr-node-1')
    expect(made.status).toBe(201)
    const low = await call('PATCH', `/v1/servers/${kr.id}`, { row_version: kr.row_version, capacity_nodes: 0 })
    expect(low.status).toBe(422)
    const full = (await servers()).find((x) => x.id === kr.id)!
    expect(full.node_count).toBe(1)
    const shrink = await call('PATCH', `/v1/servers/${kr.id}`, { row_version: full.row_version, capacity_nodes: 1 })
    expect(shrink.status).toBe(200)
    const after = serverSchema.parse(await shrink.json())
    expect((await call('DELETE', `/v1/servers/${kr.id}`, { row_version: after.row_version })).status).toBe(200)
    const orphan = nodesResponse.parse(await (await call('GET', '/v1/nodes?include_retired=1')).json()).nodes.find((n) => n.name === '首尔 02')!
    expect(orphan).toMatchObject({ serving_status: 'retired', server_id: null })
    const hk = (await servers()).find((x) => x.name === 'hk-hkg-edge-1')!
    const tight = await call('PATCH', `/v1/servers/${hk.id}`, { row_version: hk.row_version, capacity_nodes: 1 })
    expect(await tight.json()).toMatchObject({ error: { code: 'conflict', fields: { capacity_nodes: expect.stringMatching(/^minimum=[2-9]$/) } } })
  })

  it('issues server install tokens idempotently', async () => {
    const hk = (await servers()).find((x) => x.name === 'hk-hkg-edge-1')!
    const first = await call('POST', `/v1/servers/${hk.id}/bootstrap-token`, { ttl_minutes: 30 }, 'srv-tok-1')
    expect(first.status).toBe(201)
    const again = await call('POST', `/v1/servers/${hk.id}/bootstrap-token`, { ttl_minutes: 30 }, 'srv-tok-1')
    expect(await again.json()).toEqual(await first.json())
    expect((await call('POST', `/v1/servers/${hk.id}/bootstrap-token`, { ttl_minutes: 20 }, 'srv-tok-1')).status).toBe(409)
  })

  it('creates, edits and guards pool deletion', async () => {
    const created = await call('POST', '/v1/node-pools', { name: 'Europe West' })
    expect(created.status).toBe(200)
    const { id } = (await created.json()) as { id: string }
    expect((await call('POST', '/v1/node-pools', { name: '另一个', code: 'europe-west' })).status).toBe(409)
    expect((await call('POST', `/v1/node-pools/${id}`, { status: 'paused' })).status).toBe(422)
    expect(await (await call('POST', `/v1/node-pools/${id}`, { region: 'EU', status: 'draining' })).json()).toEqual({ ok: true })
    const pools = poolsResponse.parse(await (await call('GET', '/v1/node-pools')).json()).pools
    expect(pools.find((p) => p.id === id)).toMatchObject({ code: 'europe-west', region: 'EU', status: 'draining', members: [], plan_names: [] })
    const busy = pools.find((p) => p.nodes > 0)!
    expect(busy.members.length).toBeGreaterThan(0)
    expect((await call('DELETE', `/v1/node-pools/${busy.id}`)).status).toBe(409)
    expect(await (await call('DELETE', `/v1/node-pools/${pools.find((p) => p.code === 'enterprise')!.id}`)).json()).toMatchObject({ error: { message: '还有未使用的引导令牌绑定这个分组，请等待令牌过期后再删除' } })
    expect((await call('DELETE', `/v1/node-pools/${id}`)).status).toBe(200)
  })

  it('publishes global routing with revision checks and referenced-outbound 409', async () => {
    const g = globalRoutingSchema.parse(await (await call('GET', '/v1/nodes/routing')).json())
    expect(g.revision).toMatch(/^[0-9a-f]{64}$/)
    const put = (body: unknown, key: string) => call('PUT', '/v1/nodes/routing', body, key)
    expect((await put({ expected_revision: 'stale', outbounds: g.outbounds, routes: g.routes }, 'g-1')).status).toBe(409)
    // 香港 01 的私有规则指向 US-LAX-01，删掉它要 409 并列出节点
    const dropped = await put({ expected_revision: g.revision, outbounds: g.outbounds.filter((o) => o.tag !== 'US-LAX-01'), routes: g.routes.filter((r) => r.outbound_tag !== 'US-LAX-01') }, 'g-2')
    expect(await dropped.json()).toMatchObject({ error: { code: 'conflict', message: '要删除的全局出站仍被节点规则引用：香港 01 · 原生' } })
    const bad = await put({ expected_revision: g.revision, outbounds: [], routes: [{ matcher: { geosite: ['cn'] }, outbound_tag: 'direct', enabled: true }] }, 'g-3')
    expect(await bad.json()).toMatchObject({ error: { fields: { routes: '第 1 条规则无法跨内核下发：不支持的匹配类型' } } })
    const ok = await put({ expected_revision: g.revision, outbounds: g.outbounds, routes: [{ priority: 10, matcher: { port: [25] }, outbound_tag: 'block', enabled: true, note: '' }] }, 'g-4')
    expect(ok.status).toBe(200)
    const saved = (await ok.json()) as { revision: string; affected_nodes: number }
    expect(saved.revision).not.toBe(g.revision)
    expect(saved.affected_nodes).toBeGreaterThan(0)
    expect((await call('GET', '/v1/nodes/routing').then((r) => r.json())) as { routes: unknown[] }).toMatchObject({ revision: saved.revision, routes: [{ outbound_tag: 'block' }] })
  })
})

describe('mock api · admin content', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    const res = await fetch(`${base}/v1/auth/login`, { method: 'POST', body: JSON.stringify(MOCK_ACCOUNTS.admin) })
    auth = { Authorization: `Bearer ${((await res.json()) as { access_token: string }).access_token}` }
  })
  afterAll(() => new Promise<void>((resolve) => server.close(() => resolve())))

  const call = (method: string, path: string, body?: unknown, key?: string) =>
    fetch(`${base}${path}`, { method, headers: { ...auth, ...(key ? { 'Idempotency-Key': key } : {}) }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) })
  const anns = async () => announcementsResponse.parse(await (await call('GET', '/v1/announcements')).json())
  const draft = { title: '测试公告', body: '测试正文', severity: 'notice', pinned: false, target_plan_ids: [], target_user_group_ids: [], publish_at: '', expires_at: '', publish: false, expected_version: 0 }

  it('hides the content module from the viewer', async () => {
    const res = await fetch(`${base}/v1/auth/login`, { method: 'POST', body: JSON.stringify(MOCK_ACCOUNTS.viewer) })
    const viewer = { Authorization: `Bearer ${((await res.json()) as { access_token: string }).access_token}` }
    for (const path of ['/v1/announcements', '/v1/content-pages', '/v1/themes', '/v1/slots', '/v1/settings/site']) expect((await fetch(`${base}${path}`, { headers: viewer })).status, path).toBe(404)
  })

  it('walks an announcement from draft to scheduled, published and withdrawn', async () => {
    const data = await anns()
    expect(data.announcements.map((a) => a.status)).toEqual(expect.arrayContaining(['draft', 'scheduled', 'published', 'withdrawn']))
    expect(data.user_groups.length).toBeGreaterThan(0)
    expect((await call('POST', '/v1/announcements', { ...draft, audience: 'all' }, 'a-0')).status).toBe(400)
    expect(await (await call('POST', '/v1/announcements', { ...draft, title: '短' }, 'a-1')).json()).toMatchObject({ error: { fields: { title: expect.any(String) } } })
    expect(await (await call('POST', '/v1/announcements', { ...draft, publish_at: '2026-10-01 10:00' }, 'a-2')).json()).toMatchObject({ error: { message: '时间必须是带时区的 RFC3339 格式' } })
    expect(await (await call('POST', '/v1/announcements', { ...draft, target_user_group_ids: ['00000000-0000-4000-8000-000000000000'] }, 'a-3')).json()).toMatchObject({ error: { fields: { target_user_group_ids: expect.any(String) } } })

    // 编辑是全量覆盖：之后每次都要带上用户组
    const mine = { ...draft, target_user_group_ids: [data.user_groups[0]!.id] }
    const created = annSaved.parse(await (await call('POST', '/v1/announcements', mine, 'a-4')).json())
    expect(created).toMatchObject({ status: 'draft', version: 1 })
    const future = new Date(Date.now() + 86_400_000).toISOString()
    const scheduled = annSaved.parse(await (await call('POST', `/v1/announcements/${created.id}`, { ...mine, publish: true, publish_at: future, expected_version: 1 }, 'a-5')).json())
    expect(scheduled).toMatchObject({ status: 'scheduled', version: 2 })
    expect((await call('POST', `/v1/announcements/${created.id}`, { ...mine, publish: true, expected_version: 1 }, 'a-6')).status).toBe(409)
    const published = annSaved.parse(await (await call('POST', `/v1/announcements/${created.id}`, { ...mine, publish: true, expected_version: 2 }, 'a-7')).json())
    expect(published.status).toBe('published')
    const row = (await anns()).announcements.find((a) => a.id === created.id)!
    expect(row).toMatchObject({ user_group_targets: [{ id: data.user_groups[0]!.id }], version: 3 })
    expect(row.publish_at).not.toBeNull()
    expect(await (await call('POST', `/v1/announcements/${created.id}`, { ...mine, expected_version: 3 }, 'a-8')).json()).toMatchObject({ error: { code: 'conflict', message: '已发布公告只能保持发布状态；如需下线请使用撤回' } })
    expect((await call('POST', `/v1/announcements/${created.id}/withdraw`, { expected_version: 3 }, 'a-9')).status).toBe(200)
    expect(await (await call('POST', `/v1/announcements/${created.id}/withdraw`, { expected_version: 4 }, 'a-10')).json()).toMatchObject({ error: { message: '这条公告已经撤回过了' } })
    expect(await (await call('POST', `/v1/announcements/${created.id}`, { ...mine, publish: true, expected_version: 4 }, 'a-11')).json()).toMatchObject({ error: { message: '已撤回公告不可重新编辑，请新建公告' } })
    expect((await call('POST', '/v1/announcements/not-a-uuid', { ...draft, expected_version: 1 }, 'a-12')).status).toBe(404)
  })

  it('saves knowledge-base versions, archiving the same-audience published one', async () => {
    const list = async () => pagesResponse.parse(await (await call('GET', '/v1/content-pages?kind=kb_article&limit=500')).json()).pages
    const rows = (await list()).filter((p) => p.slug === 'gift-card')
    expect(rows.every((p) => p.created_by_name && p.latest_version === 2 && p.body === undefined)).toBe(true)
    const published = rows.find((p) => p.status === 'published')!
    const detail = pageResponse.parse(await (await call('GET', `/v1/content-pages/${published.id}`)).json()).page
    expect(detail.body).toContain('礼品卡')
    expect(detail.created_by_name).toBeUndefined()
    const body = { slug: 'gift-card', kind: 'kb_article', category: '账单与支付', title: '如何使用礼品卡', summary: '', body: `${detail.body}\n\n补充一句。`, locale: 'zh-CN', target_platforms: [], min_client_version: '', max_client_version: '', target_plan_ids: [], visibility: 'authenticated', status: 'published', expected_latest_version: 1 }
    expect(await (await call('POST', '/v1/content-pages', body, 'k-1')).json()).toMatchObject({ error: { message: '内容已产生新版本，请刷新后重试' } })
    expect(await (await call('POST', '/v1/content-pages', { ...body, slug: 'Bad Slug', body: '短', expected_latest_version: 0 }, 'k-2')).json()).toMatchObject({ error: { fields: { slug: expect.any(String), body: expect.any(String) } } })
    expect((await call('POST', '/v1/content-pages', { ...body, review_due_at: '' }, 'k-3')).status).toBe(400)
    const saved = await call('POST', '/v1/content-pages', { ...body, expected_latest_version: 2 }, 'k-4')
    expect(saved.status).toBe(201)
    expect(pageSaved.parse(await saved.json()).page).toMatchObject({ version: 3, status: 'published' })
    const after = (await list()).filter((p) => p.slug === 'gift-card')
    expect(after.filter((p) => p.status === 'published').map((p) => p.version)).toEqual([3])
    const archived = pageArchived.parse(await (await call('POST', `/v1/content-pages/${after[0]!.id}/archive`, { expected_version: 3 }, 'k-5')).json())
    expect(archived.page.already_archived).toBe(false)
    const again = pageArchived.parse(await (await call('POST', `/v1/content-pages/${after[0]!.id}/archive`, { expected_version: 3 }, 'k-6')).json())
    expect(again.page.already_archived).toBe(true)
    expect((await call('POST', `/v1/content-pages/${after[0]!.id}/archive`, { expected_version: 2 }, 'k-7')).status).toBe(409)
  })

  it('serves the paper theme, sanitizes slots and validates the site timezone', async () => {
    const themes = themesResponse.parse(await (await call('GET', '/v1/themes')).json()).themes!
    expect(themes).toHaveLength(1)
    expect(themes[0]).toMatchObject({ code: 'paper', is_active: true, custom_css: '' })
    expect(Object.keys(themes[0]!.tokens.light)).toHaveLength(43)
    expect(slotsResponse.parse(await (await call('GET', '/v1/slots')).json()).slots).toHaveLength(7)
    const dirty = slotSaved.parse(await (await call('POST', '/v1/slots/portal.home.banner', { content: '<b onclick="x()">欢迎</b><script>alert(1)</script>', enabled: true }, 's-1')).json())
    expect(dirty.dropped).toHaveLength(2)
    expect(slotsResponse.parse(await (await call('GET', '/v1/slots')).json()).slots.find((s) => s.key === 'portal.home.banner')).toMatchObject({ content: '<b>欢迎</b>', enabled: true })
    expect(slotSaved.parse(await (await call('POST', '/v1/slots/portal.home.banner', { content: '', enabled: false }, 's-2')).json()).dropped).toBeNull()
    expect(await (await call('POST', '/v1/slots/portal.nope', { content: '', enabled: false }, 's-3')).json()).toMatchObject({ error: { fields: { key: '未知的插槽位' } } })
    expect(siteSettingsSchema.parse(await (await call('GET', '/v1/settings/site')).json()).timezone).toBe('Asia/Shanghai')
    for (const timezone of ['', 'Local', 'Mars/Base']) expect(await (await call('POST', '/v1/settings/site', { timezone })).json()).toMatchObject({ error: { fields: { timezone: '不是有效的时区' } } })
    expect(await (await call('POST', '/v1/settings/site', { timezone: 'Asia/Tokyo' })).json()).toEqual({ timezone: 'Asia/Tokyo' })
  })
})

describe('mock api · admin plans', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    const res = await fetch(`${base}/v1/auth/login`, { method: 'POST', body: JSON.stringify(MOCK_ACCOUNTS.admin) })
    auth = { Authorization: `Bearer ${((await res.json()) as { access_token: string }).access_token}` }
  })
  afterAll(() => {
    setSalesEnabled(true)
    return new Promise<void>((resolve) => server.close(() => resolve()))
  })

  // users 假后端的固定套餐 id：标准版，批量筛选按套餐能命中
  const STD = '9c0e1a2b-3333-4b00-8000-000000000001'
  const get = (path: string) => fetch(`${base}${path}`, { headers: auth })
  const send = (method: string, path: string, body: unknown, key?: string) =>
    fetch(`${base}${path}`, { method, headers: { ...auth, ...(key ? { 'Idempotency-Key': key } : {}) }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) })
  const detailOf = async (id: string) => planResponseSchema.parse(await (await get(`/v1/plans/${id}`)).json()).plan
  const poolIds = async () => {
    const res = (await (await get(`/v1/plans/${STD}/pools`)).json()) as { pools: Array<{ id: string; name: string; active_nodes: number }> }
    return { live: res.pools.find((p) => p.active_nodes > 0)!.id, empty: res.pools.find((p) => p.active_nodes === 0)!.id }
  }

  it('serves a catalog the page schemas accept, on the users seed plan ids', async () => {
    const list = plansSchema.parse(await (await get('/v1/plans')).json()).plans
    expect(list.map((p) => p.code)).toEqual(['std', 'pro', 'family', 'trial', 'ent-line'])
    const std = list.find((p) => p.id === STD)!
    expect(std.active_subscriptions).toBeGreaterThan(0)
    expect(std.node_count).toBeGreaterThan(0)
    expect(list.find((p) => p.code === 'pro')!.draft_version_id).not.toBeNull()
    expect(list.find((p) => p.code === 'ent-line')).toMatchObject({ status: 'draft', current_version_id: null })
    const d = await detailOf(STD)
    expect(d.versions[0]!.version).toBeGreaterThan(d.versions[1]!.version)
    expect(planPoolsSchema.parse(await (await get(`/v1/plans/${STD}/pools`)).json()).editable).toBe(false)
    expect(packsSchema.parse(await (await get('/v1/traffic-packs?status=archived')).json()).packs.every((p) => p.status === 'archived')).toBe(true)
  })

  it('creates through the wizard in one transaction, replays, and validates like the Go wizard', async () => {
    const { live, empty } = await poolIds()
    const before = plansSchema.parse(await (await get('/v1/plans')).json()).plans.length
    const bad = await send(
      'POST',
      '/v1/plans/complete',
      { code: 'wiz', name: '向导', prices: [{ billing_interval: 'month', interval_count: 1, unit_amount: 0, currency: 'CNY' }], publish: true },
      'wiz-0',
    )
    expect(await bad.json()).toMatchObject({ error: { fields: { 'prices.0.unit_amount': expect.any(String), pool_ids: expect.any(String) } } })
    // 池里没有可服务节点：发布失败，库里什么也不留（R65）
    const body = {
      code: 'wiz',
      name: '向导',
      traffic_gb: 100,
      max_devices: 2,
      pool_ids: [empty],
      prices: [{ billing_interval: 'month', interval_count: 1, unit_amount: 1900, currency: 'CNY', trial_days: 0 }],
      publish: true,
    }
    const noNodes = await send('POST', '/v1/plans/complete', body, 'wiz-1')
    expect(await noNodes.json()).toMatchObject({ error: { fields: { pool_ids: expect.any(String) } } })
    expect(plansSchema.parse(await (await get('/v1/plans')).json()).plans).toHaveLength(before)

    const ok = { ...body, pool_ids: [live] }
    const created = await send('POST', '/v1/plans/complete', ok, 'wiz-2')
    expect(created.status).toBe(201)
    const r = planCreatedSchema.parse(await created.json())
    expect(r).toMatchObject({ published: true, plan: { status: 'active', code: 'wiz' } })
    expect(r.plan.current_version_id).toBe(r.version_id)
    expect(planCreatedSchema.parse(await (await send('POST', '/v1/plans/complete', ok, 'wiz-2')).json()).plan.id).toBe(r.plan.id)
    expect((await send('POST', '/v1/plans/complete', ok, 'wiz-3')).status).toBe(409)
    expect((await send('POST', '/v1/plans/complete', { ...ok, code: 'wiz-x', max_devices: 0 }, 'wiz-4')).status).toBe(422)
  })

  it('edits through the wizard: null leaves things alone, quota changes roll a published version', async () => {
    const d = await detailOf(STD)
    const basics = {
      code: d.code,
      name: d.name,
      description: d.description,
      visibility: d.visibility,
      sort_order: d.sort_order,
      visible_group_ids: [],
      purchase_limit_per_user: null,
      stock_total: null,
    }
    const stale = await send('PUT', `/v1/plans/${STD}/complete`, { ...basics, expected_row_version: d.row_version - 1 }, 'edit-0')
    expect(await stale.json()).toMatchObject({ error: { code: 'conflict', fields: { row_version: `current=${d.row_version}` } } })
    const res = await send('PUT', `/v1/plans/${STD}/complete`, { ...basics, expected_row_version: d.row_version, traffic_gb: 300, prices: null, pool_ids: null }, 'edit-1')
    const r = planUpdatedSchema.parse(await res.json())
    expect(r.changed).toHaveLength(2)
    const cur = r.plan.versions.find((v) => v.id === r.plan.current_version_id)!
    expect(cur.quotas.find((q) => q.metric === 'traffic.bytes')!.limit).toBe(300 * 1024 ** 3)
    expect(cur.entitlements).toEqual([])
    expect(r.plan.prices).toEqual(d.prices)
  })

  it('walks a draft version: create, reject pool_ids and a stray throttle, save, bind, publish', async () => {
    const d = await detailOf(STD)
    const v = versionCreatedSchema.parse(await (await send('POST', `/v1/plans/${STD}/versions`, undefined, 'ver-1')).json()).version
    expect([v.status, v.quotas, v.pool_ids]).toEqual(['draft', [], []])
    expect((await send('POST', `/v1/plans/${STD}/versions`, undefined, 'ver-2')).status).toBe(409)
    const cur = d.versions.find((x) => x.id === d.current_version_id)!
    const semantics = { ...cur, expected_row_version: v.row_version }
    for (const k of ['id', 'version', 'status', 'frozen_at', 'row_version', 'pool_ids', 'created_by_email', 'created_at'] as const) delete (semantics as Partial<typeof semantics>)[k]
    expect((await send('PUT', `/v1/plans/${STD}/versions/${v.id}`, { ...semantics, pool_ids: [] })).status).toBe(422)
    expect(await (await send('PUT', `/v1/plans/${STD}/versions/${v.id}`, { ...semantics, throttle_kbps: 5000 })).json()).toMatchObject({ error: { fields: { throttle_kbps: expect.any(String) } } })
    const saved = (await (await send('PUT', `/v1/plans/${STD}/versions/${v.id}`, semantics)).json()) as { row_version: number }
    const bound = await send('POST', `/v1/plans/${STD}/pools`, { version_id: v.id, expected_version_row_version: saved.row_version, pool_ids: cur.pool_ids }, 'bind-1')
    const { row_version } = (await bound.json()) as { row_version: number }
    const plan = await detailOf(STD)
    const pub = await send('POST', `/v1/plans/${STD}/versions/${v.id}/publish`, { expected_plan_row_version: plan.row_version, expected_version_row_version: row_version }, 'pub-1')
    expect(pub.status).toBe(200)
    expect((await detailOf(STD)).current_version_id).toBe(v.id)
  })

  it('adds and archives prices, and answers 503 for catalog.publish writes while sales are off', async () => {
    const add = { currency: 'USD', unit_amount: 3000, billing_interval: 'year', interval_count: 1 }
    const created = priceCreatedSchema.parse(await (await send('POST', `/v1/plans/${STD}/prices`, add, 'price-1')).json()).price
    expect((await send('POST', `/v1/plans/${STD}/prices`, add, 'price-2')).status).toBe(409)
    expect((await send('POST', `/v1/plans/${STD}/prices`, { ...add, currency: 'EUR' }, 'price-3')).status).toBe(422)
    const archive = (key: string) => send('POST', `/v1/plans/${STD}/prices/${created.id}/archive`, { expected_row_version: created.row_version }, key)
    expect((await archive('arch-1')).status).toBe(200)
    expect(await (await archive('arch-2')).json()).toMatchObject({ error: { message: '价格已经归档' } })

    setSalesEnabled(false)
    const off = await send('POST', `/v1/plans/${STD}/prices`, { ...add, billing_interval: 'month' }, 'price-4')
    expect(off.status).toBe(503)
    expect(await off.json()).toMatchObject({ error: { code: 'service_unavailable' } })
    const pack = packsSchema.parse(await (await get('/v1/traffic-packs?status=active')).json()).packs[0]!
    const down = await send('POST', `/v1/traffic-packs/${pack.id}/status`, { status: 'archived', expected_updated_at: pack.updated_at }, 'pack-off')
    const archived = packResponseSchema.parse(await down.json()).pack
    expect((await send('POST', `/v1/traffic-packs/${pack.id}/status`, { status: 'active', expected_updated_at: archived.updated_at }, 'pack-on')).status).toBe(503)
    setSalesEnabled(true)
    expect((await send('POST', `/v1/traffic-packs/${pack.id}/status`, { status: 'active', expected_updated_at: archived.updated_at }, 'pack-on')).status).toBe(200)
  })

  it('creates and edits traffic packs with the updated_at lock and rejects unknown fields', async () => {
    const body = { name: '20 GB 小包', traffic_bytes: 20 * 1024 ** 3, currency: 'CNY', unit_amount: 800, recommended: false, sort_order: 5 }
    expect((await send('POST', '/v1/traffic-packs', { ...body, stock: 1 }, 'tp-0')).status).toBe(400)
    const pack = packResponseSchema.parse(await (await send('POST', '/v1/traffic-packs', body, 'tp-1')).json()).pack
    expect(pack).toMatchObject({ status: 'active', sold_count: 0 })
    const edited = packResponseSchema.parse(await (await send('PUT', `/v1/traffic-packs/${pack.id}`, { ...body, unit_amount: 900, expected_updated_at: pack.updated_at }, 'tp-2')).json()).pack
    expect(edited.updated_at).not.toBe(pack.updated_at)
    const stale = await send('PUT', `/v1/traffic-packs/${pack.id}`, { ...body, expected_updated_at: pack.updated_at }, 'tp-3')
    expect(await stale.json()).toMatchObject({ error: { code: 'conflict', fields: { updated_at: `current=${edited.updated_at}` } } })
    expect((await send('POST', `/v1/traffic-packs/${pack.id}/status`, { status: 'active', expected_updated_at: edited.updated_at }, 'tp-4')).status).toBe(409)
  })
})

describe('mock api · portal', () => {
  let server: Server
  let base: string
  beforeAll(async () => ({ server, base } = await serve('portal')))
  afterAll(() => new Promise<void>((resolve) => server.close(() => resolve())))

  it('serves the shell top-bar reads from the page modules and round-trips a quick-login token', async () => {
    const login = await fetch(`${base}/v1/auth/login`, { method: 'POST', body: JSON.stringify(MOCK_ACCOUNTS.portal) })
    const { access_token } = (await login.json()) as { access_token: string }
    const auth = { Authorization: `Bearer ${access_token}` }
    for (const path of ['/v1/me', '/v1/me/balance', '/v1/me/subscriptions', '/v1/me/commission', '/v1/me/notifications']) {
      expect((await fetch(`${base}${path}`, { headers: auth })).status, path).toBe(200)
    }
    const issued = await fetch(`${base}/v1/me/quick-login`, { method: 'POST', headers: auth })
    const { token } = (await issued.json()) as { token: string }
    const consume = () => fetch(`${base}/v1/auth/quick-login`, { method: 'POST', body: JSON.stringify({ token }) })
    expect((await consume()).status).toBe(200)
    expect((await consume()).status).toBe(401)
  })
})
