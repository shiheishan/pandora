/**
 * [INPUT]: 依赖 node:crypto 的 randomUUID，依赖 ../types 的 Json / MockContext / MockResult / MockRoute，依赖 ./nodes-infra 的 globalRouting、validateRouting、emptyBody、setDanglingSource 与 RoutedNode
 * [OUTPUT]: 对外提供路由组假数据 routeGroups、groupsOfNode / visibleTagsForNode / copyMemberships（nodes.ts 的单节点路由与复制用）、悬空引用 danglingRefs，以及 routeGroupRoutes(节点存储) 返回的路由表
 * [POS]: dev/mock/admin 的「节点与服务器 · 路由组」（00096）假接口，由 nodes.ts 并入同一个 MockModule：组列表 / 新建 / 改元信息 / 删除、组内路由读写、组侧与节点侧改成员、节点生效预览。合并口径照 nodefabric/routing_merge.go（规则 节点 → 组按 sort_order → 全局；出站 全局 → 组 → 节点同 tag 保位覆盖），悬空引用照 routing_refs.go 只拒新造成的。权限 / reauth / 幂等 scope / 文案照 router_nodes.go 与 nodefabric
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomUUID } from 'node:crypto'
import type { Json, MockContext, MockResult, MockRoute } from '../types.ts'
import { emptyBody, globalRouting, setDanglingSource, validateRouting, type RoutedNode } from './nodes-infra.ts'

const err = (status: number, code: string, message: string, fields?: Record<string, string>): MockResult => ({ status, body: { error: { code, message, ...(fields ? { fields } : {}) } } })
const invalid = (fields: Record<string, string>) => err(422, 'validation_failed', '请求参数校验未通过', fields)
const notFound = () => err(404, 'not_found', '资源不存在或无权访问')
const reply = (ctx: MockContext, r: MockResult) => ctx.send(r.status, r.body)
const text = (v: unknown) => (typeof v === 'string' ? v : '')
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const unknownField = (body: Json, allowed: readonly string[]) => Object.keys(body).find((k) => !allowed.includes(k)) ?? null
const lower = (v: unknown) => text(v).trim().toLowerCase()
const BUILTIN = ['direct', 'block']

type Outbound = { tag: string; type: string; settings: unknown }
export interface RouteGroup {
  id: string
  name: string
  description: string
  sort_order: number
  row_version: number
  created_at: string
  updated_at: string
  outbounds: Outbound[]
  routes: Json[]
  members: string[]
}

const now = () => new Date().toISOString()
function group(name: string, description: string, sort_order: number, outbounds: Outbound[], routes: Json[]): RouteGroup {
  const at = new Date(Date.now() - 86400_000 * 7).toISOString()
  return { id: randomUUID(), name, description, sort_order, row_version: 1, created_at: at, updated_at: at, outbounds, routes, members: [] }
}

export const routeGroups: RouteGroup[] = [
  group('香港 · 流媒体解锁', '香港节点的 Netflix / Disney+ 走解锁出口', 10, [{ tag: 'HK-UNLOCK', type: 'trojan', settings: { server: 'unlock-hk.example.invalid', port: 443 } }], [
    { priority: 10, matcher: { domain_suffix: ['netflix.com', 'nflxvideo.net', 'disneyplus.com'] }, outbound_tag: 'HK-UNLOCK', enabled: true, note: '流媒体解锁' },
  ]),
  group('日本 · 中转', '日本节点回程走中转', 20, [{ tag: 'JP-RELAY', type: 'shadowsocks', settings: { server: 'relay-jp.example.invalid', port: 8388 } }], [
    { priority: 10, matcher: { domain_suffix: ['jp', 'nicovideo.jp'] }, outbound_tag: 'JP-RELAY', enabled: true, note: '日本站点' },
  ]),
]

/** 组间生效顺序：sort_order 小的先，同值按创建先后（Go 端为 uuidv7 的 id） */
const ordered = (groups: readonly RouteGroup[]) => [...groups].sort((a, b) => a.sort_order - b.sort_order || a.created_at.localeCompare(b.created_at))
export const groupsOfNode = (nodeId: string) => ordered(routeGroups.filter((g) => g.members.includes(nodeId)))
const groupRef = (g: RouteGroup) => ({ id: g.id, name: g.name, sort_order: g.sort_order })
export const groupRefsOfNode = (nodeId: string) => groupsOfNode(nodeId).map(groupRef)

