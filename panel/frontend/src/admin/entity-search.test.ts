import { describe, expect, it } from 'vitest'
import {
  ENTITY_QUERY_MAX,
  entityGroups,
  isEntityItem,
  nodeItems,
  normalizeEntityQuery,
  orderItems,
  userItems,
} from './entity-search'

// 数据全是虚构值（example.test），只测纯函数
const user = (over: Partial<Parameters<typeof userItems>[0][number]> = {}) => ({
  id: 'u-1',
  email: 'one@example.test',
  status: 'active',
  current_subscription: null,
  ...over,
})

const order = (over: Partial<Parameters<typeof orderItems>[0][number]> = {}) => ({
  id: 'o-1',
  order_no: 'NO-1001',
  user_email: 'one@example.test',
  status: 'pending_payment',
  total_amount: 6800,
  ...over,
})

const node = (over: Partial<Parameters<typeof nodeItems>[0][number]> = {}) => ({
  id: 'n-1',
  name: 'hk-01',
  serving_status: 'active',
  stale: false,
  node_type: 'vless',
  server_name: 'sv-01',
  ...over,
})

describe('userItems', () => {
  it('active 有订阅：sub 是 套餐 · 生效中，path 指向详情', () => {
    const [item] = userItems([
      user({ id: 'u-1', current_subscription: { plan_name: '基础套餐', status: 'active' } }),
    ])
    expect(item).toMatchObject({
      key: 'u:u-1',
      kind: '用户',
      title: 'one@example.test',
      sub: '基础套餐 · 生效中',
      mono: true,
      module: 'users',
      tab: 'list',
      path: '/users/list/u-1',
    })
  })

  it('banned 无订阅：先用户状态再「无订阅」', () => {
    const [item] = userItems([user({ status: 'banned', id: 'u-2' })])
    expect(item?.sub).toBe('已禁用 · 无订阅')
  })

  it('expired 订阅：套餐 · 已过期', () => {
    const [item] = userItems([
      user({ current_subscription: { plan_name: '年付', status: 'expired' } }),
    ])
    expect(item?.sub).toBe('年付 · 已过期')
  })

  it('超过 4 条只留前 4 条', () => {
    const rows = Array.from({ length: 6 }, (_, i) => user({ id: `u-${i}` }))
    expect(userItems(rows)).toHaveLength(4)
  })

  it('路径里的 id 被编码', () => {
    const [item] = userItems([user({ id: 'u/a b' })])
    expect(item?.path).toBe('/users/list/u%2Fa%20b')
  })

  it('active 用户不带状态前缀', () => {
    const [item] = userItems([user()])
    expect(item?.sub).toBe('无订阅')
  })
})

describe('orderItems', () => {
  it('金额与状态：¥68.00 · 待支付 · 邮箱', () => {
    const [item] = orderItems([order()])
    expect(item).toMatchObject({
      key: 'o:o-1',
      kind: '订单',
      title: 'NO-1001',
      sub: '¥68.00 · 待支付 · one@example.test',
      path: '/billing/orders/o-1',
    })
    expect(formatMoneyShape(item?.sub))
  })

  it('fulfilled 显示已支付', () => {
    const [item] = orderItems([order({ status: 'fulfilled' })])
    expect(item?.sub).toBe('¥68.00 · 已支付 · one@example.test')
  })

  it('超过 4 条只留前 4 条，未知状态原样', () => {
    const rows = Array.from({ length: 5 }, (_, i) => order({ id: `o-${i}` }))
    expect(orderItems(rows)).toHaveLength(4)
    const [item] = orderItems([order({ status: 'mystery' })])
    expect(item?.sub).toContain('· mystery ·')
  })
})

describe('nodeItems', () => {
  it('active 在线：协议 · 服务器 · 在线', () => {
    const [item] = nodeItems([node()])
    expect(item).toMatchObject({
      key: 'n:n-1',
      kind: '节点',
      title: 'hk-01',
      sub: 'vless · sv-01 · 在线',
      path: '/nodes/nodes/n-1',
    })
  })

  it('active + stale 显示离线', () => {
    const [item] = nodeItems([node({ stale: true })])
    expect(item?.sub).toBe('vless · sv-01 · 离线')
  })

  it('retired 显示已退役', () => {
    const [item] = nodeItems([node({ serving_status: 'retired' })])
    expect(item?.sub).toBe('vless · sv-01 · 已退役')
  })

  it('node_type 与 server_name 为 null 时给占位文案', () => {
    const [item] = nodeItems([node({ node_type: null, server_name: null })])
    expect(item?.sub).toBe('未设协议 · 未绑定服务器 · 在线')
  })
})

describe('entityGroups', () => {
  const u = userItems([user()])
  const o = orderItems([order()])
  const n = nodeItems([node()])

  it('跳过空组与 undefined，顺序固定为 用户、订单、节点', () => {
    const groups = entityGroups({ users: u, orders: undefined, nodes: n })
    expect(groups.map((g) => g.label)).toEqual(['用户', '节点'])
    expect(groups[0]?.items).toEqual(u)
  })

  it('空数组也算没有条目', () => {
    const groups = entityGroups({ users: [], orders: o, nodes: [] })
    expect(groups.map((g) => g.label)).toEqual(['订单'])
  })

  it('全空返回空数组', () => {
    expect(entityGroups({})).toEqual([])
  })
})

describe('isEntityItem', () => {
  it('区分带 path 的实体候选与普通 PaletteItem', () => {
    const entity = userItems([user()])[0]!
    expect(isEntityItem(entity)).toBe(true)
    expect(
      isEntityItem({ key: 'users', kind: '模块', title: '用户', sub: '运营', module: 'users', tab: null }),
    ).toBe(false)
  })
})

describe('normalizeEntityQuery', () => {
  it('去首尾空白并截到 100', () => {
    expect(normalizeEntityQuery('  hello  ')).toBe('hello')
    expect(normalizeEntityQuery('x'.repeat(ENTITY_QUERY_MAX + 50))).toHaveLength(ENTITY_QUERY_MAX)
  })
})

/** 订单金额必须是 ¥ 开头两位小数的形（formatMoney(6800) → ¥68.00） */
function formatMoneyShape(sub: string | undefined): void {
  expect(sub).toMatch(/^¥68\.00 · /)
}
