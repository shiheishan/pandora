/**
 * [INPUT]: 依赖 vitest，依赖 ./mock-helpers，依赖 ../dev/mock-api 的 MOCK_ACCOUNTS
 * [OUTPUT]: 对外提供营销（后台-06）假接口的测试
 * [POS]: tests 的营销假后端守卫：礼品卡掩码、一次性导出（非 JSON 重放不带 Content-Disposition）、券与套餐卡指向套餐模块的固定套餐 id、未知字段 400
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · admin marketing', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
  })
  afterAll(() => close(server))

  const post = (path: string, body: unknown, key?: string) => mockFetch(base, auth, 'POST', path, body, key)

  it('never returns plaintext gift codes outside the generate sample and the one-time export', async () => {
    const { templates } = (await (await fetch(`${base}/v1/gift-cards`, { headers: auth })).json()) as { templates: Array<{ id: string; type: string }> }
    const general = templates.find((t) => t.type === 'general')!
    const made = (await (await post(`/v1/gift-cards/${general.id}/codes`, { count: 6, prefix: 'T' }, 'gen-1')).json()) as { batch_id: string; sample: string[]; batch: { exported_at: string | null } }
    expect(made.sample).toHaveLength(4)
    expect(made.batch.exported_at).toBeNull()
    const list = (await (await fetch(`${base}/v1/gift-cards/codes?batch_id=${made.batch_id}`, { headers: auth })).json()) as { codes: Array<Record<string, unknown>>; total: number }
    expect(list.total).toBe(6)
    expect(list.codes.every((c) => !('code' in c) && /^T[A-Z0-9]{4}•{8}$/.test(String(c.code_masked)))).toBe(true)

    const first = await post(`/v1/gift-cards/batches/${made.batch_id}/export`, {}, 'exp-1')
    expect(first.headers.get('content-disposition')).toBe(`attachment; filename="gift-codes-${made.batch_id.slice(0, 8)}.csv"`)
    const csv = await first.text()
    expect(csv.split('\n').filter(Boolean)).toHaveLength(7)
    expect(csv).toContain(made.sample[0]!)
    // 同键重放：同一份 CSV，但与后端一致不带 Content-Disposition
    const replay = await post(`/v1/gift-cards/batches/${made.batch_id}/export`, {}, 'exp-1')
    expect(replay.headers.get('content-disposition')).toBeNull()
    expect(await replay.text()).toBe(csv)
    const again = await post(`/v1/gift-cards/batches/${made.batch_id}/export`, {}, 'exp-2')
    expect(again.status).toBe(409)
    expect(await again.json()).toMatchObject({ error: { code: 'conflict', message: '该批次已导出，完整卡码不可再次获取' } })
  })

  it('points coupons and plan cards at the plans module catalog (fixed plan ids)', async () => {
    const { plans } = (await (await fetch(`${base}/v1/plans`, { headers: auth })).json()) as { plans: Array<{ id: string }> }
    const ids = new Set(plans.map((p) => p.id))
    const { coupons } = (await (await fetch(`${base}/v1/coupons?limit=200`, { headers: auth })).json()) as { coupons: Array<{ applicable_plan_ids: string[] }> }
    const scoped = coupons.flatMap((c) => c.applicable_plan_ids)
    expect(scoped.length).toBeGreaterThan(0)
    expect(scoped.every((id) => ids.has(id) && /^9c0e1a2b-3333-4b00-8000-00000000000[1-4]$/.test(id))).toBe(true)
    const { templates } = (await (await fetch(`${base}/v1/gift-cards`, { headers: auth })).json()) as { templates: Array<{ type: string; rewards: { plan_id?: string; price_id?: string } }> }
    const card = templates.find((t) => t.type === 'plan')!
    const { plan: detail } = (await (await fetch(`${base}/v1/plans/${card.rewards.plan_id}`, { headers: auth })).json()) as { plan: { prices: Array<{ id: string }> } }
    expect(detail.prices.map((p) => p.id)).toContain(card.rewards.price_id)
  })

  it('rejects unknown fields like the Go decoder and keeps field-level 422s', async () => {
    const extra = await post('/v1/coupons', { code: 'X1', discount_type: 'percent', discount_value: 100, batch: true })
    expect(extra.status).toBe(400)
    const dup = await post('/v1/coupons', { code: 'autumn26', discount_type: 'percent', discount_value: 100 })
    expect(dup.status).toBe(409)
    const bad = await post('/v1/commission/config', { rate_percent: 51 })
    expect(await bad.json()).toMatchObject({ error: { code: 'validation_failed', fields: { rate_percent: '佣金比例需在 0 到 50 之间' } } })
  })
})
