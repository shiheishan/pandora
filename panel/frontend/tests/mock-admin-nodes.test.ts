import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { storedProtocolConfig } from '../dev/mock/admin/nodes'
import { activeNodesInPool } from '../dev/mock/admin/nodes-infra'
import { adminNodeSchema, globalRoutingSchema, nodeDetailResponse, nodesResponse, poolsResponse, serverSchema, serversResponse } from '../src/admin/screens/nodes/schemas'
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
    // 列表不带编辑字段；按 id 单取才带，且读接口抹掉敏感键
    const raw = (await (await call('GET', '/v1/nodes')).json()) as { nodes: Array<Record<string, unknown>> }
    expect(raw.nodes.some((n) => 'protocol_config' in n || 'traffic_rate' in n)).toBe(false)
    const one = nodeDetailResponse.parse(await (await call('GET', `/v1/nodes?id=${rows[0]!.id}`)).json())
    expect(one.nodes).toHaveLength(1)
    expect(one.nodes[0]!.id).toBe(rows[0]!.id)
    expect(one.nodes[0]!.protocol_config).toMatchObject({ network: 'tcp' })
    expect(JSON.stringify(one.nodes[0]!.protocol_config)).not.toContain('private_key')
    expect(nodeDetailResponse.parse(await (await call('GET', '/v1/nodes?id=00000000-0000-4000-8000-000000000000')).json())).toEqual({ nodes: [], total: 0 })
  })

  it('dashboard node numbers come from the same node store (ids open real nodes)', async () => {
    const all = await list()
    const active = all.filter((n) => n.serving_status === 'active')
    const online = active.filter((n) => !n.stale)
    // dash/api.ts 带 React 运行时，node 侧的 tsconfig 编不了，这里按形状直接读
    const ov = (await (await call('GET', '/v1/overview')).json()) as { nodes: { total: number; online: number } }
    expect(ov.nodes).toEqual({ total: active.length, online: online.length })
    const sys = (await (await call('GET', '/v1/system/status')).json()) as { components: Array<{ key: string; state: string; metrics: Record<string, number>; message?: string }> }
    const fabric = sys.components.find((c) => c.key === 'node_fabric')!
    expect(fabric).toMatchObject({ state: online.length < active.length ? 'warn' : 'ok', metrics: { total: active.length, online: online.length, config_lagging: 0 } })
    // 照 Go：node_fabric 不写 message
    expect(fabric.message ?? null).toBeNull()
    const tasks = (await (await call('GET', '/v1/dashboard/tasks')).json()) as { items: Array<{ kind: string; count: number; sample?: Array<{ id: string; name: string }>; failing?: number; expiring?: number }> }
    const offline = tasks.items.find((t) => t.kind === 'nodes_offline')!
    expect(offline.count).toBe(active.length - online.length)
    expect(offline.sample!.map((x) => x.name)).toEqual(['新加坡 02'])
    const certs = tasks.items.find((t) => t.kind === 'node_certs')!
    expect(certs).toMatchObject({ count: 2, failing: 1, expiring: 2 })
    expect(certs.sample!.map((x) => x.name)).toEqual(['法兰克福 01 · ECH', '香港 02 · IPLC'])
    const ids = new Set(all.map((n) => n.id))
    expect([...offline.sample!, ...certs.sample!].every((x) => ids.has(x.id))).toBe(true)
    const traffic = (await (await call('GET', '/v1/dashboard/traffic/nodes?range=24h&limit=10')).json()) as { items: Array<{ node_id: string }> }
    expect(traffic.items.length).toBeGreaterThan(0)
    expect(traffic.items.every((x) => ids.has(x.node_id))).toBe(true)
  })

  it('searches, filters and pages on the server like GET /v1/nodes', async () => {
    const page = async (query: string) => nodesResponse.parse(await (await call('GET', `/v1/nodes?${query}`)).json())
    const all = await page('state=all')
    expect(all.nodes.every((n) => n.serving_status !== 'retired')).toBe(true)
    // state 给出时由它决定含不含已退役
    expect((await page('state=retired')).nodes.every((n) => n.serving_status === 'retired')).toBe(true)
    expect((await page('state=retired')).total).toBeGreaterThan(0)
    expect((await page('state=offline')).nodes.every((n) => n.serving_status === 'active' && n.stale)).toBe(true)
    expect((await page('state=online')).nodes.every((n) => n.serving_status === 'active' && !n.stale)).toBe(true)
    // 节点心跳取所在服务器：新加坡那台 15 分钟没心跳，新加坡 02 离线但仍在下发（「心跳超时仍下发」）
    const sg2 = (await page('state=offline')).nodes.find((n) => n.name === '新加坡 02')!
    expect(sg2).toMatchObject({ stale: true, delivered_to_users: true, delivery_note: expect.stringContaining('心跳已超时，但仍在下发') })
    // 证书要处理（仪表盘链到 ?f=cert）：失败或快到期的在役节点
    expect((await page('state=cert')).nodes.map((n) => n.name).sort()).toEqual(['法兰克福 01 · ECH', '香港 02 · IPLC'].sort())
    expect((await page('state=disabled')).nodes.every((n) => n.serving_status === 'draft' || n.serving_status === 'disabled')).toBe(true)
    // 搜索：名称、国家（不分大小写）、编号
    const hk = await page('q=hk')
    expect(hk.total).toBeGreaterThan(0)
    expect(hk.nodes.every((n) => `${n.name} ${n.country_code} ${n.server_host} ${n.server_name}`.toLowerCase().includes('hk'))).toBe(true)
    const byNo = await page(`q=${all.nodes[1]!.node_no}`)
    expect(byNo.nodes.map((n) => n.id)).toContain(all.nodes[1]!.id)
    // 分页：total 与页无关
    const second = await page('state=all&limit=2&offset=2')
    expect(second.total).toBe(all.total)
    expect(second.nodes.map((n) => n.id)).toEqual(all.nodes.slice(2, 4).map((n) => n.id))
    // 与 Go 一样在碰数据前回 422
    for (const [query, field] of [['state=gone', 'state'], ['id=nope', 'id'], [`q=${'长'.repeat(121)}`, 'q']] as const) {
      const res = await call('GET', `/v1/nodes?${query}`)
      expect(res.status).toBe(422)
      expect(await res.json()).toMatchObject({ error: { code: 'validation_failed', fields: { [field]: expect.any(String) } } })
    }
  })

  it('refuses illegal transitions and server rebinds', async () => {
    const [src] = await list()
    const draft = await call('POST', '/v1/nodes/status:batch', { items: [{ id: src!.id, row_version: src!.row_version }], serving_status: 'draft' }, 'batch-1')
    expect(draft.status).toBe(409)
    expect(await draft.json()).toMatchObject({ error: { fields: { serving_status: 'active -> draft' } } })
    // 停用节点能改绑 / 解绑：列表 movable=true
    const disabled = (await list()).find((n) => n.serving_status === 'disabled')!
    expect(disabled.movable).toBe(true)
    const disabledServer = disabled.server_id
    const target = serversResponse.parse(await (await call('GET', '/v1/servers')).json()).servers.find((s) => s.id !== disabledServer && s.status === 'ready')!
    // 在役节点不能改绑 / 解绑：movable=false，强行绑定 409
    expect(src!.movable).toBe(false)
    const boundActive = await call('POST', `/v1/nodes/${src!.id}/bind`, { row_version: src!.row_version, server_id: target.id }, 'bind-1')
    expect(boundActive.status).toBe(409)
    expect(await boundActive.json()).toMatchObject({ error: { code: 'conflict', message: '节点在役，先停用再改绑 / 解绑' } })
    // 停用节点能解绑；解绑后列表里说未绑定服务器，不下发
    const unbound = await call('POST', `/v1/nodes/${disabled.id}/unbind`, { row_version: disabled.row_version, reason: '换机房' }, 'unbind-1')
    expect(unbound.status).toBe(200)
    const afterUnbind = (await unbound.json()) as { server_id: string | null; row_version: number }
    expect(afterUnbind.server_id).toBeNull()
    const unboundRow = (await list()).find((n) => n.id === disabled.id)!
    expect(unboundRow).toMatchObject({ server_id: null, delivered_to_users: false, delivery_note: '未绑定服务器，不下发：先在节点详情「身份与操作」里绑定服务器，再上线' })
    // 再绑到另一台就绪服务器上的空闲端口
    const rebound = await call('POST', `/v1/nodes/${disabled.id}/bind`, { row_version: afterUnbind.row_version, server_id: target.id, server_port: 24443 }, 'bind-2')
    expect(rebound.status).toBe(200)
    expect(((await rebound.json()) as { server_id: string | null }).server_id).toBe(target.id)
    // 绑到同端口已被别的节点占着的服务器（在役节点 src 那台、那个端口）→ 409 带 server_port
    const moved = (await list()).find((n) => n.id === disabled.id)!
    const busy = await call('POST', `/v1/nodes/${moved.id}/bind`, { row_version: moved.row_version, server_id: src!.server_id, server_port: src!.server_port }, 'bind-3')
    expect(busy.status).toBe(409)
    expect(await busy.json()).toMatchObject({ error: { code: 'conflict', fields: { server_port: expect.any(String) } } })
    // 老迁移接口已删：路由不存在，假后端按未匹配路由回 404
    expect((await call('POST', `/v1/nodes/${moved.id}/move`, { row_version: moved.row_version, server_id: target.id })).status).toBe(404)
  })

  it('creates an unbound node and refuses to activate it', async () => {
    const made = await call('POST', '/v1/nodes', { name: '未绑服务器 01', node_type: 'shadowsocks', protocol_config: { cipher: 'aes-256-gcm' } }, 'unbound-node-1')
    expect(made.status).toBe(201)
    const created = (await made.json()) as { id: string; row_version: number; server_id: string | null }
    expect(created.server_id).toBeNull()
    // 没绑服务器就不下发，列表里说清楚
    const row = (await list()).find((n) => n.id === created.id)!
    expect(row).toMatchObject({ server_id: null, delivered_to_users: false, delivery_note: '未绑定服务器，不下发：先在节点详情「身份与操作」里绑定服务器，再上线' })
    const activated = await call('POST', `/v1/nodes/${created.id}/activate`, { row_version: created.row_version }, 'unbound-activate-1')
    expect(activated.status).toBe(409)
    expect(await activated.json()).toMatchObject({
      error: {
        code: 'conflict',
        message: '节点还没有绑定服务器，先在节点详情「身份与操作」里绑定服务器再上线',
        fields: { server_id: '节点还没有绑定服务器，先在节点详情「身份与操作」里绑定服务器再上线' },
      },
    })
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

  it('refuses to shrink capacity below the nodes in use and detaches nodes on delete', async () => {
    const kr = (await servers()).find((x) => x.name === 'kr-sel-edge-1')!
    const made = await call('POST', '/v1/nodes', { name: '首尔 02', server_id: kr.id, node_type: 'shadowsocks', server_host: 'kr2.example.test', server_port: 8388, protocol_config: { cipher: 'aes-256-gcm' } }, 'kr-node-1')
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
    // 草稿节点随删服务器只解绑、留作配置（在役的才退役）
    expect(orphan).toMatchObject({ serving_status: 'draft', server_id: null })
    const hk = (await servers()).find((x) => x.name === 'hk-hkg-edge-1')!
    const tight = await call('PATCH', `/v1/servers/${hk.id}`, { row_version: hk.row_version, capacity_nodes: 1 })
    expect(await tight.json()).toMatchObject({ error: { code: 'conflict', fields: { capacity_nodes: expect.stringMatching(/^minimum=[2-9]$/) } } })
  })

  it('issues server install tokens once per idempotency key', async () => {
    const hk = (await servers()).find((x) => x.name === 'hk-hkg-edge-1')!
    const first = await call('POST', `/v1/servers/${hk.id}/bootstrap-token`, { ttl_minutes: 30 }, 'srv-tok-1')
    expect(first.status).toBe(201)
    const firstJson = (await first.json()) as { token: string }
    const again = await call('POST', `/v1/servers/${hk.id}/bootstrap-token`, { ttl_minutes: 30 }, 'srv-tok-1')
    expect(again.status).toBe(409)
    const againJson = (await again.json()) as { error: { code: string } }
    expect(againJson.error.code).toBe('conflict')
    expect(JSON.stringify(againJson)).not.toContain(firstJson.token)
    expect((await call('POST', `/v1/servers/${hk.id}/bootstrap-token`, { ttl_minutes: 20 }, 'srv-tok-1')).status).toBe(409)
    const otherKey = await call('POST', `/v1/servers/${hk.id}/bootstrap-token`, { ttl_minutes: 30 }, 'srv-tok-2')
    expect(otherKey.status).toBe(201)
    const otherJson = (await otherKey.json()) as { token: string }
    expect(otherJson.token).not.toBe(firstJson.token)
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
    expect(await dropped.json()).toMatchObject({ error: { code: 'conflict', message: '要删除的全局出站仍被规则引用：节点 香港 01 · 原生 → US-LAX-01' } })
    const bad = await put({ expected_revision: g.revision, outbounds: [], routes: [{ matcher: { geosite: ['cn'] }, outbound_tag: 'direct', enabled: true }] }, 'g-3')
    expect(await bad.json()).toMatchObject({ error: { fields: { routes: '第 1 条规则无法跨内核下发：不支持的匹配类型' } } })
    const ok = await put({ expected_revision: g.revision, outbounds: g.outbounds, routes: [{ priority: 10, matcher: { port: [25] }, outbound_tag: 'block', enabled: true, note: '' }] }, 'g-4')
    expect(ok.status).toBe(200)
    const saved = (await ok.json()) as { revision: string; affected_nodes: number }
    expect(saved.revision).not.toBe(g.revision)
    expect(saved.affected_nodes).toBeGreaterThan(0)
    expect((await call('GET', '/v1/nodes/routing').then((r) => r.json())) as { routes: unknown[] }).toMatchObject({ revision: saved.revision, routes: [{ outbound_tag: 'block' }] })
  })

  it('reports per-server upgrade eligibility in the server DTO', async () => {
    const raw = (await (await call('GET', '/v1/servers')).json()) as { servers: Array<Record<string, unknown>> }
    const rows = serversResponse.parse(raw).servers
    const byName = (name: string) => rows.find((s) => s.name === name)!
    expect(byName('hk-hkg-edge-1').upgrade.reason).toBe('up_to_date')
    expect(byName('jp-tyo-edge-2').upgrade.reason).toBe('no_upgrade_support')
    // 新加坡那台是「心跳超时仍下发」的演示机：15 分钟没心跳，连不上就不能在线升级
    expect(byName('sg-sin-edge-1').upgrade.reason).toBe('not_connected')
    expect(byName('tw-tpe-edge-1').upgrade.reason).toBe('not_connected')
    // 首尔那台草稿机已被前面的用例删掉，用大阪这台草稿
    expect(byName('jp-osa-edge-1').upgrade.reason).toBe('not_ready')
    expect(byName('us-lax-edge-1').last_upgrade).toMatchObject({ status: 'rolled_back', reason_code: 'confirm_timeout' })
    expect(byName('us-lax-edge-1').upgrade.available).toBe(true)
    for (const s of raw.servers) {
      expect(s).not.toHaveProperty('upgradeV1')
      expect(s).not.toHaveProperty('buildSerial')
    }
  })

  it('starts upgrades per server, replays by key and validates the body', async () => {
    const lax = (await servers()).find((s) => s.name === 'us-lax-edge-1')!
    const hk = (await servers()).find((s) => s.name === 'hk-hkg-edge-1')!
    const missing = '00000000-0000-4000-8000-000000000000'
    const first = await call('POST', '/v1/servers/upgrade', { server_ids: [lax.id, hk.id, missing, lax.id] }, 'upg-1')
    expect(first.status).toBe(200)
    const firstBody = await first.json()
    expect(firstBody).toMatchObject({
      target_version: 'v2026.10.21',
      results: [
        { server_id: lax.id, ok: true, reason: null },
        { server_id: hk.id, ok: false, reason: 'up_to_date' },
        { server_id: missing, ok: false, reason: 'not_found' },
      ],
    })
    const listed = (await servers()).find((s) => s.id === lax.id)!
    expect(listed.last_upgrade?.status).toBe('sent')
    expect(listed.upgrade.reason).toBe('in_progress')
    const replay = await call('POST', '/v1/servers/upgrade', { server_ids: [lax.id, hk.id, missing, lax.id] }, 'upg-1')
    expect(await replay.json()).toEqual(firstBody)
    const again = await call('POST', '/v1/servers/upgrade', { server_ids: [lax.id] }, 'upg-2')
    expect(await again.json()).toMatchObject({ results: [{ server_id: lax.id, ok: false, reason: 'in_progress' }] })
    const empty = await call('POST', '/v1/servers/upgrade', { server_ids: [] }, 'upg-empty')
    expect(empty.status).toBe(422)
    expect(await empty.json()).toMatchObject({ error: { fields: { server_ids: '一次选 1 到 50 台服务器' } } })
    const badId = await call('POST', '/v1/servers/upgrade', { server_ids: ['nope'] }, 'upg-bad')
    expect(badId.status).toBe(422)
    expect(await badId.json()).toMatchObject({ error: { fields: { server_ids: '服务器 ID 格式不正确' } } })
    const extra = await call('POST', '/v1/servers/upgrade', { server_ids: [lax.id], force: true }, 'upg-extra')
    expect(extra.status).toBe(400)
  })

  it('issues install tokens in the Go shape: ttl 1–60 (omitted = 20), one-line command with panel address, CA pin and token', async () => {
    const osa = (await servers()).find((x) => x.name === 'jp-osa-edge-1')!
    const res = await call('POST', `/v1/servers/${osa.id}/bootstrap-token`, {}, 'srv-tok-shape')
    expect(res.status).toBe(201)
    const body = (await res.json()) as { token: string; expires_at: string; install_command: string }
    // 照 Go：32 字节 base64url 令牌；缺省 20 分钟
    expect(body.token).toMatch(/^[A-Za-z0-9_-]{43}$/)
    const minutes = (Date.parse(body.expires_at) - Date.now()) / 60_000
    expect(minutes).toBeGreaterThan(19)
    expect(minutes).toBeLessThanOrEqual(20)
    // 照 Go buildInstallCommand：脚本地址、gRPC 地址（缺省 9443）、CA 指纹、令牌
    expect(body.install_command).toMatch(/^curl --proto '=https' -fsSL 'https:\/\/panel\.example\.test\/pdnd\/install\.sh' \| sudo bash -s -- --panel 'panel\.example\.test:9443' --ca-sha256 'sha256:[0-9a-f]{64}' --token '[A-Za-z0-9_-]{43}'$/)
    expect(body.install_command.endsWith(`--token '${body.token}'`)).toBe(true)
    const long = await call('POST', `/v1/servers/${osa.id}/bootstrap-token`, { ttl_minutes: 61 }, 'srv-tok-61')
    expect(long.status).toBe(422)
    expect(await long.json()).toMatchObject({ error: { fields: { ttl_minutes: '有效期须在 1–60 分钟' } } })
    expect((await call('POST', `/v1/servers/${osa.id}/bootstrap-token`, { ttl_minutes: 30, node_name: 'x' }, 'srv-tok-extra')).status).toBe(400)
    // 已退役的服务器不能签发
    const gone = serverSchema.parse(await (await call('POST', '/v1/servers', { name: 'mock-edge-retire' })).json())
    expect(gone.enrolled).toBe(false)
    expect((await call('POST', `/v1/servers/${gone.id}/status`, { status: 'retired', row_version: gone.row_version })).status).toBe(200)
    const refused = await call('POST', `/v1/servers/${gone.id}/bootstrap-token`, { ttl_minutes: 30 }, 'srv-tok-retired')
    expect(refused.status).toBe(409)
    expect(await refused.json()).toMatchObject({ error: { code: 'conflict', message: '服务器已删除或已退役，不能签发安装令牌' } })
  })

  it('keeps identity per server: revoking through one node revokes the whole server, retiring a server revokes it too', async () => {
    const all = nodesResponse.parse(await (await call('GET', '/v1/nodes?include_retired=1')).json()).nodes
    // 法兰克福那台：从节点吊销，作用在服务器身份上
    const fra = all.find((n) => n.name === '法兰克福 01 · ECH')!
    expect(fra.identity_serial).toBe(3)
    expect((await call('POST', `/v1/nodes/${fra.id}/revoke-identity`)).status).toBe(200)
    const view = (await (await call('GET', `/v1/nodes/${fra.id}/identity`)).json()) as { identity: { status: string; revoked_reason: string; spiffe_id: string } }
    expect(view.identity).toMatchObject({ status: 'revoked', revoked_reason: '后台吊销', spiffe_id: `spiffe://pandora/server/${fra.server_id}` })
    expect((await servers()).find((s) => s.id === fra.server_id)!.enrolled).toBe(false)
    expect((await call('POST', `/v1/nodes/${fra.id}/revoke-identity`)).status).toBe(409)
    // 台北那台：ready → draining → retired，退役时吊销服务器身份
    let tpe = (await servers()).find((s) => s.name === 'tw-tpe-edge-1')!
    expect(tpe.enrolled).toBe(true)
    tpe = serverSchema.parse(await (await call('POST', `/v1/servers/${tpe.id}/status`, { status: 'draining', row_version: tpe.row_version })).json())
    tpe = serverSchema.parse(await (await call('POST', `/v1/servers/${tpe.id}/status`, { status: 'retired', row_version: tpe.row_version })).json())
    expect(tpe.enrolled).toBe(false)
    const tpeNode = all.find((n) => n.server_id === tpe.id)!
    const tpeView = (await (await call('GET', `/v1/nodes/${tpeNode.id}/identity`)).json()) as { identity: { status: string; revoked_reason: string } }
    expect(tpeView.identity).toMatchObject({ status: 'revoked', revoked_reason: '服务器已退役' })
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
    const base = { network: 'mkcp', tls: 2, reality_settings: { dest: 'www.apple.com:443', server_name: 'www.apple.com', public_key: 'Qw2Er4Ty6Ui8Op0As1Df3Gh5Jk7Lz9Xc2Vb4Nm6Qp8R', short_id: '3f9a7c21' }, mask: 'srtp' }
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
    // 显式 null：以请求为准清空（选填的 mask_password）
    const cleared = await patch({ ...base, mask_password: null })
    expect(cleared.mask_password).toBeNull()
    // 必填的私钥显式清空：照 Go 校验回 422，错误落在表单路径上
    const noKey = await call('PATCH', `/v1/nodes/${n.id}`, { row_version: row, protocol_config: { ...base, reality_settings: { ...base.reality_settings, private_key: null } } })
    expect(noKey.status).toBe(422)
    expect(await noKey.json()).toMatchObject({ error: { fields: { 'protocol_config.reality_settings.private_key': '私钥必填' } } })
  })

  it('R108/R113: activate takes a draft node to active, readies its server, replays, and refuses others with 409', async () => {
    const n = (await list()).find((x) => x.name === '大阪 01（待上线）')!
    expect(n.serving_status).toBe('draft')
    const res = await call('POST', `/v1/nodes/${n.id}/activate`, { row_version: n.row_version }, 'act-1')
    expect(res.status).toBe(200)
    const body = adminNodeSchema.parse(await res.json())
    expect(body).toMatchObject({ serving_status: 'active', row_version: n.row_version + 1 })
    // R113：池绑着套餐，没有提示时不出现 warnings 这个键
    expect(body).not.toHaveProperty('warnings')
    // 同键重放拿到同一结果；已是 active 再上线是 200 不改动，且先于版本号判断（带过期版本号也回 200）
    expect(adminNodeSchema.parse(await (await call('POST', `/v1/nodes/${n.id}/activate`, { row_version: n.row_version }, 'act-1')).json())).toEqual(body)
    expect((await call('POST', `/v1/nodes/${n.id}/activate`, { row_version: n.row_version }, 'act-2')).status).toBe(200)
    // 无池的在役节点：200 并带「未划入节点池」提示
    const noPool = (await list()).find((x) => x.pool_id === null && x.serving_status === 'active')!
    const warned = adminNodeSchema.parse(await (await call('POST', `/v1/nodes/${noPool.id}/activate`, { row_version: noPool.row_version }, 'act-5')).json())
    expect(warned.warnings).toEqual(['未划入节点池，不服务任何用户'])
    // 首次搭建：池还没绑套餐，上线成功并带提示
    const fresh = (await list()).find((x) => x.name === '大阪 02（新池待上线）')!
    const unbound = adminNodeSchema.parse(await (await call('POST', `/v1/nodes/${fresh.id}/activate`, { row_version: fresh.row_version }, 'act-8')).json())
    expect(unbound).toMatchObject({ serving_status: 'active', warnings: ['所在节点池没有绑定任何套餐，暂时不服务任何用户'] })
    // 版本号非法先回 422（httpx.Invalid）
    expect((await call('POST', `/v1/nodes/${n.id}/activate`, { row_version: 0 }, 'act-6')).status).toBe(422)
    const srv = serversResponse.parse(await (await call('GET', '/v1/servers')).json()).servers.find((s) => s.id === n.server_id)!
    expect(srv.status).toBe('ready')
    // 版本冲突、终态
    const retired = (await list()).find((x) => x.serving_status === 'retired')!
    const refused = await call('POST', `/v1/nodes/${retired.id}/activate`, { row_version: retired.row_version }, 'act-4')
    expect(refused.status).toBe(409)
    expect(((await refused.json()) as { error: { message: string } }).error.message).toContain('已退役')
    // 新建的服务器还没接入：上面的草稿节点 identity_serial 为空
    const fresh9 = serverSchema.parse(await (await call('POST', '/v1/servers', { name: 'mock-edge-unenrolled' })).json())
    expect(fresh9.enrolled).toBe(false)
    const made = await call('POST', '/v1/nodes', { name: '未接入 01', server_id: fresh9.id, node_type: 'shadowsocks', server_host: 'u1.example.test', server_port: 8388, protocol_config: { cipher: 'aes-256-gcm' } }, 'act-node-unenrolled')
    expect(made.status).toBe(201)
    const pending = (await list()).find((x) => x.name === '未接入 01')!
    expect(pending).toMatchObject({ serving_status: 'draft', identity_serial: null })
    // 过期版本号对未上线节点回 409
    expect((await call('POST', `/v1/nodes/${pending.id}/activate`, { row_version: pending.row_version + 9 }, 'act-3')).status).toBe(409)
    // 没接入的草稿：同版本号回 409 并说服务器还没接入（照 Go serverNotEnrolledMsg，带 fields.server_id）
    const early = await call('POST', `/v1/nodes/${pending.id}/activate`, { row_version: pending.row_version }, 'act-7')
    expect(early.status).toBe(409)
    expect(await early.json()).toMatchObject({ error: { message: '服务器还没接入：先到「服务器」页签发安装令牌，在机器上用 root 执行安装命令', fields: { server_id: '服务器还没接入：先到「服务器」页签发安装令牌，在机器上用 root 执行安装命令' } } })
  })

  it('seeds an unbound draft: no identity, no tokens, cannot go live', async () => {
    const n = (await list()).find((x) => x.name === '伦敦 01')!
    expect(n).toMatchObject({ server_id: null, serving_status: 'draft', delivered_to_users: false, delivery_note: '未绑定服务器，不下发：先在节点详情「身份与操作」里绑定服务器，再上线' })
    expect(await (await call('GET', `/v1/nodes/${n.id}/identity`)).json()).toEqual({ identity: null, bootstrap_tokens_pending: 0 })
    expect((await call('POST', `/v1/nodes/${n.id}/revoke-identity`)).status).toBe(409)
    const refused = await call('POST', `/v1/nodes/${n.id}/activate`, { row_version: n.row_version }, 'act-unbound')
    expect(refused.status).toBe(409)
    expect(await refused.json()).toMatchObject({ error: { fields: { server_id: expect.any(String) } } })
  })

  it('W2: delivery notes follow the subscription rule, and a serving node cannot drop its pool', async () => {
    // R108 的用例已把大阪 02 推上线：在役、有心跳、服务器已就绪，但池没绑任何套餐
    const unbound = (await list()).find((x) => x.name === '大阪 02（新池待上线）')!
    // 照 Go nodeDeliveryVerdict 的 poolUnbound 说明（与上线时的 warnings 是两句话）
    expect(unbound).toMatchObject({ serving_status: 'active', delivered_to_users: false, delivery_note: '所在节点池没有绑定任何套餐，不会出现在任何用户的订阅里' })
    const serving = (await list()).find((x) => x.name === '香港 01 · 原生')!
    const refused = await call('PATCH', `/v1/nodes/${serving.id}`, { row_version: serving.row_version, pool_id: null })
    expect(refused.status).toBe(422)
    expect(await refused.json()).toMatchObject({ error: { fields: { pool_id: expect.stringContaining('在役节点必须属于一个节点池') } } })
    // 草稿可以无池
    const draft = (await list()).find((x) => x.name === '新加坡 03（草稿）')!
    const cleared = await call('PATCH', `/v1/nodes/${draft.id}`, { row_version: draft.row_version, pool_id: null })
    expect(cleared.status).toBe(200)
    const after = adminNodeSchema.parse(await cleared.json())
    expect(after.pool_id).toBeNull()
    expect((await call('PATCH', `/v1/nodes/${draft.id}`, { row_version: after.row_version, pool_id: draft.pool_id })).status).toBe(200)
  })
})
