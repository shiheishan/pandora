import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { deliverableNodesInPool } from '../dev/mock/admin/nodes'
import {
  planPoolsSchema,
  planResponseSchema,
  plansSchema,
} from '../src/admin/screens/plans/schemas'
import { auditResponse } from '../src/admin/screens/security/schemas'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · admin plans (two-kind redesign)', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>

  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
  })

  afterAll(() => close(server))

  const STD = '9c0e1a2b-3333-4b00-8000-000000000001'
  const FAMILY = '9c0e1a2b-3333-4b00-8000-000000000003'

  const get = (path: string) => mockFetch(base, auth, 'GET', path)
  const send = (method: string, path: string, body?: unknown, key?: string) =>
    mockFetch(base, auth, method, path, body, key)
  const detailOf = async (id: string) =>
    planResponseSchema.parse(await (await get(`/v1/plans/${id}`)).json()).plan

  const poolIds = async () => {
    const res = (await (await get(`/v1/plans/${STD}/pools`)).json()) as {
      pools: Array<{ id: string; name: string; active_nodes: number; deliverable_nodes: number }>
    }
    return {
      live: res.pools.find((p) => p.active_nodes > 0 && p.deliverable_nodes > 0)!.id,
      empty: res.pools.find((p) => p.active_nodes === 0)!.id,
    }
  }

  // 1. 列表
  it('1. lists plans match schemas, seed order, counts, and filters', async () => {
    const res = await get('/v1/plans')
    expect(res.status).toBe(200)
    const list = plansSchema.parse(await res.json()).plans

    // 代码依次是 standard、pro、family、trial、enterprise-line、annual-once
    expect(list.map((p) => p.code)).toEqual([
      'standard',
      'pro',
      'family',
      'trial',
      'enterprise-line',
      'annual-once',
    ])

    // 标准版 active_subscriptions > 0、node_count > 0
    const std = list.find((p) => p.code === 'standard')!
    expect(std.active_subscriptions).toBeGreaterThan(0)
    expect(std.node_count).toBeGreaterThan(0)

    // 专业版 draft_version_id 非空
    const pro = list.find((p) => p.code === 'pro')!
    expect(pro.draft_version_id).not.toBeNull()

    // ?kind=one_time 只回两个一次性
    const oneTimeList = plansSchema.parse(await (await get('/v1/plans?kind=one_time')).json()).plans
    expect(oneTimeList.map((p) => p.code)).toEqual(['trial', 'annual-once'])

    // ?status=bogus 回 422 带 fields.status
    const badStatus = await get('/v1/plans?status=bogus')
    expect(badStatus.status).toBe(422)
    expect(await badStatus.json()).toMatchObject({
      error: { code: 'validation_failed', fields: { status: expect.any(String) } },
    })

    // ?q=PRO 命中专业版
    const qList = plansSchema.parse(await (await get('/v1/plans?q=PRO')).json()).plans
    expect(qList.map((p) => p.code)).toEqual(['pro'])
  })

  // 2. 详情
  it('2. detail responds with descending versions, archived prices, and deliverable_nodes', async () => {
    const d = await detailOf(STD)
    // 版本降序
    for (let i = 0; i < d.versions.length - 1; i++) {
      expect(d.versions[i]!.version).toBeGreaterThan(d.versions[i + 1]!.version)
    }

    // 标准版 prices 含一条 archived
    expect(d.prices.some((p) => p.status === 'archived')).toBe(true)

    // pools 接口里 deliverable_nodes === deliverableNodesInPool(id) 且 <= active_nodes
    const poolsRes = planPoolsSchema.parse(await (await get(`/v1/plans/${STD}/pools`)).json())
    expect(poolsRes.pools.length).toBeGreaterThan(0)
    for (const pool of poolsRes.pools) {
      expect(pool.deliverable_nodes).toBe(deliverableNodesInPool(pool.id))
      expect(pool.deliverable_nodes).toBeLessThanOrEqual(pool.active_nodes)
    }
  })

  // 3. 新建订阅套餐
  it('3. creates subscription plan, verifies reauth, validation, publish gate, and autoCode', async () => {
    const { live, empty } = await poolIds()

    // 缺 reauth 回 403 reauth_required
    await fetch(`${base}/__mock/expire-reauth`, { method: 'POST' })
    const unauthed = await send('POST', '/v1/plans', { name: '测试' }, 'create-reauth')
    expect(unauthed.status).toBe(403)
    expect(await unauthed.json()).toMatchObject({ error: { code: 'reauth_required' } })

    // 重新登录
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)

    // 名称空与价格为空 -> 422 键 name, prices
    const emptyFields = await send(
      'POST',
      '/v1/plans',
      { kind: 'subscription', name: '  ', prices: [] },
      'create-bad-fields',
    )
    expect(emptyFields.status).toBe(422)
    expect(await emptyFields.json()).toMatchObject({
      error: { fields: { name: expect.any(String), prices: expect.any(String) } },
    })

    // 绑一个没有在线节点的池且 publish: true -> 422 pool_ids，列表条数不变
    const listBefore = plansSchema.parse(await (await get('/v1/plans')).json()).plans.length
    const noNodeRes = await send(
      'POST',
      '/v1/plans',
      {
        kind: 'subscription',
        name: '标准版',
        prices: [{ billing_interval: 'month', interval_count: 1, unit_amount: 1900 }],
        pool_ids: [empty],
        publish: true,
      },
      'create-no-nodes',
    )
    expect(noNodeRes.status).toBe(422)
    expect(await noNodeRes.json()).toMatchObject({
      error: { fields: { pool_ids: '绑定池中至少需要一个可服务节点' } },
    })
    const listAfterNoNode = plansSchema.parse(await (await get('/v1/plans')).json()).plans.length
    expect(listAfterNoNode).toBe(listBefore)

    // 绑有节点的池 -> 201、status active、code 由名称生成（「标准版」重名得到 standard-2）
    const okReq = {
      kind: 'subscription',
      name: '标准版',
      prices: [
        { billing_interval: 'month', interval_count: 1, unit_amount: 1900 },
        { billing_interval: 'month', interval_count: 3, unit_amount: null }, // 跳过 null 档
      ],
      pool_ids: [live],
      publish: true,
    }
    const createdRes = await send('POST', '/v1/plans', okReq, 'create-ok-1')
    expect(createdRes.status).toBe(201)
    const createdData = planResponseSchema.parse(await createdRes.json()).plan
    expect(createdData.status).toBe('active')
    expect(createdData.code).toBe('standard-2')
    expect(createdData.prices).toHaveLength(1)
    expect(createdData.prices[0]!.unit_amount).toBe(1900)

    // 同键重放回同样结果
    const replayRes = await send('POST', '/v1/plans', okReq, 'create-ok-1')
    expect(replayRes.status).toBe(201)
    const replayData = planResponseSchema.parse(await replayRes.json()).plan
    expect(replayData.id).toBe(createdData.id)
  })

  // 4. 新建一次性套餐
  it('4. creates one_time plan: allow_renewal validation, validity_days', async () => {
    // allow_renewal: true -> 422 allow_renewal
    const badRenewal = await send(
      'POST',
      '/v1/plans',
      {
        kind: 'one_time',
        name: '一次性测试',
        unit_amount: 9900,
        validity_days: 30,
        allow_renewal: true,
      },
      'one-time-bad-renewal',
    )
    expect(badRenewal.status).toBe(422)
    expect(await badRenewal.json()).toMatchObject({
      error: { fields: { allow_renewal: '一次性套餐不能续费' } },
    })

    // 正常建成
    const okRes = await send(
      'POST',
      '/v1/plans',
      {
        kind: 'one_time',
        name: '一次性测试',
        unit_amount: 9900,
        validity_days: 30,
      },
      'one-time-ok',
    )
    expect(okRes.status).toBe(201)
    const plan = planResponseSchema.parse(await okRes.json()).plan
    expect(plan.kind).toBe('one_time')
    expect(plan.prices[0]!.validity_days).toBe(30)
    expect(plan.prices[0]!.unit_amount).toBe(9900)
  })

  // 5. 分块写 basic 与乐观锁
  it('5. basic update bumps row_version, rejects stale row_version with 409 and unknown field with 400', async () => {
    const d = await detailOf(STD)
    const oldVersion = d.row_version

    // 多余字段 -> 400
    const badField = await send(
      'PUT',
      `/v1/plans/${STD}/basic`,
      { expected_row_version: oldVersion, name: '标准版新名', extra_field: 123 },
      'basic-unknown',
    )
    expect(badField.status).toBe(400)

    // 成功改名 -> row_version + 1
    const ok = await send(
      'PUT',
      `/v1/plans/${STD}/basic`,
      { expected_row_version: oldVersion, name: '标准版新名', description: '新描述' },
      'basic-ok',
    )
    expect(ok.status).toBe(200)
    const updated = planResponseSchema.parse(await ok.json()).plan
    expect(updated.name).toBe('标准版新名')
    expect(updated.row_version).toBe(oldVersion + 1)

    // 用旧 row_version 再写 -> 409 且 fields.row_version 以 current= 开头
    const conflict = await send(
      'PUT',
      `/v1/plans/${STD}/basic`,
      { expected_row_version: oldVersion, name: '标准版再改' },
      'basic-conflict',
    )
    expect(conflict.status).toBe(409)
    const conflictBody = await conflict.json()
    expect(conflictBody).toMatchObject({
      error: {
        code: 'conflict',
        fields: { row_version: expect.stringMatching(/^current=\d+/) },
      },
    })
  })

  // 6. prices 改价
  it('6. prices update archives tier, detects no-op, prevents deleting last tier in active plan', async () => {
    let d = await detailOf(STD)
    const initialVersion = d.row_version

    // 标准版把 monthx3 置 null -> 该价归档、row_version + 1
    const archiveMonth3 = await send(
      'PUT',
      `/v1/plans/${STD}/prices`,
      {
        expected_row_version: initialVersion,
        prices: [{ billing_interval: 'month', interval_count: 3, unit_amount: null }],
      },
      'prices-archive-3',
    )
    expect(archiveMonth3.status).toBe(200)
    d = planResponseSchema.parse(await archiveMonth3.json()).plan
    expect(d.row_version).toBe(initialVersion + 1)
    const m3Price = d.prices.find((p) => p.billing_interval === 'month' && p.interval_count === 3)!
    expect(m3Price.status).toBe('archived')

    // 同样的请求换新键再发（无变化）row_version 不变
    const noopRes = await send(
      'PUT',
      `/v1/plans/${STD}/prices`,
      {
        expected_row_version: d.row_version,
        prices: [{ billing_interval: 'month', interval_count: 3, unit_amount: null }],
      },
      'prices-noop',
    )
    expect(noopRes.status).toBe(200)
    const dNoop = planResponseSchema.parse(await noopRes.json()).plan
    expect(dNoop.row_version).toBe(d.row_version)

    // 家庭版把唯一的 monthx1 置 null -> 422 prices: 在售套餐至少保留一档价格
    const familyDetail = await detailOf(FAMILY)
    const familyFail = await send(
      'PUT',
      `/v1/plans/${FAMILY}/prices`,
      {
        expected_row_version: familyDetail.row_version,
        prices: [{ billing_interval: 'month', interval_count: 1, unit_amount: null }],
      },
      'family-prices-fail',
    )
    expect(familyFail.status).toBe(422)
    expect(await familyFail.json()).toMatchObject({
      error: { fields: { prices: '在售套餐至少保留一档价格' } },
    })
  })

  // 7. usage 与 versions 发布
  it('7. usage creates draft version, publish activates it', async () => {
    let d = await detailOf(STD)
    expect(d.draft_version_id).toBeNull()

    // 标准版写用量 -> versions 多一个草稿、draft_version_id 非空
    const usageRes = await send(
      'PUT',
      `/v1/plans/${STD}/usage`,
      {
        expected_row_version: d.row_version,
        traffic_bytes: 350 * 1024 ** 3,
        max_devices: 4,
        throttle_kbps: 100_000,
      },
      'usage-update-1',
    )
    expect(usageRes.status).toBe(200)
    d = planResponseSchema.parse(await usageRes.json()).plan
    expect(d.draft_version_id).not.toBeNull()
    const draftId = d.draft_version_id!
    const draftVer = d.versions.find((v) => v.id === draftId)!
    expect(draftVer.status).toBe('draft')
    expect(draftVer.traffic_bytes).toBe(350 * 1024 ** 3)

    // 发布它 -> 版本变当前、version 等于新版本号、draft_version_id 为 null
    const pubRes = await send(
      'POST',
      `/v1/plans/${STD}/versions/${draftId}/publish`,
      { expected_row_version: d.row_version },
      'pub-draft-1',
    )
    expect(pubRes.status).toBe(200)
    d = planResponseSchema.parse(await pubRes.json()).plan
    expect(d.versions.find((v) => v.id === draftId)!.status).toBe('published')
    expect(d.draft_version_id).toBeNull()
    expect(d.version).toBe(draftVer.version)
  })

  it('writes audit rows like Go when a version is published', async () => {
    let d = await detailOf(FAMILY)
    const usageRes = await send(
      'PUT',
      `/v1/plans/${FAMILY}/usage`,
      {
        expected_row_version: d.row_version,
        traffic_bytes: 280 * 1024 ** 3,
        max_devices: 3,
        throttle_kbps: 80_000,
      },
      'usage-audit-family',
    )
    expect(usageRes.status).toBe(200)
    d = planResponseSchema.parse(await usageRes.json()).plan
    const draftId = d.draft_version_id!

    const pubRes = await send(
      'POST',
      `/v1/plans/${FAMILY}/versions/${draftId}/publish`,
      { expected_row_version: d.row_version },
      'pub-audit-family',
    )
    expect(pubRes.status).toBe(200)

    const publishAudit = auditResponse.parse(
      await (await get('/v1/audit?action=plan_version.publish&limit=200')).json(),
    )
    expect(publishAudit.events[0]).toMatchObject({
      action: 'plan_version.publish',
      resource_type: 'plan_version',
      resource_id: draftId,
      actor_kind: 'admin',
      actor_email: MOCK_ACCOUNTS.admin.email,
      outcome: 'success',
    })

    const usageAudit = auditResponse.parse(
      await (await get('/v1/audit?action=plan.usage_update&limit=200')).json(),
    )
    expect(usageAudit.events[0]).toMatchObject({ resource_id: FAMILY })
  })

  // 8. reset
  it('8. reset update handles enabled toggle and positive validation', async () => {
    let d = await detailOf(STD)

    // enabled: false -> 详情 reset 为 null
    const disableReset = await send(
      'PUT',
      `/v1/plans/${STD}/reset`,
      { expected_row_version: d.row_version, enabled: false, price: 0, limit: null },
      'reset-disable',
    )
    expect(disableReset.status).toBe(200)
    d = planResponseSchema.parse(await disableReset.json()).plan
    expect(d.reset).toBeNull()

    // enabled: true, price: 0 -> 422 reset.price
    const badReset = await send(
      'PUT',
      `/v1/plans/${STD}/reset`,
      { expected_row_version: d.row_version, enabled: true, price: 0, limit: 10 },
      'reset-bad',
    )
    expect(badReset.status).toBe(422)
    expect(await badReset.json()).toMatchObject({
      error: { fields: { 'reset.price': '必须大于 0' } },
    })
  })

  // 9. pools
  it('9. pools update updates GET pools bound state', async () => {
    const d = await detailOf(STD)
    const { live } = await poolIds()

    // 绑定单一池
    const setPools = await send(
      'PUT',
      `/v1/plans/${STD}/pools`,
      { expected_row_version: d.row_version, pool_ids: [live] },
      'set-pools-1',
    )
    expect(setPools.status).toBe(200)

    const poolsRes = planPoolsSchema.parse(await (await get(`/v1/plans/${STD}/pools`)).json())
    const targetPool = poolsRes.pools.find((p) => p.id === live)!
    expect(targetPool.bound).toBe(true)
  })

  // 10. archive
  it('10. archiving plan locks it from future edits with 409', async () => {
    let d = await detailOf(STD)
    const archRes = await send(
      'POST',
      `/v1/plans/${STD}/archive`,
      { expected_row_version: d.row_version },
      'arch-std',
    )
    expect(archRes.status).toBe(200)
    d = planResponseSchema.parse(await archRes.json()).plan
    expect(d.status).toBe('archived')

    // 归档后任何写都 409 已归档套餐不能编辑
    const tryEdit = await send(
      'PUT',
      `/v1/plans/${STD}/basic`,
      { expected_row_version: d.row_version, name: '改名已归档' },
      'edit-archived',
    )
    expect(tryEdit.status).toBe(409)
    expect(await tryEdit.json()).toMatchObject({
      error: { message: '已归档套餐不能编辑' },
    })
  })
})
