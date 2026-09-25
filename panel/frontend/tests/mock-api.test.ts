/**
 * [INPUT]: 依赖 vitest，依赖 ./mock-helpers 的 serve / close，依赖 ../dev/mock-api 的 MOCK_ACCOUNTS，依赖 ../dev/mock/types 的 matchPattern
 * [OUTPUT]: 对外提供假后端外壳与模块分发的测试
 * [POS]: tests 的假后端外壳守卫：matchPattern 的段匹配；外壳接口、模块分发、权限 404 先于 reauth、reauth 不消耗幂等键、同键重放与换请求 409、只重放 2xx（4xx 后同 key 重新执行、条件改好后成功，R85）、admin.writes 关闭后写接口 503（豁免切开关、auth 与改自己密码）——调账用 users 假后端的真实种子用户，余额经详情接口核对，种子外的 id 回 404；各页面会话往 dev/mock/ 里加接口时都依赖这几条行为。各模块的假接口测试在同目录的 mock-admin-*.test.ts 与 mock-portal.test.ts
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { matchPattern } from '../dev/mock/types'
import { close, serve } from './mock-helpers'

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
  afterAll(() => close(server))

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

  it('turns the admin gateway read-only while admin.writes is off, exempting switches, auth and own password', async () => {
    const { access_token } = await login(MOCK_ACCOUNTS.admin)
    const auth = { Authorization: `Bearer ${access_token}` }
    const toggle = (enabled: boolean) => fetch(`${base}/v1/switches/admin.writes`, { method: 'POST', headers: auth, body: JSON.stringify({ enabled, reason: enabled ? '' : '演练只读' }) })
    expect((await toggle(false)).status).toBe(200)
    const denied = await adjust(access_token, 'ro-1', { amount: 100, currency: 'CNY', reason: '只读模式演练' })
    expect(denied.status).toBe(503)
    expect(await denied.json()).toMatchObject({ error: { code: 'service_unavailable', message: '管理端只读模式' } })
    // 先于认证：没有令牌的写也是 503；读、登录与重认证照常
    expect((await fetch(`${base}/v1/users/${SEED_USER}/balance`, { method: 'POST', body: '{}' })).status).toBe(503)
    expect((await fetch(`${base}/v1/switches`, { headers: auth })).status).toBe(200)
    expect((await fetch(`${base}/v1/auth/reauth`, { method: 'POST', headers: auth, body: JSON.stringify({ password: MOCK_ACCOUNTS.admin.password }) })).status).toBe(200)
    await login(MOCK_ACCOUNTS.admin)
    // 切开关本身放行，否则关了就开不回来；恢复后同一个 key 重新执行（503 不重放）
    expect((await toggle(true)).status).toBe(200)
    expect((await adjust(access_token, 'ro-1', { amount: 100, currency: 'CNY', reason: '只读模式演练' })).status).toBe(200)
  })
})
