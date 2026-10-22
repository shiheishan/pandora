import { describe, expect, it, vi } from 'vitest'
import { ApiError, createApiClient } from '../core/api'
import { formatMoney } from '../core/format'
import { createTokenStore } from '../core/token'
import type { EventItem } from './events'
import { MAX_EVENTS } from './events'
import {
  eventText,
  fetchEventDetail,
  LOOKUP_MAX,
  lookupTargets,
  nodeLookupSchema,
  orderLookupSchema,
  planLookupSchema,
  serverLookupSchema,
  ticketLookupSchema,
} from './events-lookup'

const ID = '0192a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b'
const at = () => new Date('2026-10-16T08:00:00Z')

const makeItem = (key: number, lookup: EventItem['lookup']): EventItem => ({
  key,
  title: '标题',
  meta: '区域 · abc',
  at: at(),
  tone: 'info',
  tab: 'orders',
  path: null,
  unread: true,
  lookup,
})

describe('lookupTargets', () => {
  it('跳过 null、按 table+id 去重保持顺序、超过 LOOKUP_MAX 截断', () => {
    const items: EventItem[] = [
      makeItem(1, null),
      makeItem(2, { table: 'orders', id: 'a' }),
      makeItem(3, { table: 'orders', id: 'a' }),
      makeItem(4, { table: 'tickets', id: 'b' }),
    ]
    for (let i = 5; i <= LOOKUP_MAX + 3; i++) {
      items.push(makeItem(i, { table: 'plans', id: `p${i}` }))
    }
    const targets = lookupTargets(items)
    expect(targets).toHaveLength(LOOKUP_MAX)
    expect(targets[0]).toEqual({ table: 'orders', id: 'a' })
    expect(targets[1]).toEqual({ table: 'tickets', id: 'b' })
    expect(LOOKUP_MAX).toBe(MAX_EVENTS)
  })
})

describe('eventText', () => {
  const base = makeItem(1, { table: 'orders', id: ID })

  it('none 与 undefined 原样', () => {
    expect(eventText(base, undefined)).toEqual({ title: '标题', meta: '区域 · abc' })
    expect(eventText(base, { kind: 'none' })).toEqual({ title: '标题', meta: '区域 · abc' })
  })

  it('gone：meta 末尾「已删除」；item.meta 为空时只有已删除', () => {
    expect(eventText(base, { kind: 'gone' })).toEqual({ title: '标题', meta: '区域 · abc · 已删除' })
    expect(eventText({ ...base, meta: '' }, { kind: 'gone' })).toEqual({ title: '标题', meta: '已删除' })
  })

  it('五种表 ok 的标题与 meta', () => {
    expect(
      eventText(
        { ...base, title: '新订单' },
        {
          kind: 'ok',
          detail: {
            table: 'orders',
            orderNo: 'PD2610200012',
            userEmail: 'wu.qing@example.com',
            totalAmount: 2500,
            status: 'pending_payment',
          },
        },
      ),
    ).toEqual({
      title: '新订单：PD2610200012',
      meta: `wu.qing@example.com · ${formatMoney(2500)} · 待支付`,
    })

    expect(
      eventText(
        { ...base, title: '新工单', tab: 'tickets' },
        {
          kind: 'ok',
          detail: { table: 'tickets', subject: '无法连接', userEmail: 'u@example.com', status: 'open' },
        },
      ),
    ).toEqual({ title: '新工单：无法连接', meta: 'u@example.com · 待处理' })

    expect(
      eventText(
        { ...base, title: '新节点', tab: 'nodes' },
        { kind: 'ok', detail: { table: 'nodes', name: 'N1', servingStatus: 'active', stale: true } },
      ),
    ).toEqual({ title: '新节点：N1', meta: '离线' })

    expect(
      eventText(
        { ...base, title: '新服务器', tab: 'nodes' },
        { kind: 'ok', detail: { table: 'servers', name: 'S1', status: 'ready', heartbeatOnline: false } },
      ),
    ).toEqual({ title: '新服务器：S1', meta: '心跳中断' })

    expect(
      eventText(
        { ...base, title: '新套餐', tab: 'other' },
        { kind: 'ok', detail: { table: 'plans', name: 'Pro', status: 'active' } },
      ),
    ).toEqual({ title: '新套餐：Pro', meta: '在售' })
  })

  it('未知状态原样', () => {
    expect(
      eventText(base, {
        kind: 'ok',
        detail: { table: 'orders', orderNo: 'X', userEmail: 'a@example.com', totalAmount: 0, status: 'weird' },
      }),
    ).toMatchObject({ meta: expect.stringContaining('weird') })
  })
})

