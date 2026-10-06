import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · admin tickets', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
  })
  afterAll(() => close(server))

  interface Ticket { id: string; ticket_no: string; message_count: number; last_reply_at: string; related_order: { id: string; order_no: string } | null }
  const get = async <T>(path: string): Promise<T> => {
    const res = await mockFetch(base, auth, 'GET', path)
    expect(res.status).toBe(200)
    return (await res.json()) as T
  }

  it('R114: detail joins the related order and counts messages like the queue', async () => {
    const queue = await get<{ tickets: Ticket[] }>('/v1/tickets?limit=100')
    expect(queue.tickets.every((t) => t.related_order === null)).toBe(true)
    const row = queue.tickets.find((t) => t.ticket_no.endsWith('4818'))!
    const detail = await get<Ticket & { messages: Array<{ created_at: string }> }>(`/v1/tickets/${row.id}`)
    expect(detail.related_order).toMatchObject({ order_no: expect.any(String) })
    expect(detail.message_count).toBe(row.message_count)
    expect(detail.message_count).toBe(detail.messages.length)
    expect(detail.last_reply_at).toBe(row.last_reply_at)
    expect(detail.last_reply_at).toBe(detail.messages.at(-1)!.created_at)
    // 关联的是一张真实存在的订单
    expect((await mockFetch(base, auth, 'GET', `/v1/orders/${detail.related_order!.id}`)).status).toBe(200)
  })
})
