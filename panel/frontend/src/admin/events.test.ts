import { describe, expect, it } from 'vitest'
import type { SseEvent } from '../core/sse'
import {
  describeEvent,
  eventsInTab,
  markAllRead,
  markRead,
  MAX_EVENTS,
  pushEvent,
  unreadCount,
  type EventItem,
} from './events'

const ID = '0192a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b'

const ev = (event: string, data: unknown): SseEvent => ({ event, data: typeof data === 'string' ? data : JSON.stringify(data) })
const at = () => new Date('2026-10-16T08:00:00Z')

describe('describeEvent', () => {
  it('orders INSERT：标题 / meta / 详情路径 / 页签 / 色调 / 未读', () => {
    const item = describeEvent(ev('orders.changed', { table: 'orders', op: 'INSERT', id: ID }), 1, at())
    expect(item).toMatchObject({
      key: 1,
      title: '新订单',
      meta: `订单与收款 · ${ID.slice(0, 8)}`,
      tone: 'ok',
      tab: 'orders',
      path: `/billing/orders/${ID}`,
      unread: true,
    })
    expect(item.at).toEqual(at())
  })

  it('orders DELETE：退回订单列表路径', () => {
    const item = describeEvent(ev('orders.changed', { table: 'orders', op: 'DELETE', id: ID }), 2, at())
    expect(item).toMatchObject({ title: '订单已删除', path: '/billing/orders' })
  })

  it('orders 没有 id：列表路径，meta 只剩区域名', () => {
    const item = describeEvent(ev('orders.changed', { table: 'orders', op: 'UPDATE' }), 3, at())
    expect(item).toMatchObject({ title: '订单有变化', meta: '订单与收款', path: '/billing/orders' })
  })

  it('subscriptions：详情定位不到用户，恒回 /users/list', () => {
    const item = describeEvent(ev('subscriptions.changed', { table: 'subscriptions', op: 'INSERT', id: ID }), 4, at())
    expect(item).toMatchObject({ title: '新订阅', meta: `用户 · ${ID.slice(0, 8)}`, tab: 'subscriptions', tone: 'info', path: '/users/list' })
  })

  it('tickets.changed tickets 表：详情页；ticket_messages 表：恒回 /tickets，标题固定', () => {
    expect(describeEvent(ev('tickets.changed', { table: 'tickets', op: 'INSERT', id: ID }), 5, at())).toMatchObject({
      title: '新工单',
      meta: `工单 · ${ID.slice(0, 8)}`,
      tab: 'tickets',
      tone: 'danger',
      path: `/tickets/${ID}`,
    })
    const msg = describeEvent(ev('tickets.changed', { table: 'ticket_messages', op: 'INSERT', id: ID, ticket_id: ID }), 6, at())
    expect(msg).toMatchObject({ title: '工单有新消息', path: '/tickets' })
  })

  it('tickets.changed 缺 table：按 tickets 行处理', () => {
    const item = describeEvent(ev('tickets.changed', { op: 'UPDATE', id: ID }), 7, at())
    expect(item).toMatchObject({ title: '工单有更新', path: `/tickets/${ID}` })
  })

  it('ticket.updated：短 id 取 ticket_id，路径用 ticket_id；没有 ticket_id 时退回列表', () => {
    expect(describeEvent(ev('ticket.updated', { ticket_id: ID }), 8, at())).toMatchObject({
      title: '工单有新回复',
      meta: `工单 · ${ID.slice(0, 8)}`,
      path: `/tickets/${ID}`,
    })
    expect(describeEvent(ev('ticket.updated', {}), 9, at())).toMatchObject({ path: '/tickets', meta: '工单' })
  })

  it('nodes.changed servers 表与 nodes 表走不同路径', () => {
    expect(describeEvent(ev('nodes.changed', { table: 'servers', op: 'INSERT', id: ID }), 10, at())).toMatchObject({
      title: '新服务器',
      tab: 'nodes',
      tone: 'warn',
      path: `/nodes/servers/${ID}`,
    })
    expect(describeEvent(ev('nodes.changed', { table: 'nodes', op: 'INSERT', id: ID }), 11, at())).toMatchObject({
      title: '新节点',
      path: `/nodes/nodes/${ID}`,
    })
  })

  it('plans.changed plans 表有详情；prices 等子表恒回 /plans 且标题固定', () => {
    expect(describeEvent(ev('plans.changed', { table: 'plans', op: 'INSERT', id: ID }), 12, at())).toMatchObject({
      title: '新套餐',
      meta: `套餐 · ${ID.slice(0, 8)}`,
      tab: 'other',
      tone: 'info',
      path: `/plans/${ID}`,
    })
    const price = describeEvent(ev('plans.changed', { table: 'prices', op: 'INSERT', id: ID }), 13, at())
    expect(price).toMatchObject({ title: '套餐有变化', path: '/plans' })
  })

  it('announcements：恒回 /content/announce', () => {
    expect(describeEvent(ev('announcements.changed', { table: 'announcements', op: 'UPDATE', id: ID }), 14, at())).toMatchObject({
      title: '公告已更新',
      meta: `内容与外观 · ${ID.slice(0, 8)}`,
      path: '/content/announce',
    })
  })

  it('switches：enabled=false 暂停（warn）、true 恢复（ok）、缺 enabled 有变化（warn），meta 用 code', () => {
    expect(describeEvent(ev('switches.changed', { code: 'billing.checkout', enabled: false }), 15, at())).toMatchObject({
      title: '降级开关已暂停',
      tone: 'warn',
      meta: '安全与运维 · billing.checkout',
      path: '/security/switches',
    })
    expect(describeEvent(ev('switches.changed', { code: 'admin.writes', enabled: true }), 16, at())).toMatchObject({
      title: '降级开关已恢复',
      tone: 'ok',
    })
    expect(describeEvent(ev('switches.changed', { code: 'x' }), 17, at())).toMatchObject({ title: '降级开关有变化', tone: 'warn' })
  })

  it('未知 topic 与非 JSON 数据：标题「数据有变化」、path null、meta 空串', () => {
    expect(describeEvent(ev('mystery.changed', { table: 'orders', op: 'INSERT', id: ID }), 18, at())).toMatchObject({
      title: '数据有变化',
      meta: '',
      tab: 'other',
      tone: 'info',
      path: null,
    })
    const bad = describeEvent({ event: 'orders.changed', data: 'not json' }, 19, at())
    expect(bad).toMatchObject({ title: '订单有变化', meta: '订单与收款', path: '/billing/orders' })
  })
})

