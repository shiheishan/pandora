import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { balanceEntriesSchema } from '../src/admin/screens/users/schemas'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · admin users balance ledger', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>

  const ADMIN_USER = '1a2b3c41-0000-4000-8000-000000000001'
  const SEED_USER = '1a2b3c42-0000-4000-8000-000000000002'
  const USER_3 = '1a2b3c43-0000-4000-8000-000000000003'
  const USER_4 = '1a2b3c44-0000-4000-8000-000000000004'

  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
  })
  afterAll(() => close(server))

  const get = (path: string) => mockFetch(base, auth, 'GET', path)
  const send = (method: string, path: string, body: unknown, key?: string) => mockFetch(base, auth, method, path, body, key)
  const reauth = async () => {
    const res = await fetch(`${base}/v1/auth/reauth`, { method: 'POST', headers: auth, body: JSON.stringify({ password: MOCK_ACCOUNTS.admin.password }) })
    auth = { Authorization: `Bearer ${((await res.json()) as { access_token: string }).access_token}` }
  }

  it('lists balance entries newest first with a running balance that ends at the current balance', async () => {
    const res = await get(`/v1/users/${SEED_USER}/balance-entries`)
    expect(res.status).toBe(200)
    const parsed = balanceEntriesSchema.parse(await res.json())
    expect(parsed.total).toBe(3)

    const detail = (await (await get(`/v1/users/${SEED_USER}`)).json()) as { balance: number; recent_orders: Array<{ id: string }> }
    expect(parsed.entries[0]!.balance_after).toBe(detail.balance)

    const n = parsed.entries.length
    for (let i = 0; i < n - 1; i++) {
      expect(parsed.entries[i]!.balance_after - parsed.entries[i]!.delta).toBe(parsed.entries[i + 1]!.balance_after)
    }
    const oldest = parsed.entries[n - 1]!
    expect(oldest.balance_after).toBe(oldest.delta)

    const withOrder = parsed.entries.filter((e) => e.order_id !== null)
    expect(withOrder).toHaveLength(1)
    const orderIds = new Set(detail.recent_orders.map((o) => o.id))
    expect(orderIds.has(withOrder[0]!.order_id!)).toBe(true)
    for (const e of parsed.entries) {
      expect('order_id' in e).toBe(true)
    }
  })

  it('returns an empty page for a user without balance activity or an unknown id', async () => {
    const adminRes = await get(`/v1/users/${ADMIN_USER}/balance-entries`)
    expect(adminRes.status).toBe(200)
    expect(balanceEntriesSchema.parse(await adminRes.json())).toEqual({ entries: [], total: 0 })

    const unknownRes = await get('/v1/users/00000000-0000-4000-8000-0000000000ff/balance-entries')
    expect(unknownRes.status).toBe(200)
    expect(balanceEntriesSchema.parse(await unknownRes.json())).toEqual({ entries: [], total: 0 })
  })

  it('pages like the Go pagination helper', async () => {
    const full = balanceEntriesSchema.parse(await (await get(`/v1/users/${SEED_USER}/balance-entries`)).json())

    const paged = balanceEntriesSchema.parse(await (await get(`/v1/users/${SEED_USER}/balance-entries?limit=1&offset=1`)).json())
    expect(paged.entries).toHaveLength(1)
    expect(paged.total).toBe(3)
    expect(paged.entries[0]).toEqual(full.entries[1])

    const capped = balanceEntriesSchema.parse(await (await get(`/v1/users/${SEED_USER}/balance-entries?limit=500`)).json())
    expect(capped.entries).toHaveLength(3)
    expect(capped.total).toBe(3)

    const badLimit = await get(`/v1/users/${SEED_USER}/balance-entries?limit=0`)
    expect(badLimit.status).toBe(422)
    const badLimitBody = (await badLimit.json()) as { error: { code: string; fields: { limit: string } } }
    expect(badLimitBody.error.code).toBe('validation_failed')
    expect(badLimitBody.error.fields.limit).toBe('格式不正确')

    const badOffset = await get(`/v1/users/${SEED_USER}/balance-entries?offset=-1`)
    expect(badOffset.status).toBe(422)
    const badOffsetBody = (await badOffset.json()) as { error: { fields: { offset: string } } }
    expect(badOffsetBody.error.fields.offset).toBe('格式不正确')

    const notUuid = await get('/v1/users/not-a-uuid/balance-entries')
    expect(notUuid.status).toBe(404)
    const notUuidBody = (await notUuid.json()) as { error: { code: string } }
    expect(notUuidBody.error.code).toBe('not_found')
  })

  it('appends a manual adjustment after a balance change', async () => {
    await reauth()

    const before = balanceEntriesSchema.parse(await (await get(`/v1/users/${USER_3}/balance-entries`)).json())
    const adj = await send('POST', `/v1/users/${USER_3}/balance`, { amount: -100, reason: '测试扣减原因' }, 'ledger-adj-1')
    expect(adj.status).toBe(200)
    const newBalance = ((await adj.json()) as { balance: number }).balance

    const after = balanceEntriesSchema.parse(await (await get(`/v1/users/${USER_3}/balance-entries`)).json())
    expect(after.total).toBe(before.total + 1)
    expect(after.entries[0]).toMatchObject({
      kind: 'balance_adjusted',
      delta: -100,
      memo: '测试扣减原因',
      order_id: null,
    })
    expect(after.entries[0]!.balance_after).toBe(newBalance)
    expect(after.entries[1]!.balance_after).toBe(newBalance + 100)

    const adj2 = await send('POST', `/v1/users/${USER_4}/balance`, { amount: 200, reason: '测试加款原因' }, 'ledger-adj-2')
    expect(adj2.status).toBe(200)
    const bal4 = ((await adj2.json()) as { balance: number }).balance
    const ledger4 = balanceEntriesSchema.parse(await (await get(`/v1/users/${USER_4}/balance-entries`)).json())
    expect(ledger4.entries[0]).toMatchObject({ delta: 200, balance_after: bal4 })

    const n = ledger4.entries.length
    for (let i = 0; i < n - 1; i++) {
      expect(ledger4.entries[i]!.balance_after - ledger4.entries[i]!.delta).toBe(ledger4.entries[i + 1]!.balance_after)
    }
  })
})
