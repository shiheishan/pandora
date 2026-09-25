/**
 * [INPUT]: 依赖 vitest，依赖 ./mock-helpers，依赖 ../dev/mock-api 的 MOCK_ACCOUNTS，依赖 ../dev/mock/admin/nodes-infra 的 activeNodesInPool，依赖 ../src/admin/screens/nodes/schemas 的节点 / 服务器 / 节点池 / 全局路由 schema
 * [OUTPUT]: 对外提供节点与服务器（后台-07）假接口的测试
 * [POS]: tests 的节点假后端守卫：节点列表能被页面 schema 接住、读不回敏感键、复制出新节点、非法状态边与已部署节点迁移回 409、协议按 schema 校验；服务器能被页面 schema 接住、状态机与进入 ready 的前提、PATCH 清空与容量下限、删除仅草稿或已退役并级联静默名下节点、安装令牌幂等；节点池新建 / 编辑 / 删除守卫与按池在线数同口径；全局路由 revision 冲突、删除被引用出站 409、匹配类型校验与发布
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { activeNodesInPool } from '../dev/mock/admin/nodes-infra'
import { globalRoutingSchema, nodesResponse, poolsResponse, serverSchema, serversResponse } from '../src/admin/screens/nodes/schemas'
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