describe('pushEvent / eventsInTab', () => {
  const make = (key: number, tab: EventItem['tab']): EventItem => ({
    key,
    title: `t${key}`,
    meta: '',
    at: at(),
    tone: 'info',
    tab,
    path: null,
    unread: true,
  })

  it('新的放最前，截到 MAX_EVENTS', () => {
    let list: EventItem[] = []
    for (let key = 1; key <= MAX_EVENTS + 5; key++) list = pushEvent(list, make(key, 'orders'))
    expect(list).toHaveLength(MAX_EVENTS)
    expect(list[0]!.key).toBe(MAX_EVENTS + 5)
    expect(list.at(-1)!.key).toBe(6)
  })

  it('eventsInTab：all 返回全部，其余按 tab 过滤，保持顺序', () => {
    const list = [make(1, 'orders'), make(2, 'tickets'), make(3, 'orders')]
    expect(eventsInTab(list, 'all')).toEqual(list)
    expect(eventsInTab(list, 'orders').map((i) => i.key)).toEqual([1, 3])
    expect(eventsInTab(list, 'nodes')).toEqual([])
  })
})

describe('markRead / markAllRead / unreadCount', () => {
  const make = (key: number, unread = true): EventItem => ({
    key,
    title: `t${key}`,
    meta: '',
    at: at(),
    tone: 'info',
    tab: 'other',
    path: null,
    unread,
  })

  it('markRead 只改指定的一条', () => {
    const list = [make(1), make(2), make(3)]
    const next = markRead(list, 2)
    expect(next).not.toBe(list)
    expect(next.map((i) => i.unread)).toEqual([true, false, true])
    expect(list.every((i) => i.unread)).toBe(true)
  })

  it('markRead 对不存在的 key 或已读条目返回原数组', () => {
    const list = [make(1), make(2, false)]
    expect(markRead(list, 99)).toBe(list)
    expect(markRead(list, 2)).toBe(list)
  })

  it('markAllRead 全部置为已读；没有未读时返回原数组', () => {
    const list = [make(1), make(2)]
    expect(markAllRead(list).every((i) => !i.unread)).toBe(true)
    const read = [make(1, false)]
    expect(markAllRead(read)).toBe(read)
  })

  it('unreadCount 数未读条数', () => {
    expect(unreadCount([make(1), make(2, false), make(3)])).toBe(2)
    expect(unreadCount([])).toBe(0)
  })
})
