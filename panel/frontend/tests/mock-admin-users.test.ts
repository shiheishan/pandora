import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { generationJobSchema, generationJobsSchema, trafficGrantedSchema } from '../src/admin/screens/users/opsSchemas'
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

    // 批量生成是后台任务：202 回任务，进度轮询，完成后下载结果 CSV（只有提交人能下）
    const made = await send('POST', '/v1/users/bulk/generate', { count: 3, email_prefix: 'dealer', email_domain: 'example.com', reason: '线下渠道预制' }, 'gen-1')
    expect(made.status).toBe(202)
    const job = generationJobSchema.parse(await made.json())
    expect(job).toMatchObject({ total: 3, email_prefix: 'dealer', email_domain: 'example.com' })
    let progress = job
    for (let i = 0; i < 50 && progress.status !== 'succeeded'; i++) {
      await new Promise((r) => setTimeout(r, 20))
      progress = generationJobSchema.parse(await (await get(`/v1/users/bulk/generate/jobs/${job.id}`)).json())
    }
    expect(progress).toMatchObject({ status: 'succeeded', completed: 3, result_available: true })
    expect(generationJobsSchema.parse(await (await get('/v1/users/bulk/generate/jobs')).json()).jobs[0]!.id).toBe(job.id)
    const result = await get(`/v1/users/bulk/generate/jobs/${job.id}/result`)
    expect(result.headers.get('Content-Disposition')).toContain('generated-users.csv')
    const rows = new TextDecoder().decode(new Uint8Array(await result.arrayBuffer())).trim().split('\n')
    expect(rows[0]).toBe('邮箱,初始密码')
    expect(rows).toHaveLength(4)
    expect(rows[1]).toMatch(/^dealer-[a-z0-9]{8}@example\.com,/)
    const listed = (await (await get('/v1/users?q=dealer-')).json()) as { total: number }
    expect(listed.total).toBe(3)
    const bad = await send('POST', '/v1/users/bulk/generate', { count: 3, email_prefix: 'dealer', email_domain: 'example.com', reason: '线下渠道预制', group_id: '9c0e1a2b-2222-4b00-8000-00000000000f' }, 'gen-2')
    expect(await bad.json()).toMatchObject({ error: { fields: { group_id: '分组不存在' } } })
  })

  it('grants a non-expiring traffic pack to the subscription owner', async () => {
    const d = (await (await get(`/v1/users/${SEED_USER}`)).json()) as { subscriptions: Array<{ id: string }> }
    const sub = d.subscriptions[0]!.id
    const short = await send('POST', `/v1/subscriptions/${sub}/traffic-pack`, { bytes: 1024, reason: '短' }, 'tp-0')
    expect(short.status).toBe(422)
    expect(await short.json()).toMatchObject({ error: { fields: { reason: expect.any(String) } } })
    const granted = trafficGrantedSchema.parse(await (await send('POST', `/v1/subscriptions/${sub}/traffic-pack`, { bytes: 10 * 1024 ** 3, reason: '补偿线路故障' }, 'tp-1')).json())
    expect(granted).toMatchObject({ user_id: SEED_USER, granted_bytes: 10 * 1024 ** 3, remaining_bytes_total: 10 * 1024 ** 3 })
    // 同键重放回同一份结果，不再发一笔
    const replay = trafficGrantedSchema.parse(await (await send('POST', `/v1/subscriptions/${sub}/traffic-pack`, { bytes: 10 * 1024 ** 3, reason: '补偿线路故障' }, 'tp-1')).json())
    expect(replay.grant_id).toBe(granted.grant_id)
    expect((await send('POST', '/v1/subscriptions/00000000-0000-4000-8000-000000000000/traffic-pack', { bytes: 1, reason: '补偿线路故障' }, 'tp-2')).status).toBe(404)
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

  it('R103: the device window is one of 5 / 10 / 30 / 60 minutes, left alone when omitted', async () => {
    // 窗口的封闭取值由页面 schema 在 users/ops.test.ts 守着（users/api.ts 连着 React，节点侧 tsconfig 引不进来）
    const win = async () => ((await (await get('/v1/devices')).json()) as { window_minutes: number }).window_minutes
    expect(await win()).toBe(5)
    expect((await send('POST', '/v1/settings/device-limit', { mode: 'loose', window_minutes: 15 })).status).toBe(422)
    expect((await send('POST', '/v1/settings/device-limit', { mode: 'loose', window_minutes: 30 })).status).toBe(200)
    expect(await win()).toBe(30)
    expect((await send('POST', '/v1/settings/device-limit', { mode: 'strict', grace: 1 })).status).toBe(200)
    expect(await win()).toBe(30)
  })

  it('extends an active subscription only after reauth, replays the stored result and refuses the rest', async () => {
    type Detail = { subscriptions: Array<{ id: string; status: string; current_period_end: string | null }> }
    const detail = (await (await get(`/v1/users/${SEED_USER}`)).json()) as Detail
    const active = detail.subscriptions.find((s) => s.status === 'active' && s.current_period_end)!
    const path = `/v1/subscriptions/${active.id}/extend`
    const body = { days: 7, reason: '补偿线路故障' }

    // 只读账号没有 billing.adjustment.write：404，不暴露接口
    const viewer = bearer((await loginAs(base, MOCK_ACCOUNTS.viewer)).access_token)
    expect((await mockFetch(base, viewer, 'POST', path, body, 'extend-viewer')).status).toBe(404)

    await fetch(`${base}/__mock/expire-reauth`, { method: 'POST' })
    const blocked = await send('POST', path, body, 'extend-1')
    expect(blocked.status).toBe(403)
    expect(await blocked.json()).toMatchObject({ error: { code: 'reauth_required' } })
    const reauth = await fetch(`${base}/v1/auth/reauth`, { method: 'POST', headers: auth, body: JSON.stringify({ password: MOCK_ACCOUNTS.admin.password }) })
    auth = { Authorization: `Bearer ${((await reauth.json()) as { access_token: string }).access_token}` }

    expect((await send('POST', path, { ...body, note: 'x' }, 'extend-0')).status).toBe(400)
    expect(await (await send('POST', path, { days: 0, reason: '短' }, 'extend-0b')).json()).toMatchObject({
      error: { code: 'validation_failed', fields: { days: expect.any(String), reason: expect.any(String) } },
    })

    const done = await send('POST', path, body, 'extend-1')
    expect(done.status).toBe(200)
    const first = (await done.json()) as { subscription_id: string; days: number; previous_end: string; period_end: string }
    const base0 = Math.max(Date.parse(active.current_period_end!), Date.now())
    expect(first).toMatchObject({ subscription_id: active.id, days: 7, previous_end: active.current_period_end })
    expect(Math.abs(Date.parse(first.period_end) - (base0 + 7 * 86_400_000))).toBeLessThan(60_000)
    // 同键重放回同一份结果，不再多加 7 天
    expect(await (await send('POST', path, body, 'extend-1')).json()).toEqual(first)
    const after = (await (await get(`/v1/users/${SEED_USER}`)).json()) as Detail
    expect(after.subscriptions.find((s) => s.id === active.id)!.current_period_end).toBe(first.period_end)

    expect((await send('POST', '/v1/subscriptions/00000000-0000-4000-8000-000000000000/extend', body, 'extend-2')).status).toBe(404)
    const users = (await (await get('/v1/users?limit=100')).json()) as { users: Array<{ id: string }> }
    let ended: string | undefined
    for (const u of users.users) {
      const d = (await (await get(`/v1/users/${u.id}`)).json()) as Detail
      ended = d.subscriptions.find((s) => s.status === 'expired' || s.status === 'cancelled')?.id
      if (ended) break
    }
    const refused = await send('POST', `/v1/subscriptions/${ended!}/extend`, body, 'extend-3')
    expect(refused.status).toBe(409)
    expect(await refused.json()).toMatchObject({ error: { code: 'conflict' } })
  })

  it('sets a new password without a reason (R101) but still caps a given reason at 500', async () => {
    const fresh = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
    const reset = (body: unknown) => mockFetch(base, fresh, 'POST', `/v1/users/${SEED_USER}/reset-password`, body)
    expect(await (await reset({ new_password: 'newpass2026' })).json()).toEqual({ ok: true, sessions_revoked: true })
    expect((await reset({ new_password: 'newpass2026', reason: '' })).status).toBe(200)
    expect(await (await reset({ new_password: 'newpass2026', reason: '长'.repeat(501) })).json()).toMatchObject({ error: { code: 'validation_failed', fields: { reason: expect.any(String) } } })
    expect(await (await reset({ new_password: 'short' })).json()).toMatchObject({ error: { fields: { password: expect.any(String) } } })
  })
})
