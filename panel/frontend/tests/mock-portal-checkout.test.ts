/**
 * [INPUT]: 依赖 vitest，依赖 ./mock-helpers 的 serve / close / loginAs / bearer / mockFetch，依赖 ../dev/mock-api 的 MOCK_ACCOUNTS
 * [OUTPUT]: 对外提供门户结账（变更套餐试算）假接口的测试
 * [POS]: tests 的门户结账假后端守卫（R114）：变更套餐试算回 coupon——没用码为 null，用了码是与优惠码试算同形的券面
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

interface Sub { id: string; plan_id: string; status: string }
interface Plan { id: string; allow_upgrade: boolean; prices: Array<{ id: string; currency: string }> }

describe('mock api · portal checkout', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('portal'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.portal)).access_token)
  })
  afterAll(() => close(server))

  it('R114: change-plan preview carries the coupon face, null without a code', async () => {
    const subs = ((await (await mockFetch(base, auth, 'GET', '/v1/me/subscriptions')).json()) as { subscriptions: Sub[] }).subscriptions
    const sub = subs.find((s) => s.status === 'active')!
    const plans = ((await (await mockFetch(base, auth, 'GET', '/v1/plans')).json()) as { plans: Plan[] }).plans
    const plan = plans.find((p) => p.id !== sub.plan_id && p.allow_upgrade && p.prices.some((x) => x.currency === 'CNY'))!
    const price = plan.prices.find((x) => x.currency === 'CNY')!
    const preview = async (extra: Record<string, string>) => {
      const res = await mockFetch(base, auth, 'POST', `/v1/me/subscriptions/${sub.id}/change-plan/preview`, { plan_id: plan.id, price_id: price.id, ...extra })
      expect(res.status).toBe(200)
      return (await res.json()) as { coupon: unknown; discount: number }
    }
    expect((await preview({})).coupon).toBeNull()
    const used = await preview({ coupon_code: 'welcome' })
    expect(used.coupon).toEqual({ code: 'WELCOME', discount_type: 'percent', discount_value: 1000 })
    expect(used.discount).toBeGreaterThan(0)
  })
})
