/**
 * [INPUT]: 依赖 vitest，依赖 ./mock-helpers，依赖 ../dev/mock-api 的 MOCK_ACCOUNTS，依赖 ../dev/mock/admin/plans 的 setSalesEnabled，依赖 ../src/admin/screens/plans/schemas 的套餐与流量包 schema
 * [OUTPUT]: 对外提供套餐（后台-04）假接口的测试
 * [POS]: tests 的套餐假后端守卫：目录能被页面 schema 接住、向导单事务新建与幂等重放、编辑向导的 null = 不动与开新版本、草稿版本全流程、价格与销售开关 503、流量包 updated_at 乐观锁
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { setSalesEnabled } from '../dev/mock/admin/plans'
import { packResponseSchema, packsSchema, planCreatedSchema, planPoolsSchema, planResponseSchema, plansSchema, planUpdatedSchema, priceCreatedSchema, versionCreatedSchema } from '../src/admin/screens/plans/schemas'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · admin plans', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
  })
  afterAll(() => {
    setSalesEnabled(true)
    return close(server)
  })

  // users 假后端的固定套餐 id：标准版，批量筛选按套餐能命中
  const STD = '9c0e1a2b-3333-4b00-8000-000000000001'
  const get = (path: string) => mockFetch(base, auth, 'GET', path)
  const send = (method: string, path: string, body: unknown, key?: string) => mockFetch(base, auth, method, path, body, key)
  const detailOf = async (id: string) => planResponseSchema.parse(await (await get(`/v1/plans/${id}`)).json()).plan
  const poolIds = async () => {
    const res = (await (await get(`/v1/plans/${STD}/pools`)).json()) as { pools: Array<{ id: string; name: string; active_nodes: number }> }
    return { live: res.pools.find((p) => p.active_nodes > 0)!.id, empty: res.pools.find((p) => p.active_nodes === 0)!.id }
  }

  it('serves a catalog the page schemas accept, on the users seed plan ids', async () => {
    const list = plansSchema.parse(await (await get('/v1/plans')).json()).plans
    expect(list.map((p) => p.code)).toEqual(['std', 'pro', 'family', 'trial', 'ent-line'])
    const std = list.find((p) => p.id === STD)!
    expect(std.active_subscriptions).toBeGreaterThan(0)
    expect(std.node_count).toBeGreaterThan(0)
    expect(list.find((p) => p.code === 'pro')!.draft_version_id).not.toBeNull()
    expect(list.find((p) => p.code === 'ent-line')).toMatchObject({ status: 'draft', current_version_id: null })
    const d = await detailOf(STD)
    expect(d.versions[0]!.version).toBeGreaterThan(d.versions[1]!.version)
    expect(planPoolsSchema.parse(await (await get(`/v1/plans/${STD}/pools`)).json()).editable).toBe(false)
    expect(packsSchema.parse(await (await get('/v1/traffic-packs?status=archived')).json()).packs.every((p) => p.status === 'archived')).toBe(true)
  })

  it('creates through the wizard in one transaction, replays, and validates like the Go wizard', async () => {
    const { live, empty } = await poolIds()
    const before = plansSchema.parse(await (await get('/v1/plans')).json()).plans.length
    const bad = await send(
      'POST',
      '/v1/plans/complete',
      { code: 'wiz', name: '向导', prices: [{ billing_interval: 'month', interval_count: 1, unit_amount: 0, currency: 'CNY' }], publish: true },
      'wiz-0',
    )
    expect(await bad.json()).toMatchObject({ error: { fields: { 'prices.0.unit_amount': expect.any(String), pool_ids: expect.any(String) } } })
    // 池里没有可服务节点：发布失败，库里什么也不留（R65）
    const body = {
      code: 'wiz',
      name: '向导',
      traffic_gb: 100,
      max_devices: 2,
      pool_ids: [empty],
      prices: [{ billing_interval: 'month', interval_count: 1, unit_amount: 1900, currency: 'CNY', trial_days: 0 }],
      publish: true,
    }
    const noNodes = await send('POST', '/v1/plans/complete', body, 'wiz-1')
    expect(await noNodes.json()).toMatchObject({ error: { fields: { pool_ids: expect.any(String) } } })
    expect(plansSchema.parse(await (await get('/v1/plans')).json()).plans).toHaveLength(before)

    const ok = { ...body, pool_ids: [live] }
    const created = await send('POST', '/v1/plans/complete', ok, 'wiz-2')
    expect(created.status).toBe(201)
    const r = planCreatedSchema.parse(await created.json())
    expect(r).toMatchObject({ published: true, plan: { status: 'active', code: 'wiz' } })
    expect(r.plan.current_version_id).toBe(r.version_id)
    expect(planCreatedSchema.parse(await (await send('POST', '/v1/plans/complete', ok, 'wiz-2')).json()).plan.id).toBe(r.plan.id)
    expect((await send('POST', '/v1/plans/complete', ok, 'wiz-3')).status).toBe(409)
    expect((await send('POST', '/v1/plans/complete', { ...ok, code: 'wiz-x', max_devices: 0 }, 'wiz-4')).status).toBe(422)
  })

  it('edits through the wizard: null leaves things alone, quota changes roll a published version', async () => {
    const d = await detailOf(STD)
    const basics = {
      code: d.code,
      name: d.name,
      description: d.description,
      visibility: d.visibility,
      sort_order: d.sort_order,
      visible_group_ids: [],
      purchase_limit_per_user: null,
      stock_total: null,
    }
    const stale = await send('PUT', `/v1/plans/${STD}/complete`, { ...basics, expected_row_version: d.row_version - 1 }, 'edit-0')
    expect(await stale.json()).toMatchObject({ error: { code: 'conflict', fields: { row_version: `current=${d.row_version}` } } })
    const res = await send('PUT', `/v1/plans/${STD}/complete`, { ...basics, expected_row_version: d.row_version, traffic_gb: 300, prices: null, pool_ids: null }, 'edit-1')
    const r = planUpdatedSchema.parse(await res.json())
    expect(r.changed).toHaveLength(2)
    const cur = r.plan.versions.find((v) => v.id === r.plan.current_version_id)!
    expect(cur.quotas.find((q) => q.metric === 'traffic.bytes')!.limit).toBe(300 * 1024 ** 3)
    expect(cur.entitlements).toEqual([])
    expect(r.plan.prices).toEqual(d.prices)
  })

  it('walks a draft version: create, reject pool_ids and a stray throttle, save, bind, publish', async () => {
    const d = await detailOf(STD)
    const v = versionCreatedSchema.parse(await (await send('POST', `/v1/plans/${STD}/versions`, undefined, 'ver-1')).json()).version
    expect([v.status, v.quotas, v.pool_ids]).toEqual(['draft', [], []])
    expect((await send('POST', `/v1/plans/${STD}/versions`, undefined, 'ver-2')).status).toBe(409)
    const cur = d.versions.find((x) => x.id === d.current_version_id)!
    const semantics = { ...cur, expected_row_version: v.row_version }
    for (const k of ['id', 'version', 'status', 'frozen_at', 'row_version', 'pool_ids', 'created_by_email', 'created_at'] as const) delete (semantics as Partial<typeof semantics>)[k]
    expect((await send('PUT', `/v1/plans/${STD}/versions/${v.id}`, { ...semantics, pool_ids: [] })).status).toBe(422)
    expect(await (await send('PUT', `/v1/plans/${STD}/versions/${v.id}`, { ...semantics, throttle_kbps: 5000 })).json()).toMatchObject({ error: { fields: { throttle_kbps: expect.any(String) } } })
    const saved = (await (await send('PUT', `/v1/plans/${STD}/versions/${v.id}`, semantics)).json()) as { row_version: number }
    const bound = await send('POST', `/v1/plans/${STD}/pools`, { version_id: v.id, expected_version_row_version: saved.row_version, pool_ids: cur.pool_ids }, 'bind-1')
    const { row_version } = (await bound.json()) as { row_version: number }
    const plan = await detailOf(STD)
    const pub = await send('POST', `/v1/plans/${STD}/versions/${v.id}/publish`, { expected_plan_row_version: plan.row_version, expected_version_row_version: row_version }, 'pub-1')
    expect(pub.status).toBe(200)
    expect((await detailOf(STD)).current_version_id).toBe(v.id)
  })

  it('adds and archives prices, and answers 503 for catalog.publish writes while sales are off', async () => {
    const add = { currency: 'USD', unit_amount: 3000, billing_interval: 'year', interval_count: 1 }
    const created = priceCreatedSchema.parse(await (await send('POST', `/v1/plans/${STD}/prices`, add, 'price-1')).json()).price
    expect((await send('POST', `/v1/plans/${STD}/prices`, add, 'price-2')).status).toBe(409)
    expect((await send('POST', `/v1/plans/${STD}/prices`, { ...add, currency: 'EUR' }, 'price-3')).status).toBe(422)
    const archive = (key: string) => send('POST', `/v1/plans/${STD}/prices/${created.id}/archive`, { expected_row_version: created.row_version }, key)
    expect((await archive('arch-1')).status).toBe(200)
    expect(await (await archive('arch-2')).json()).toMatchObject({ error: { message: '价格已经归档' } })

    setSalesEnabled(false)
    const off = await send('POST', `/v1/plans/${STD}/prices`, { ...add, billing_interval: 'month' }, 'price-4')
    expect(off.status).toBe(503)
    expect(await off.json()).toMatchObject({ error: { code: 'service_unavailable' } })
    const pack = packsSchema.parse(await (await get('/v1/traffic-packs?status=active')).json()).packs[0]!
    const down = await send('POST', `/v1/traffic-packs/${pack.id}/status`, { status: 'archived', expected_updated_at: pack.updated_at }, 'pack-off')
    const archived = packResponseSchema.parse(await down.json()).pack
    expect((await send('POST', `/v1/traffic-packs/${pack.id}/status`, { status: 'active', expected_updated_at: archived.updated_at }, 'pack-on')).status).toBe(503)
    setSalesEnabled(true)
    expect((await send('POST', `/v1/traffic-packs/${pack.id}/status`, { status: 'active', expected_updated_at: archived.updated_at }, 'pack-on')).status).toBe(200)
  })

  it('creates and edits traffic packs with the updated_at lock and rejects unknown fields', async () => {
    const body = { name: '20 GB 小包', traffic_bytes: 20 * 1024 ** 3, currency: 'CNY', unit_amount: 800, recommended: false, sort_order: 5 }
    expect((await send('POST', '/v1/traffic-packs', { ...body, stock: 1 }, 'tp-0')).status).toBe(400)
    const pack = packResponseSchema.parse(await (await send('POST', '/v1/traffic-packs', body, 'tp-1')).json()).pack
    expect(pack).toMatchObject({ status: 'active', sold_count: 0 })
    const edited = packResponseSchema.parse(await (await send('PUT', `/v1/traffic-packs/${pack.id}`, { ...body, unit_amount: 900, expected_updated_at: pack.updated_at }, 'tp-2')).json()).pack
    expect(edited.updated_at).not.toBe(pack.updated_at)
    const stale = await send('PUT', `/v1/traffic-packs/${pack.id}`, { ...body, expected_updated_at: pack.updated_at }, 'tp-3')
    expect(await stale.json()).toMatchObject({ error: { code: 'conflict', fields: { updated_at: `current=${edited.updated_at}` } } })
    expect((await send('POST', `/v1/traffic-packs/${pack.id}/status`, { status: 'active', expected_updated_at: edited.updated_at }, 'tp-4')).status).toBe(409)
  })
})