describe('fetchEventDetail', () => {
  // 用真的 ApiClient + 假 fetch：路径编码、query、schema 解析、错误信封转 ApiError 都走真链路
  function setup(respond: (url: URL) => { status: number; body: unknown }) {
    const urls: URL[] = []
    const fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input))
      urls.push(url)
      const { status, body } = respond(url)
      return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
    }) as unknown as typeof globalThis.fetch
    const tokens = createTokenStore('admin', { storage: null, events: null })
    const api = createApiClient({ tokens, fetch, baseUrl: () => 'https://panel.example.test/', retryDelays: [] })
    return { api, urls }
  }
  const notFound = { status: 404, body: { error: { code: 'not_found', message: '资源不存在或无权访问' } } }

  it('订单：路径里的 id 编码，响应映射成 EventDetail（多余字段丢掉）', async () => {
    const env = setup(() => ({
      status: 200,
      body: { order: { id: 'x', order_no: 'PD2610200012', user_email: 'u@example.com', total_amount: 100, status: 'paid', kind: 'new' } },
    }))
    const detail = await fetchEventDetail(env.api, { table: 'orders', id: 'id/with slash' })
    expect(env.urls[0]!.pathname).toBe(`/v1/orders/${encodeURIComponent('id/with slash')}`)
    expect(detail).toEqual({ table: 'orders', orderNo: 'PD2610200012', userEmail: 'u@example.com', totalAmount: 100, status: 'paid' })
  })

  it('工单、服务器、套餐各走自己的单条接口', async () => {
    const env = setup((url) => {
      if (url.pathname.startsWith('/v1/tickets/')) return { status: 200, body: { subject: '无法连接', user_email: 'u@example.com', status: 'resolved' } }
      if (url.pathname.startsWith('/v1/servers/')) return { status: 200, body: { name: 'edge-1', status: 'ready', heartbeat_online: true } }
      return { status: 200, body: { plan: { name: '专业版', status: 'active' } } }
    })
    expect(await fetchEventDetail(env.api, { table: 'tickets', id: ID })).toEqual({ table: 'tickets', subject: '无法连接', userEmail: 'u@example.com', status: 'resolved' })
    expect(await fetchEventDetail(env.api, { table: 'servers', id: ID })).toEqual({ table: 'servers', name: 'edge-1', status: 'ready', heartbeatOnline: true })
    expect(await fetchEventDetail(env.api, { table: 'plans', id: ID })).toEqual({ table: 'plans', name: '专业版', status: 'active' })
    expect(env.urls.map((u) => u.pathname)).toEqual([`/v1/tickets/${ID}`, `/v1/servers/${ID}`, `/v1/plans/${ID}`])
  })

  it('节点：没有单条接口，用 v1/nodes?id=&include_retired=1；有行映射，空数组 → null', async () => {
    let rows: unknown[] = [{ name: '香港 01', serving_status: 'active', stale: true }]
    const env = setup(() => ({ status: 200, body: { nodes: rows, total: rows.length } }))
    expect(await fetchEventDetail(env.api, { table: 'nodes', id: ID })).toEqual({ table: 'nodes', name: '香港 01', servingStatus: 'active', stale: true })
    expect(env.urls[0]!.pathname).toBe('/v1/nodes')
    expect(env.urls[0]!.searchParams.get('id')).toBe(ID)
    expect(env.urls[0]!.searchParams.get('include_retired')).toBe('1')
    rows = []
    expect(await fetchEventDetail(env.api, { table: 'nodes', id: ID })).toBeNull()
  })

  it('404 → null；其他错误原样抛出', async () => {
    expect(await fetchEventDetail(setup(() => notFound).api, { table: 'tickets', id: ID })).toBeNull()
    const failing = setup(() => ({ status: 500, body: { error: { code: 'internal_error', message: 'boom' } } }))
    const err = await fetchEventDetail(failing.api, { table: 'orders', id: ID }).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(500)
  })
})

describe('schemas', () => {
  it('样例 safeParse 成功（含多余字段）', () => {
    expect(
      orderLookupSchema.safeParse({
        order: { order_no: '1', user_email: 'a@example.com', total_amount: 1, status: 'paid', extra: 1 },
        junk: true,
      }).success,
    ).toBe(true)
    expect(
      ticketLookupSchema.safeParse({ subject: 's', user_email: 'a@example.com', status: 'open', x: 1 }).success,
    ).toBe(true)
    expect(
      nodeLookupSchema.safeParse({ nodes: [{ name: 'n', serving_status: 'active', stale: false, y: 2 }] }).success,
    ).toBe(true)
    expect(
      serverLookupSchema.safeParse({ name: 's', status: 'ready', heartbeat_online: true, z: 3 }).success,
    ).toBe(true)
    expect(planLookupSchema.safeParse({ plan: { name: 'p', status: 'active', w: 4 }, extra: 5 }).success).toBe(true)
  })
})