/** 节点除私有出站外看得见的 tag：全局与所在各组（routing_refs.go 的 visibleOutboundTagsTx） */
export const visibleTagsForNode = (nodeId: string): string[] => [...globalRouting.outbounds.map((o) => o.tag), ...groupsOfNode(nodeId).flatMap((g) => g.outbounds.map((o) => o.tag))]

/** 带路由复制节点时，副本跟着进源节点所在的组 */
export const copyMemberships = (from: string, to: string) => routeGroups.forEach((g) => g.members.includes(from) && g.members.push(to))

/** 全租户的悬空引用（含停用规则），标注与 Go 的 danglingRef.String 一致 */
export function danglingRefs(nodes: readonly RoutedNode[]): Set<string> {
  const out = new Set<string>()
  const global = new Set(globalRouting.outbounds.map((o) => o.tag.toLowerCase()))
  for (const g of routeGroups) {
    const own = new Set(g.outbounds.map((o) => o.tag.toLowerCase()))
    for (const r of g.routes) {
      const tag = lower(r.outbound_tag)
      if (!BUILTIN.includes(tag) && !global.has(tag) && !own.has(tag)) out.add(`路由组 ${g.name} → ${tag}`)
    }
  }
  for (const n of nodes) {
    if (n.status === 'destroyed') continue
    const seen = new Set([...visibleTagsForNode(n.id), ...n.routing.outbounds.map((o) => o.tag)].map((t) => t.toLowerCase()))
    for (const r of n.routing.routes) {
      const tag = lower(r.outbound_tag)
      if (!BUILTIN.includes(tag) && !seen.has(tag)) out.add(`节点 ${n.display_name ?? n.name} → ${tag}`)
    }
  }
  return out
}

/** 写之后再算一次，出现写之前没有的就回 409（routing_refs.go 的 refuseNewDanglingTx） */
export function newDangling(before: ReadonlySet<string>, nodes: readonly RoutedNode[], message: string): MockResult | null {
  const fresh = [...danglingRefs(nodes)].filter((d) => !before.has(d)).sort()
  return fresh.length ? err(409, 'conflict', `${message}：${fresh.join('、')}`) : null
}

function out(g: RouteGroup, nodes: readonly RoutedNode[]) {
  return {
    id: g.id,
    name: g.name,
    description: g.description,
    sort_order: g.sort_order,
    row_version: g.row_version,
    outbound_count: g.outbounds.length,
    rule_count: g.routes.length,
    members: nodes.filter((n) => g.members.includes(n.id)).map((n) => ({ id: n.id, name: n.name })),
    created_at: g.created_at,
    updated_at: g.updated_at,
  }
}

/** route_groups.go 的 normalizeRouteGroupFields：名称 1–64、说明 ≤ 500、组序 ±1000000 */
function fieldErrors(body: Json, creating: boolean): Record<string, string> | null {
  if (creating || body.name !== undefined) {
    const n = [...text(body.name).trim()].length
    if (n < 1 || n > 64) return { name: '名称为 1 到 64 个字符' }
  }
  if (body.description !== undefined && [...text(body.description).trim()].length > 500) return { description: '说明最多 500 个字符' }
  if (body.sort_order !== undefined && (!Number.isInteger(body.sort_order) || Math.abs(body.sort_order as number) > 1_000_000)) return { sort_order: '排序取值 -1000000 到 1000000' }
  return null
}

