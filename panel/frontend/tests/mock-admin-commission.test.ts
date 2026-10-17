import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { userStore } from '../dev/mock/admin/users'
import { overviewSchema, withdrawalsResponse } from '../src/admin/screens/marketing/schemas'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · admin commission', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
  })
  afterAll(() => close(server))

  const get = (path: string) => fetch(`${base}${path}`, { headers: auth })
  const post = (path: string, body: unknown, key?: string) => mockFetch(base, auth, 'POST', path, body, key)
  const del = (path: string) => mockFetch(base, auth, 'DELETE', path)

  it('overview includes distribution and withdraw fields and passes overviewSchema', async () => {
    const overview = await (await get('/v1/commission/overview')).json()
    expect(overview).toMatchObject({
      distribution_enabled: false,
      distribution_levels: [0, 0, 0],
      withdraw_enabled: true,
      withdraw_methods: ['alipay'],
    })
    expect(overviewSchema.safeParse(overview).success).toBe(true)
  })

  it('validates commission config for distribution and withdraw methods', async () => {
    const over = await post('/v1/commission/config', { distribution_enabled: true, distribution_l1: 60, distribution_l2: 30, distribution_l3: 20 })
    expect(over.status).toBe(422)
    expect(await over.json()).toMatchObject({
      error: { code: 'validation_failed', fields: { distribution_l1: '三级比例之和不能超过 100' } },
    })

    const allZero = await post('/v1/commission/config', { distribution_enabled: true })
    expect(allZero.status).toBe(422)
    expect(await allZero.json()).toMatchObject({
      error: { code: 'validation_failed', fields: { distribution_l1: '开启多级分销时至少要给一级设比例' } },
    })

    const okDist = await post('/v1/commission/config', { distribution_enabled: true, distribution_l1: 50, distribution_l2: 30, distribution_l3: 20 })
    expect(okDist.status).toBe(200)
    const afterDist = await (await get('/v1/commission/overview')).json()
    expect(afterDist).toMatchObject({ distribution_enabled: true, distribution_levels: [50, 30, 20] })
    await post('/v1/commission/config', { distribution_enabled: false, distribution_l1: 0, distribution_l2: 0, distribution_l3: 0 })

    const badMethod = await post('/v1/commission/config', { withdraw_methods: ['usdt_trc20', 'alipay', 'bitcoin'] })
    expect(badMethod.status).toBe(422)
    expect(await badMethod.json()).toMatchObject({
      error: { code: 'validation_failed', fields: { withdraw_methods: '提现方式不在可选范围内' } },
    })

    const okMethods = await post('/v1/commission/config', { withdraw_methods: ['usdt_trc20', 'alipay'] })
    expect(okMethods.status).toBe(200)
    const afterMethods = await (await get('/v1/commission/overview')).json()
    expect(afterMethods.withdraw_methods).toEqual(['alipay', 'usdt_trc20'])
    await post('/v1/commission/config', { withdraw_methods: ['alipay'] })

    const emptyMethods = await post('/v1/commission/config', { withdraw_enabled: true, withdraw_methods: [] })
    expect(emptyMethods.status).toBe(422)
    expect(await emptyMethods.json()).toMatchObject({
      error: { code: 'validation_failed', fields: { withdraw_methods: '开着提现时至少要选一种提现方式' } },
    })

    const closedEmpty = await post('/v1/commission/config', { withdraw_enabled: false, withdraw_methods: [] })
    expect(closedEmpty.status).toBe(422)
    expect(await closedEmpty.json()).toMatchObject({
      error: { code: 'validation_failed', fields: { withdraw_methods: '关着提现时也至少要选一种提现方式，重新打开提现时直接生效' } },
    })

    const unknown = await post('/v1/commission/config', { nope: 1 })
    expect(unknown.status).toBe(400)
  })

  it('validates USDT payout reference and paid_usdt_amount on mark paid', async () => {
    const { withdrawals } = (await (await get('/v1/withdrawals')).json()) as {
      withdrawals: Array<{ id: string; email: string; status: string }>
    }
    const row = withdrawals.find((w) => w.email === 'usdt.payee@example.test' && w.status === 'approved')!
    expect(row).toBeDefined()

    const badRef = await post(`/v1/withdrawals/${row.id}/paid`, { payout_reference: 'TX-1' }, 'paid-bad-ref')
    expect(badRef.status).toBe(422)
    expect(await badRef.json()).toMatchObject({
      error: { code: 'validation_failed', fields: { payout_reference: '交易哈希应为 0x 开头加 64 位十六进制' } },
    })

    const hash = `0x${'ab'.repeat(32)}`
    const badAmount = await post(`/v1/withdrawals/${row.id}/paid`, { payout_reference: hash, paid_usdt_amount: '1.1234567' }, 'paid-bad-amt')
    expect(badAmount.status).toBe(422)
    expect(await badAmount.json()).toMatchObject({
      error: { code: 'validation_failed', fields: { paid_usdt_amount: '实付 USDT 数量需大于 0，最多 6 位小数' } },
    })

    const ok = await post(`/v1/withdrawals/${row.id}/paid`, { payout_reference: hash, paid_usdt_amount: '88.5' }, 'paid-ok-88')
    expect(ok.status).toBe(200)
    const { withdrawals: after } = (await (await get('/v1/withdrawals')).json()) as {
      withdrawals: Array<{ id: string; status: string; paid_usdt_amount: string | null; payout_reference: string }>
    }
    const paid = after.find((w) => w.id === row.id)!
    expect(paid.status).toBe('paid')
    expect(paid.paid_usdt_amount).toBe('88.5')
    expect(paid.payout_reference).toBe(hash)
  })

  it('filters withdrawals by comma-separated statuses like the Go handler', async () => {
    const list = async (q: string) => ((await (await get(`/v1/withdrawals${q}`)).json()) as { withdrawals: Array<{ status: string }> }).withdrawals
    const all = await list('')
    expect(withdrawalsResponse.safeParse({ withdrawals: all }).success).toBe(true)
    expect(all.every((w) => !('currency' in w))).toBe(true)
    const open = await list('?status=requested,%20approved,')
    expect(open.length).toBeGreaterThan(0)
    expect(open.every((w) => w.status === 'requested' || w.status === 'approved')).toBe(true)
    const done = await list('?status=paid,rejected,paid')
    expect(done.length).toBeGreaterThan(0)
    expect(done.every((w) => w.status === 'paid' || w.status === 'rejected')).toBe(true)
    expect(open.length + done.length).toBe(all.length)

    const bad = await get('/v1/withdrawals?status=requested,reviewing')
    expect(bad.status).toBe(422)
    expect(await bad.json()).toMatchObject({
      error: { code: 'validation_failed', fields: { status: '状态只能是 requested、approved、rejected 或 paid' } },
    })
  })

  it('lists, creates, and deletes commission overrides', async () => {
    const listed = (await (await get('/v1/commission/overrides')).json()) as { overrides: Array<{ user_id: string; email: string }> }
    expect(listed.overrides.length).toBeGreaterThan(0)

    // 种子单独设置占了第一个非管理员用户，这里用第二个
    const target = userStore.filter((u) => !u.is_admin)[1]!.email
    const created = await post('/v1/commission/overrides', { email: target.toUpperCase(), scope: 'first_order' })
    expect(created.status).toBe(200)
    const body = (await created.json()) as { override: { email: string; scope: string } }
    expect(body.override.email).toBe(target)
    expect(body.override.scope).toBe('first_order')
    const userId = (body.override as unknown as { user_id: string }).user_id

    const noUser = await post('/v1/commission/overrides', { email: 'nobody@example.test', rate_percent: 10 })
    expect(noUser.status).toBe(422)
    expect(await noUser.json()).toMatchObject({ error: { fields: { email: '找不到这个邮箱的用户' } } })

    const badRate = await post('/v1/commission/overrides', { email: target, rate_percent: 51 })
    expect(badRate.status).toBe(422)
    expect(await badRate.json()).toMatchObject({ error: { fields: { rate_percent: '佣金比例需在 0 到 50 之间' } } })

    const emailOnly = await post('/v1/commission/overrides', { email: target })
    expect(emailOnly.status).toBe(422)
    expect(await emailOnly.json()).toMatchObject({
      error: { fields: { rate_percent: '比例与计佣范围至少设一项；都跟全局走就删除这条设置' } },
    })

    const removed = await del(`/v1/commission/overrides/${userId}`)
    expect(removed.status).toBe(200)
    const again = await del(`/v1/commission/overrides/${userId}`)
    expect(again.status).toBe(404)
    const badId = await del('/v1/commission/overrides/not-a-uuid')
    expect(badId.status).toBe(404)
  })
})
