/**
 * [INPUT]: 依赖 vitest，依赖 ./mock-helpers，依赖 ../dev/mock-api 的 MOCK_ACCOUNTS，依赖 ../dev/mock/admin/nodes-infra 的 activeNodesInPool，依赖 ../src/admin/screens/nodes/schemas 的节点 / 服务器 / 节点池 / 全局路由 schema
 * [OUTPUT]: 对外提供节点与服务器（后台-07）假接口的测试
 * [POS]: tests 的节点假后端守卫：节点列表能被页面 schema 接住、读不回敏感键、复制出新节点、非法状态边与已部署节点迁移回 409、协议按 schema 校验；服务器能被页面 schema 接住、状态机与进入 ready 的前提、PATCH 清空与容量下限、删除仅草稿或已退役并级联静默名下节点、安装令牌幂等；节点池新建 / 编辑 / 删除守卫与按池在线数同口径；全局路由 revision 冲突、删除被引用出站 409、匹配类型校验与发布
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { storedProtocolConfig } from '../dev/mock/admin/nodes'
import { activeNodesInPool } from '../dev/mock/admin/nodes-infra'
import { activatedResponse, globalRoutingSchema, nodesResponse, poolsResponse, serverSchema, serversResponse } from '../src/admin/screens/nodes/schemas'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · admin nodes', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
  })
  afterAll(() => close(server))

  const call = (method: string, path: string, body?: unknown, key?: string) => mockFetch(base, auth, method, path, body, key)
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
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
  })
  afterAll(() => close(server))

  const call = (method: string, path: string, body?: unknown, key?: string) => mockFetch(base, auth, method, path, body, key)
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
    // 套餐假后端要读的按池在线数与列表的 active_nodes 同一口径
    expect(pools.every((p) => p.active_nodes === activeNodesInPool(p.id))).toBe(true)
    expect(pools.some((p) => p.active_nodes > 0)).toBe(true)
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

describe('mock api · admin nodes · phase 4 step 3 (R104 R105 R106 R107 R108)', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
  })
  afterAll(() => close(server))

  const call = (method: string, path: string, body?: unknown, key?: string) => mockFetch(base, auth, method, path, body, key)
  const list = async () => nodesResponse.parse(await (await call('GET', '/v1/nodes?include_retired=1')).json()).nodes
  const pools = async () => poolsResponse.parse(await (await call('GET', '/v1/node-pools')).json()).pools
  const reauth = async () => {
    const res = await fetch(`${base}/v1/auth/reauth`, { method: 'POST', headers: auth, body: JSON.stringify({ password: MOCK_ACCOUNTS.admin.password }) })
    auth = bearer(((await res.json()) as { access_token: string }).access_token)
  }
  const VIP = '9c0e1a2b-2222-4b00-8000-000000000001'
  const TRIAL = '9c0e1a2b-2222-4b00-8000-000000000003'

  it('R104: pools carry allowed_user_groups, and only requests that send the list need reauth', async () => {
    const seeded = await pools()
    expect(seeded.find((p) => p.name === '企业专线')!.allowed_user_groups.map((g) => g.name)).toEqual(['企业客户'])
    await fetch(`${base}/__mock/expire-reauth`, { method: 'POST' })
    // 不带名单：改名、新建都不用 reauth
    const asia = seeded.find((p) => p.name === '亚太精选')!
    expect((await call('POST', `/v1/node-pools/${asia.id}`, { region: 'APAC-2' })).status).toBe(200)
    // 带了名单：先 403，什么都不建
    const blocked = await call('POST', '/v1/node-pools', { name: '内测专线', allowed_user_group_ids: [TRIAL] })
    expect(blocked.status).toBe(403)
    expect((await pools()).some((p) => p.name === '内测专线')).toBe(false)
    await reauth()
    // 校验：格式、重复、上限、不存在
    for (const bad of [['nope'], [TRIAL, TRIAL], Array.from({ length: 101 }, () => TRIAL), ['9c0e1a2b-2222-4b00-8000-0000000000ff']]) {
      const r = await call('POST', '/v1/node-pools', { name: '内测专线', allowed_user_group_ids: bad })
      expect(await r.json()).toMatchObject({ error: { fields: { allowed_user_group_ids: expect.any(String) } } })
    }
    const created = (await (await call('POST', '/v1/node-pools', { name: '内测专线', allowed_user_group_ids: [TRIAL, VIP] })).json()) as { id: string }
    const mine = (await pools()).find((p) => p.id === created.id)!
    expect(mine.allowed_user_groups.map((g) => g.id).sort()).toEqual([TRIAL, VIP].sort())
    // 省略 = 不改，[] = 取消限定
    await call('POST', `/v1/node-pools/${created.id}`, { name: '内测专线 2' })
    expect((await pools()).find((p) => p.id === created.id)!.allowed_user_groups).toHaveLength(2)
    await call('POST', `/v1/node-pools/${created.id}`, { allowed_user_group_ids: [] })
    expect((await pools()).find((p) => p.id === created.id)!.allowed_user_groups).toEqual([])
  })

  it('R104: user groups list exclusive_pools, and a group named by a pool cannot be deleted (pool named first)', async () => {
    const groups = (await (await call('GET', '/v1/user-groups')).json()) as { groups: Array<{ id: string; name: string; exclusive_pools: Array<{ name: string }> }> }
    expect(groups.groups.find((g) => g.id === VIP)!.exclusive_pools.map((p) => p.name)).toContain('灰度池')
    const del = await call('DELETE', `/v1/user-groups/${VIP}`)
    expect(del.status).toBe(409)
    expect(((await del.json()) as { error: { message: string } }).error.message).toContain('灰度池')
  })

  it('R105: an active node without a pool is not delivered and says so', async () => {
    const row = (await list()).find((n) => n.name === '香港 03（未入池）')!
    expect(row).toMatchObject({ pool_id: null, delivered_to_users: false, delivery_note: '未划入节点池，不服务任何用户' })
  })

  it('R106 / R107: PATCH keeps absent secrets, honours explicit null, and mask_password follows mask', async () => {
    const n = (await list()).find((x) => x.name === '东京 03')!
    const base = { network: 'mkcp', tls: 2, reality_settings: { dest: 'www.apple.com:443', server_name: 'www.apple.com' }, mask: 'srtp' }
    let row = n.row_version
    const patch = async (config: unknown) => {
      const r = await call('PATCH', `/v1/nodes/${n.id}`, { row_version: row, protocol_config: config })
      expect(r.status).toBe(200)
      row = ((await r.json()) as { row_version: number }).row_version
      return storedProtocolConfig(n.id) as Record<string, unknown> & { reality_settings: Record<string, unknown> }
    }
    const stored = await patch({ ...base, reality_settings: { ...base.reality_settings, private_key: 'PK' }, mask_password: 'MP' })
    expect(stored).toMatchObject({ mask_password: 'MP', reality_settings: { private_key: 'PK' } })
    // 只改普通字段、敏感键缺席：补回
    expect(await patch({ ...base, mtu: 1350 })).toMatchObject({ mtu: 1350, mask_password: 'MP', reality_settings: { private_key: 'PK' } })
    // 关掉掩码（去掉 mask 键）：mask_password 不补
    const off = await patch({ network: 'mkcp', tls: 2, reality_settings: base.reality_settings })
    expect(off).not.toHaveProperty('mask_password')
    expect(off.reality_settings.private_key).toBe('PK')
    // 显式 null：以请求为准清空
    const cleared = await patch({ ...base, reality_settings: { ...base.reality_settings, private_key: null } })
    expect(cleared.reality_settings.private_key).toBeNull()
  })

  it('R108: activate walks an attesting node to active, readies its server, replays, and refuses others with 409', async () => {
    const n = (await list()).find((x) => x.name === '大阪 01（待上线）')!
    expect(n.status).toBe('attesting')
    const res = await call('POST', `/v1/nodes/${n.id}/activate`, { row_version: n.row_version }, 'act-1')
    expect(res.status).toBe(200)
    const body = activatedResponse.parse(await res.json())
    expect(body).toMatchObject({ status: 'active', serving_status: 'active' })
    // 同键重放拿到同一结果；已是 active 再上线是 200 不改动
    expect(activatedResponse.parse(await (await call('POST', `/v1/nodes/${n.id}/activate`, { row_version: n.row_version }, 'act-1')).json())).toEqual(body)
    expect((await call('POST', `/v1/nodes/${n.id}/activate`, { row_version: body.row_version }, 'act-2')).status).toBe(200)
    const srv = serversResponse.parse(await (await call('GET', '/v1/servers')).json()).servers.find((s) => s.id === n.server_id)!
    expect(srv.status).toBe('ready')
    // 版本冲突、终态
    const retired = (await list()).find((x) => x.serving_status === 'retired')!
    expect((await call('POST', `/v1/nodes/${n.id}/activate`, { row_version: 1 }, 'act-3')).status).toBe(409)
    const refused = await call('POST', `/v1/nodes/${retired.id}/activate`, { row_version: retired.row_version }, 'act-4')
    expect(refused.status).toBe(409)
    expect(((await refused.json()) as { error: { message: string } }).error.message).toContain('retired')
  })
})