const groupConflict = (g: RouteGroup) => err(409, 'conflict', '路由组已被其他管理员修改，请刷新后重试', { row_version: `current=${g.row_version}` })
const bump = (g: RouteGroup) => {
  g.row_version += 1
  g.updated_at = now()
}
const serving = (n: RoutedNode) => n.status !== 'retired' && n.status !== 'destroyed' && n.serving_status !== 'retired'

function normalizeIds(raw: unknown, field: string): string[] | MockResult {
  const ids = Array.isArray(raw) ? raw : []
  const bad = ids.find((x) => typeof x !== 'string' || !UUID.test(x))
  if (bad !== undefined) return invalid({ [field]: `包含无效的 id：${String(bad)}` })
  return [...new Set((ids as string[]).map((x) => x.toLowerCase()))].sort()
}

const parseRoutes = (routes: readonly Json[]) => routes.map((r, i) => ({ priority: Number(r.priority) || (i + 1) * 10, matcher: (r.matcher as Json) ?? {}, outbound_tag: text(r.outbound_tag), enabled: r.enabled === true, note: text(r.note) }))
const parseOutbounds = (outbounds: ReadonlyArray<{ tag: string; type: string; settings?: unknown }>) => outbounds.map((o) => ({ tag: o.tag.trim(), type: o.type.trim().toLowerCase(), settings: o.settings ?? {} }))

/** 节点生效路由（routing_merge.go）：层 = 节点 → 所在各组 → 全局，带来源 */
function effective(n: RoutedNode) {
  const groups = groupsOfNode(n.id)
  const layers = [
    { source: { scope: 'node' }, outbounds: n.routing.outbounds, routes: n.routing.routes },
    ...groups.map((g) => ({ source: { scope: 'group', group_id: g.id, group_name: g.name }, outbounds: g.outbounds, routes: g.routes })),
    { source: { scope: 'global' }, outbounds: globalRouting.outbounds, routes: globalRouting.routes },
  ]
  const outbounds: Array<Outbound & { source: Json }> = []
  for (const l of [...layers].reverse()) {
    for (const o of l.outbounds) {
      const at = outbounds.findIndex((x) => x.tag === o.tag)
      const row = { tag: o.tag, type: o.type, settings: o.settings, source: l.source }
      if (at >= 0) outbounds[at] = row
      else outbounds.push(row)
    }
  }
  const routes = layers.flatMap((l) => l.routes.filter((r) => r.enabled).map((r) => ({ matcher: r.matcher, outbound: r.outbound_tag, source: l.source })))
  return { groups: groups.map(groupRef), outbounds, routes }
}

