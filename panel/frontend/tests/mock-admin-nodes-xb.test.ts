import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { adminNodeSchema, echSchema, nodesResponse, relaySchema } from '../src/admin/screens/nodes/schemas'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · admin nodes xb', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
  })
  afterAll(() => close(server))

  const call = (method: string, path: string, body?: unknown, key?: string) => mockFetch(base, auth, method, path, body, key)

  const reauthHeaders = async () => {
    await fetch(`${base}/__mock/expire-reauth`, { method: 'POST' })
    const reauth = await fetch(`${base}/v1/auth/reauth`, { method: 'POST', headers: auth, body: JSON.stringify({ password: MOCK_ACCOUNTS.admin.password }) })
    return bearer(((await reauth.json()) as { access_token: string }).access_token)
  }

  const list = async () => nodesResponse.parse(await (await call('GET', '/v1/nodes?include_retired=1')).json()).nodes

  it('lists xb fields on seed rows and traffic-exceeded delivery', async () => {
    const rows = await list()
    const hk1 = rows.find((n) => n.name === '香港 01 · 原生')!
    expect(hk1.tags).toEqual(['香港', '原生'])
    expect(hk1.traffic.limit_bytes).toBe(2 * 1024 ** 4)
    expect(hk1.traffic.used_bytes).toBeCloseTo(1.2 * 1024 ** 4, -6)
    expect(hk1.relay_count).toBe(0)
    expect(hk1.ech_enabled).toBe(false)
    const hk2 = rows.find((n) => n.name === '香港 02 · IPLC')!
    expect(hk2.relay_count).toBe(2)
    // 洛杉矶 01 是超限演示；新加坡 02 留作「心跳超时仍下发」的演示，不叠超限
    const la = rows.find((n) => n.name === '洛杉矶 01')!
    expect(la.traffic.exceeded_at).not.toBeNull()
    expect(la.delivered_to_users).toBe(false)
    expect(la.delivery_note).toBe(
      '本周期流量已达上限，已停止下发，节点端也停掉了它的入站；到重置日自动恢复，也可以在节点设置里手动重置或调高上限',
    )
    const ech = rows.find((n) => n.name === '法兰克福 01 · ECH')!
    expect(ech.ech_enabled).toBe(true)
  })

  it('filters list by tag and validates tag query', async () => {
    const tagged = nodesResponse.parse(await (await call('GET', '/v1/nodes?tag=香港')).json())
    expect(tagged.nodes.length).toBeGreaterThan(0)
    expect(tagged.nodes.every((n) => n.tags.includes('香港'))).toBe(true)
    const bad = await call('GET', `/v1/nodes?tag=${'长'.repeat(17)}`)
    expect(bad.status).toBe(422)
    expect(await bad.json()).toMatchObject({ error: { fields: { tag: '标签最多 16 个字、不能含控制字符' } } })
  })

  it('patches tags, limits, schedule; rejects overlap and unknown fields', async () => {
    const [n] = await list()
    const ok = await call('PATCH', `/v1/nodes/${n!.id}`, { row_version: n!.row_version, tags: ['测试'], traffic_limit_bytes: 1024, traffic_reset_day: 15 })
    expect(ok.status).toBe(200)
    adminNodeSchema.parse(await ok.json())
    const overlap = await call('PATCH', `/v1/nodes/${n!.id}`, {
      row_version: n!.row_version + 1,
      traffic_rate_schedule: [{ start: '08:00', end: '12:00', rate: 1 }, { start: '10:00', end: '14:00', rate: 1 }],
    })
    expect(overlap.status).toBe(422)
    expect(await overlap.json()).toMatchObject({ error: { fields: { traffic_rate_schedule: expect.stringMatching(/重叠/) } } })
    const unk = await call('PATCH', `/v1/nodes/${n!.id}`, { row_version: n!.row_version + 1, bogus: 1 })
    expect(unk.status).toBe(400)
  })

  it('manages relays and enforces the per-node cap', async () => {
    const parent = (await list()).find((n) => n.name === '香港 02 · IPLC')!
    const created = await call(
      'POST',
      `/v1/nodes/${parent.id}/relays`,
      { name: '临时中转', server_host: 'relay-tmp.example.test', server_port: 10443 },
      'relay-create-1',
    )
    expect(created.status).toBe(201)
    const relay = relaySchema.parse(await created.json())
    const listed = (await (await call('GET', `/v1/nodes/${parent.id}/relays`)).json()) as { relays: unknown[] }
    expect(listed.relays.length).toBe(3)
    const stale = await call('PATCH', `/v1/node-relays/${relay.id}`, { row_version: 999, name: 'x' })
    expect(stale.status).toBe(409)
    const del = await call('DELETE', `/v1/node-relays/${relay.id}`, { row_version: relay.row_version })
    expect(del.status).toBe(200)
    const draft = (await list()).find((n) => n.name === '新加坡 03（草稿）')!
    let count = (await (await call('GET', `/v1/nodes/${draft.id}/relays`)).json() as { relays: unknown[] }).relays.length
    while (count < 20) {
      const r = await call(
        'POST',
        `/v1/nodes/${draft.id}/relays`,
        { name: `r${count}`, server_host: `r${count}.example.test`, server_port: 20000 + count },
        `relay-fill-${count}`,
      )
      expect(r.status).toBe(201)
      count += 1
    }
    const full = await call('POST', `/v1/nodes/${draft.id}/relays`, { name: 'one-more', server_host: 'x.example.test', server_port: 30000 }, 'relay-21')
    expect(full.status).toBe(409)
    expect(await full.json()).toMatchObject({ error: { message: expect.stringMatching(/20/) } })
  })

  it('resets traffic after reauth and blocks without it', async () => {
    const sg = (await list()).find((n) => n.name === '洛杉矶 01')!
    await fetch(`${base}/__mock/expire-reauth`, { method: 'POST' })
    const blocked = await call('POST', `/v1/nodes/${sg.id}/traffic/reset`, { reason: '测试' }, 'reset-1')
    expect(blocked.status).toBe(403)
    expect(await blocked.json()).toMatchObject({ error: { code: 'reauth_required' } })
    const reauth = await fetch(`${base}/v1/auth/reauth`, {
      method: 'POST',
      headers: auth,
      body: JSON.stringify({ password: MOCK_ACCOUNTS.admin.password }),
    })
    const fresh = bearer(((await reauth.json()) as { access_token: string }).access_token)
    const ok = await mockFetch(base, fresh, 'POST', `/v1/nodes/${sg.id}/traffic/reset`, { reason: '测试清零' }, 'reset-2')
    expect(ok.status).toBe(200)
    const row = (await list()).find((n) => n.id === sg.id)!
    expect(row.traffic.used_bytes).toBe(0)
    expect(row.traffic.exceeded_at).toBeNull()
  })

  it('batch-edits tags and rolls back on version conflict or empty set', async () => {
    const rows = (await list()).filter((n) => n.name.startsWith('香港 0') && n.serving_status === 'active').slice(0, 2)
    expect(rows.length).toBe(2)
    const fresh = await reauthHeaders()
    const batch = await mockFetch(
      base,
      fresh,
      'POST',
      '/v1/nodes/edit:batch',
      { items: rows.map((n) => ({ id: n.id, row_version: n.row_version })), set: { tags: ['批量'] }, reason: '测试批量' },
      'batch-tags',
    )
    expect(batch.status).toBe(200)
    expect(await batch.json()).toEqual({ ok: true, updated: 2 })
    const after = (await list()).filter((n) => rows.some((r) => r.id === n.id))
    const badVer = await mockFetch(
      base,
      fresh,
      'POST',
      '/v1/nodes/edit:batch',
      {
        items: [{ id: after[0]!.id, row_version: after[0]!.row_version - 50 }, { id: after[1]!.id, row_version: after[1]!.row_version }],
        set: { tags: ['不应生效'] },
        reason: 'x',
      },
      'batch-conflict',
    )
    expect(badVer.status).toBe(409)
    expect(await badVer.json()).toMatchObject({ error: { fields: { node_id: after[0]!.id } } })
    expect((await list()).find((n) => n.id === after[1]!.id)!.tags).toEqual(['批量'])
    const empty = await mockFetch(base, fresh, 'POST', '/v1/nodes/edit:batch', { items: [{ id: after[0]!.id, row_version: after[0]!.row_version }], set: {}, reason: 'x' }, 'batch-empty')
    expect(empty.status).toBe(422)
    expect(await empty.json()).toMatchObject({ error: { fields: { set: '至少要改一个字段' } } })
  })

  it('gets, rotates and disables ECH', async () => {
    const echNode = (await list()).find((n) => n.name === '法兰克福 01 · ECH')!
    const ss = (await list()).find((n) => n.name === '新加坡 02')!
    const get0 = echSchema.parse(await (await call('GET', `/v1/nodes/${echNode.id}/ech`)).json())
    expect(get0.enabled).toBe(true)
    const fresh = await reauthHeaders()
    const rotated = echSchema.parse(
      await (await mockFetch(base, fresh, 'POST', `/v1/nodes/${echNode.id}/ech`, { public_name: 'ech.example.test' }, 'ech-rot')).json(),
    )
    expect(rotated.retired_keys).toBe(1)
    const denied = await mockFetch(base, fresh, 'POST', `/v1/nodes/${ss.id}/ech`, { public_name: 'x.example.test' }, 'ech-ss')
    expect(denied.status).toBe(409)
    const off = await mockFetch(base, fresh, 'DELETE', `/v1/nodes/${echNode.id}/ech`)
    expect(off.status).toBe(200)
    const get1 = echSchema.parse(await (await call('GET', `/v1/nodes/${echNode.id}/ech`)).json())
    expect(get1.enabled).toBe(false)
  })
})
