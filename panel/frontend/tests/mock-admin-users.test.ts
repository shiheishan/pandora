/**
 * [INPUT]: 依赖 vitest，依赖 ./mock-helpers，依赖 ../dev/mock-api 的 MOCK_ACCOUNTS
 * [OUTPUT]: 对外提供用户（后台-03）第 ④ 步假接口的测试
 * [POS]: tests 的用户运营假后端守卫：流量重置先 reauth、清零与日志、重放、无生效订阅 422，批量预览 / 导出 / 生成同一份名单，用户组删除 409，设备模式校验
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · admin users ops', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
  })
  afterAll(() => close(server))

  const SEED_USER = '1a2b3c42-0000-4000-8000-000000000002'
  const get = (path: string) => mockFetch(base, auth, 'GET', path)
  const send = (method: string, path: string, body: unknown, key?: string) => mockFetch(base, auth, method, path, body, key)
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