export function routeGroupRoutes(nodes: RoutedNode[]): Record<string, MockRoute> {
  setDanglingSource(() => danglingRefs(nodes))
  // 种子成员：香港两台进解锁组，东京进中转组
  if (routeGroups.every((g) => g.members.length === 0)) {
    routeGroups[0]!.members = nodes.filter((n) => n.name.startsWith('香港 0') && n.pool_id).map((n) => n.id)
    routeGroups[1]!.members = nodes.filter((n) => n.name.startsWith('东京')).map((n) => n.id)
  }
  const findGroup = (id: string | undefined) => (id && UUID.test(id) ? routeGroups.find((g) => g.id === id) : undefined)
  const findNode = (id: string | undefined) => nodes.find((n) => n.id === id && n.status !== 'destroyed')
  const live = (ids: readonly string[]) => nodes.filter((n) => ids.includes(n.id) && serving(n)).length

  return {
    'GET /v1/route-groups': (ctx) => {
      if (!ctx.requirePermission('node.read')) return
      ctx.send(200, { groups: ordered(routeGroups).map((g) => out(g, nodes)) })
    },
    'POST /v1/route-groups': async (ctx) => {
      if (!ctx.requirePermission('node.config.publish')) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('route_group_create', () => {
        const extra = unknownField(body, ['name', 'description', 'sort_order'])
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        const bad = fieldErrors(body, true)
        if (bad) return invalid(bad)
        const name = text(body.name).trim()
        if (routeGroups.some((g) => g.name.toLowerCase() === name.toLowerCase())) return err(409, 'conflict', '路由组名称已存在')
        const g = group(name, text(body.description).trim(), Number(body.sort_order ?? 0), [], [])
        g.created_at = g.updated_at = now()
        routeGroups.push(g)
        return { status: 201, body: out(g, nodes) }
      })
    },
    'PATCH /v1/route-groups/:id': async (ctx) => {
      if (!ctx.requirePermission('node.config.publish') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('route_group_update', () => {
        const extra = unknownField(body, ['row_version', 'name', 'description', 'sort_order'])
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        const g = findGroup(ctx.params.id)
        if (!g) return notFound()
        if (!Number.isInteger(body.row_version) || (body.row_version as number) <= 0) return invalid({ row_version: '必须提供正整数版本号' })
        const bad = fieldErrors(body, false)
        if (bad) return invalid(bad)
        if (body.row_version !== g.row_version) return groupConflict(g)
        const name = body.name !== undefined ? text(body.name).trim() : g.name
        if (routeGroups.some((x) => x !== g && x.name.toLowerCase() === name.toLowerCase())) return err(409, 'conflict', '路由组名称已存在')
        const reordered = body.sort_order !== undefined && body.sort_order !== g.sort_order
        g.name = name
        if (body.description !== undefined) g.description = text(body.description).trim()
        if (body.sort_order !== undefined) g.sort_order = body.sort_order as number
        bump(g)
        return { status: 200, body: { group: out(g, nodes), affected_nodes: reordered ? live(g.members) : 0 } }
      })
    },
    'DELETE /v1/route-groups/:id': async (ctx) => {
      if (!ctx.requirePermission('node.config.publish') || !ctx.requireReauth()) return
      if (emptyBody(ctx)) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('route_group_delete', () => {
        const g = findGroup(ctx.params.id)
        if (!g) return notFound()
        if (!Number.isInteger(body.row_version) || (body.row_version as number) <= 0) return invalid({ row_version: '必须提供正整数版本号' })
        if (body.row_version !== g.row_version) return groupConflict(g)
        const before = danglingRefs(nodes)
        const at = routeGroups.indexOf(g)
        routeGroups.splice(at, 1)
        const refused = newDangling(before, nodes, '组内出站仍被成员节点的规则引用')
        if (refused) {
          routeGroups.splice(at, 0, g)
          return refused
        }
        for (const n of nodes) if (g.members.includes(n.id)) n.row_version += 1
        return { status: 200, body: { deleted: true, affected_nodes: live(g.members) } }
      })
    },
    'GET /v1/route-groups/:id/routing': (ctx) => {
      if (!ctx.requirePermission('node.read')) return
      const g = findGroup(ctx.params.id)
      if (!g) return reply(ctx, notFound())
      ctx.send(200, { row_version: g.row_version, outbounds: g.outbounds, routes: g.routes })
    },
    'PUT /v1/route-groups/:id/routing': async (ctx) => {
      if (!ctx.requirePermission('node.config.publish') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('route_group_routing_publish', () => {
        const extra = unknownField(body, ['row_version', 'outbounds', 'routes'])
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        const outbounds = (Array.isArray(body.outbounds) ? body.outbounds : []) as Array<{ tag: string; type: string; settings?: unknown }>
        const routes = (Array.isArray(body.routes) ? body.routes : []) as Json[]
        const g = findGroup(ctx.params.id)
        if (!g) return notFound()
        // 组规则只能指向内置、本组与全局出站
        const bad = validateRouting(outbounds, routes, globalRouting.outbounds.map((o) => o.tag))
        if (bad) return bad
        if (!Number.isInteger(body.row_version) || (body.row_version as number) <= 0) return invalid({ row_version: '必须提供正整数版本号' })
        if (body.row_version !== g.row_version) return groupConflict(g)
        const before = danglingRefs(nodes)
        const prev = { outbounds: g.outbounds, routes: g.routes }
        g.outbounds = parseOutbounds(outbounds)
        g.routes = parseRoutes(routes)
        const refused = newDangling(before, nodes, '要删除的组内出站仍被成员节点的规则引用')
        if (refused) {
          Object.assign(g, prev)
          return refused
        }
        bump(g)
        return { status: 200, body: { ok: true, row_version: g.row_version, affected_nodes: live(g.members) } }
      })
    },
    'PUT /v1/route-groups/:id/members': async (ctx) => {
      if (!ctx.requirePermission('node.config.publish') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('route_group_members_update', () => {
        const extra = unknownField(body, ['row_version', 'node_ids'])
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        const g = findGroup(ctx.params.id)
        if (!g) return notFound()
        const ids = normalizeIds(body.node_ids, 'node_ids')
        if (!Array.isArray(ids)) return ids
        if (!Number.isInteger(body.row_version) || (body.row_version as number) <= 0) return invalid({ row_version: '必须提供正整数版本号' })
        if (body.row_version !== g.row_version) return groupConflict(g)
        const missing = ids.filter((id) => !nodes.some((n) => n.id === id))
        if (missing.length) return invalid({ node_ids: `节点不存在：${missing.join('、')}` })
        const before = danglingRefs(nodes)
        const prev = g.members
        g.members = ids
        const refused = newDangling(before, nodes, '移出组的节点仍有规则指向组内出站')
        if (refused) {
          g.members = prev
          return refused
        }
        bump(g)
        const changed = [...prev.filter((id) => !ids.includes(id)), ...ids.filter((id) => !prev.includes(id))]
        for (const n of nodes) if (changed.includes(n.id)) n.row_version += 1
        return { status: 200, body: { ok: true, row_version: g.row_version, affected_nodes: live(changed) } }
      })
    },
    'PUT /v1/nodes/:id/route-groups': async (ctx) => {
      if (!ctx.requirePermission('node.config.publish')) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const n = findNode(ctx.params.id)
      if (!n) return reply(ctx, notFound())
      const ids = normalizeIds(body.group_ids, 'group_ids')
      if (!Array.isArray(ids)) return reply(ctx, ids)
      if (!Number.isInteger(body.row_version) || (body.row_version as number) <= 0) return reply(ctx, invalid({ row_version: '必须提供正整数版本号' }))
      if (body.row_version !== n.row_version) return reply(ctx, err(409, 'conflict', '节点已被其他管理员修改，请刷新后重试', { row_version: `current=${n.row_version}` }))
      const missing = ids.filter((id) => !routeGroups.some((g) => g.id === id))
      if (missing.length) return reply(ctx, invalid({ group_ids: `路由组不存在：${missing.join('、')}` }))
      const before = danglingRefs(nodes)
      const prev = routeGroups.filter((g) => g.members.includes(n.id))
      for (const g of routeGroups) g.members = g.members.filter((id) => id !== n.id)
      for (const g of routeGroups) if (ids.includes(g.id)) g.members.push(n.id)
      const refused = newDangling(before, nodes, '本节点仍有规则指向要退出的组的出站')
      if (refused) {
        for (const g of routeGroups) g.members = g.members.filter((id) => id !== n.id)
        for (const g of prev) g.members.push(n.id)
        return reply(ctx, refused)
      }
      for (const g of routeGroups) if (prev.includes(g) !== ids.includes(g.id)) bump(g)
      n.row_version += 1
      ctx.send(200, { ok: true, row_version: n.row_version })
    },
    'GET /v1/nodes/:id/routing/effective': (ctx) => {
      if (!ctx.requirePermission('node.read')) return
      const n = findNode(ctx.params.id)
      if (!n) return reply(ctx, notFound())
      ctx.send(200, effective(n))
    },
  }
}
