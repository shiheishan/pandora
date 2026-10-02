/**
 * [INPUT]: 依赖 vitest，依赖 ./mock-helpers，依赖 ../dev/mock-api 的 MOCK_ACCOUNTS，依赖 ../src/admin/screens/nodes/schemas 的节点列表 / 节点路由 / 路由组 / 组内路由 / 生效预览 schema
 * [OUTPUT]: 对外提供路由组（00096）假接口的测试
 * [POS]: tests 的路由组假后端守卫：列表与组内路由能被页面 schema 接住、名称大小写不敏感唯一、组规则不能指向节点私有出站、行版本冲突 409、成员从组侧与节点侧两边改且互相推版本、节点私有规则能指向所在组出站、生效预览的顺序与来源（节点 → 组按排序 → 全局，出站具体范围覆盖）、新造成的悬空引用 409、删组后成员退回全局、全局出站被组规则引用时删除 409、只读账号能读不能写（写接口 404）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { effectiveRoutingSchema, groupRoutingSchema, nodeRoutingSchema, nodesResponse, routeGroupsResponse, routeGroupSchema } from '../src/admin/screens/nodes/schemas'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · admin route groups (00096)', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
  })
  afterAll(() => close(server))

  const call = (method: string, path: string, body?: unknown, key?: string) => mockFetch(base, auth, method, path, body, key)
  const groups = async () => routeGroupsResponse.parse(await (await call('GET', '/v1/route-groups')).json()).groups
  const nodes = async () => nodesResponse.parse(await (await call('GET', '/v1/nodes')).json()).nodes
  const nodeRouting = async (id: string) => nodeRoutingSchema.parse(await (await call('GET', `/v1/nodes/${id}/routing`)).json())
  const effective = async (id: string) => effectiveRoutingSchema.parse(await (await call('GET', `/v1/nodes/${id}/routing/effective`)).json())

  it('lists seeded groups in effect order and previews node → group → global', async () => {
    const list = await groups()
    expect(list.map((g) => g.sort_order)).toEqual([...list.map((g) => g.sort_order)].sort((a, b) => a - b))
    const hk = list[0]!
    expect(hk.members.length).toBeGreaterThan(0)
    const node = hk.members[0]!
    expect((await nodeRouting(node.id)).groups.map((g) => g.id)).toContain(hk.id)
    const eff = await effective(node.id)
    const scopes = eff.routes.map((r) => r.source.scope)
    // 节点规则在前、组规则其次、全局在最后
    expect(scopes.indexOf('group')).toBeGreaterThan(scopes.lastIndexOf('node'))
    expect(scopes.lastIndexOf('group')).toBeLessThan(scopes.indexOf('global'))
    expect(eff.outbounds.find((o) => o.tag === 'HK-UNLOCK')?.source).toEqual({ scope: 'group', group_id: hk.id, group_name: hk.name })
  })

  it('creates, edits and publishes a group with the same guards as the backend', async () => {
    expect((await call('POST', '/v1/route-groups', { name: '' }, 'rg-0')).status).toBe(422)
    const created = await call('POST', '/v1/route-groups', { name: '新加坡 · 测试', sort_order: 5 }, 'rg-1')
    expect(created.status).toBe(201)
    const g = routeGroupSchema.parse(await created.json())
    expect((await call('POST', '/v1/route-groups', { name: '新加坡 · 测试'.toUpperCase() }, 'rg-2')).status).toBe(409)
    const routing = groupRoutingSchema.parse(await (await call('GET', `/v1/route-groups/${g.id}/routing`)).json())
    expect(routing).toEqual({ row_version: 1, outbounds: [], routes: [] })
    // 组规则可指向全局出站，不能指向不存在的
    const bad = await call('PUT', `/v1/route-groups/${g.id}/routing`, { row_version: 1, outbounds: [], routes: [{ matcher: { port: [1] }, outbound_tag: 'nowhere', enabled: true }] }, 'rg-3')
    expect(bad.status).toBe(422)
    const ok = await call('PUT', `/v1/route-groups/${g.id}/routing`, { row_version: 1, outbounds: [{ tag: 'SG-OUT', type: 'socks', settings: {} }], routes: [{ matcher: { port: [8443] }, outbound_tag: 'SG-OUT', enabled: true }, { matcher: { port: [9443] }, outbound_tag: 'US-LAX-01', enabled: true }] }, 'rg-4')
    expect(await ok.json()).toEqual({ ok: true, row_version: 2, affected_nodes: 0 })
    expect((await call('PUT', `/v1/route-groups/${g.id}/routing`, { row_version: 1, outbounds: [], routes: [] }, 'rg-5')).status).toBe(409)
    const patched = await call('PATCH', `/v1/route-groups/${g.id}`, { row_version: 2, description: '说明' }, 'rg-6')
    expect(await patched.json()).toMatchObject({ group: { description: '说明', row_version: 3 }, affected_nodes: 0 })
  })

  it('changes members from both sides and refuses new dangling references', async () => {
    const g = (await groups()).find((x) => x.name === '新加坡 · 测试')!
    const target = (await nodes()).find((n) => n.name === '新加坡 02')!
    const joined = await call('PUT', `/v1/route-groups/${g.id}/members`, { row_version: g.row_version, node_ids: [target.id] }, 'rm-1')
    expect(await joined.json()).toMatchObject({ ok: true, row_version: g.row_version + 1, affected_nodes: 1 })
    // 节点私有规则可以指向所在组的出站（大小写不敏感）
    let r = await nodeRouting(target.id)
    expect(r.groups.map((x) => x.id)).toEqual([g.id])
    const own = await call('PUT', `/v1/nodes/${target.id}/routing`, { row_version: r.row_version, outbounds: [], routes: [{ matcher: { port: [22] }, outbound_tag: 'sg-out', enabled: true }] })
    expect(own.status).toBe(200)
    // 退组会让那条规则悬空：组侧与节点侧都 409，原样不动
    const left = await call('PUT', `/v1/route-groups/${g.id}/members`, { row_version: g.row_version + 1, node_ids: [] }, 'rm-2')
    expect(await left.json()).toMatchObject({ error: { code: 'conflict', message: '移出组的节点仍有规则指向组内出站：节点 新加坡 02 → sg-out' } })
    r = await nodeRouting(target.id)
    expect((await call('PUT', `/v1/nodes/${target.id}/route-groups`, { row_version: r.row_version, group_ids: [] })).status).toBe(409)
    expect((await call('DELETE', `/v1/route-groups/${g.id}`, { row_version: g.row_version + 1 }, 'rm-3')).status).toBe(409)
    // 规则改走后退组：节点侧写推进组的行版本
    const cleared = await call('PUT', `/v1/nodes/${target.id}/routing`, { row_version: r.row_version, outbounds: [], routes: [] })
    expect(cleared.status).toBe(200)
    r = await nodeRouting(target.id)
    expect((await call('PUT', `/v1/nodes/${target.id}/route-groups`, { row_version: r.row_version, group_ids: [] })).status).toBe(200)
    const after = (await groups()).find((x) => x.id === g.id)!
    expect(after.row_version).toBe(g.row_version + 2)
    expect(after.members).toEqual([])
    // 删组：成员退回只用全局与私有路由
    expect((await call('DELETE', `/v1/route-groups/${g.id}`, { row_version: after.row_version }, 'rm-4')).status).toBe(200)
    expect((await groups()).some((x) => x.id === g.id)).toBe(false)
    expect((await effective(target.id)).routes.every((x) => x.source.scope !== 'group')).toBe(true)
  })

  it('refuses dropping a global outbound still used by a group rule', async () => {
    const g = await (await call('GET', '/v1/nodes/routing')).json() as { revision: string; outbounds: Array<{ tag: string }>; routes: Array<{ outbound_tag: string }> }
    const hk = (await groups())[0]!
    await call('PUT', `/v1/route-groups/${hk.id}/routing`, { row_version: hk.row_version, outbounds: [], routes: [{ matcher: { port: [1] }, outbound_tag: 'HK-RELAY', enabled: true }] }, 'rx-1')
    const dropped = await call('PUT', '/v1/nodes/routing', { expected_revision: g.revision, outbounds: g.outbounds.filter((o) => o.tag !== 'HK-RELAY'), routes: g.routes.filter((x) => x.outbound_tag !== 'HK-RELAY') }, 'rx-2')
    expect(await dropped.json()).toMatchObject({ error: { code: 'conflict', message: `要删除的全局出站仍被规则引用：路由组 ${hk.name} → hk-relay` } })
  })

  it('lets a read-only account (node.read) read but hides every write behind node.config.publish', async () => {
    const viewer = bearer((await loginAs(base, MOCK_ACCOUNTS.viewer)).access_token)
    const g = (await groups())[0]!
    expect((await mockFetch(base, viewer, 'GET', '/v1/route-groups')).status).toBe(200)
    expect((await mockFetch(base, viewer, 'GET', `/v1/route-groups/${g.id}/routing`)).status).toBe(200)
    expect((await mockFetch(base, viewer, 'POST', '/v1/route-groups', { name: 'x' }, 'v-1')).status).toBe(404)
    expect((await mockFetch(base, viewer, 'PUT', `/v1/route-groups/${g.id}/members`, { row_version: g.row_version, node_ids: [] }, 'v-2')).status).toBe(404)
    expect((await mockFetch(base, viewer, 'PUT', `/v1/nodes/${g.members[0]!.id}/route-groups`, { row_version: 1, group_ids: [] })).status).toBe(404)
  })
})
