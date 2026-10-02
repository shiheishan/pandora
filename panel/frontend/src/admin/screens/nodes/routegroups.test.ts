/**
 * [INPUT]: 依赖 vitest，依赖 ./logic 的路由组纯函数（表单校验 / 新建体 / PATCH 差量、成员比较、可引用出站、来源文字），依赖 ./schemas 的路由组、节点路由与生效预览 schema
 * [OUTPUT]: 对外提供路由组（00096）前端纯逻辑与 schema 边界的单元测试
 * [POS]: admin/screens/nodes 的路由组单元测试：表单边界与 nodefabric.normalizeRouteGroupFields 同口径、PATCH 只带改了的字段、成员集合顺序无关、可引用出站按范围先到先得、按 tag 原样去重，引用计数与改名联动按原样精确匹配（与后端、pdnd 同口径）、生效来源文字；schema 接住 Go 的形状（RoutingSource 的 omitempty、节点路由的 groups 为 null 时归一）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it } from 'vitest'
import { groupCreateBody, groupFormErrors, groupFormFrom, groupPatchBody, referenceOutbounds, renameOutbound, rulesUsing, sameIds, sourceLabel, sourceTone } from './logic'
import { effectiveRoutingSchema, nodeRoutingSchema, routeGroupsResponse } from './schemas'

const G = { id: '0199a000-0000-7000-8000-000000000001', name: '香港 · 解锁', description: '', sort_order: 10, row_version: 3 }

describe('route group form', () => {
  it('validates like normalizeRouteGroupFields', () => {
    expect(groupFormErrors(groupFormFrom(G))).toEqual({})
    expect(groupFormErrors({ name: '  ', description: '', sortOrder: '0' })).toEqual({ name: '名称为 1 到 64 个字符' })
    expect(groupFormErrors({ name: '组'.repeat(65), description: 'x'.repeat(501), sortOrder: '1.5' })).toEqual({ name: '名称为 1 到 64 个字符', description: '说明最多 500 个字符', sort_order: '排序是 -1000000 到 1000000 的整数' })
    expect(groupFormErrors({ name: '组'.repeat(64), description: '', sortOrder: '-1000000' })).toEqual({})
    expect(groupFormErrors({ name: 'a', description: '', sortOrder: '1000001' }).sort_order).toBeDefined()
  })

  it('creates with all fields and patches only what changed', () => {
    expect(groupCreateBody({ name: ' 日本 ', description: ' 中转 ', sortOrder: ' 20 ' })).toEqual({ name: '日本', description: '中转', sort_order: 20 })
    expect(groupPatchBody(G, groupFormFrom(G))).toBeNull()
    expect(groupPatchBody(G, { ...groupFormFrom(G), sortOrder: '5' })).toEqual({ row_version: 3, sort_order: 5 })
    expect(groupPatchBody(G, { name: '新名', description: '说明', sortOrder: '10' })).toEqual({ row_version: 3, name: '新名', description: '说明' })
  })

  it('compares member sets regardless of order', () => {
    expect(sameIds(['a', 'b'], ['b', 'a'])).toBe(true)
    expect(sameIds(['a'], ['a', 'b'])).toBe(false)
    expect(sameIds([], [])).toBe(true)
  })
})

describe('route group references and sources', () => {
  it('lists referenceable outbounds, first scope wins, tags compared as-is', () => {
    const refs = referenceOutbounds(
      [
        { label: '路由组 · 香港', tags: ['UNLOCK', 'hk'] },
        { label: '全局', tags: ['UNLOCK', 'unlock', 'pub'] },
      ],
      ['HK', 'pub'],
    )
    expect(refs).toEqual([
      ['UNLOCK', 'UNLOCK（路由组 · 香港）'],
      ['hk', 'hk（路由组 · 香港）'],
      ['unlock', 'unlock（全局）'],
    ])
  })

  it('counts and renames references exactly, like the backend and pdnd', () => {
    const rows = [
      { kind: 'port' as const, value: '1', outbound: 'HK', enabled: true, note: '' },
      { kind: 'port' as const, value: '2', outbound: 'hk', enabled: true, note: '' },
    ]
    expect(rulesUsing(rows, ' HK ')).toBe(1)
    expect(renameOutbound(rows, 'HK', 'HK-2').map((r) => r.outbound)).toEqual(['HK-2', 'hk'])
  })

  it('labels each effective source', () => {
    expect(sourceLabel({ scope: 'node' })).toBe('本节点')
    expect(sourceLabel({ scope: 'global' })).toBe('全局')
    expect(sourceLabel({ scope: 'group', group_id: G.id, group_name: '香港' })).toBe('路由组 · 香港')
    expect([sourceTone({ scope: 'node' }), sourceTone({ scope: 'group' }), sourceTone({ scope: 'global' })]).toEqual(['info', 'ok', 'neutral'])
  })
})

describe('route group schemas', () => {
  it('accepts the Go shapes', () => {
    const list = routeGroupsResponse.parse({ groups: [{ ...G, outbound_count: 1, rule_count: 2, members: [{ id: G.id, name: 'n' }], created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-01T00:00:00Z' }] })
    expect(list.groups[0]!.members).toHaveLength(1)
    // nil 切片序列化成 null：节点路由的 groups / outbounds / routes 归一成 []
    expect(nodeRoutingSchema.parse({ row_version: 1, groups: null, outbounds: null, routes: null })).toEqual({ row_version: 1, groups: [], outbounds: [], routes: [] })
    const eff = effectiveRoutingSchema.parse({
      groups: [{ id: G.id, name: '香港', sort_order: 10 }],
      outbounds: [{ tag: 'pub', type: 'http', settings: {}, source: { scope: 'group', group_id: G.id, group_name: '香港' } }],
      routes: [{ matcher: { port: ['443'] }, outbound: 'pub', source: { scope: 'global' } }],
    })
    expect(eff.routes[0]!.source.group_name).toBeUndefined()
    expect(() => effectiveRoutingSchema.parse({ groups: [], outbounds: [], routes: [{ matcher: {}, outbound: 'x', source: { scope: 'pool' } }] })).toThrow()
  })
})
