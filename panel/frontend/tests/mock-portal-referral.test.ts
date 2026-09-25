/**
 * [INPUT]: 依赖 vitest，依赖 ./mock-helpers 的 serve / close / loginAs / bearer / mockFetch，依赖 ../dev/mock-api 的 MOCK_ACCOUNTS
 * [OUTPUT]: 对外提供门户邀请返利假接口的测试
 * [POS]: tests 的门户邀请返利假后端守卫（R114）：佣金概况带 summary.scope（外框 queries.ts 带 tsx 依赖进不了 node 侧类型检查，按字段断言），默认场景 every_order、multi 场景 first_order，legacy 场景也照回
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · portal referral', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('portal'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.portal)).access_token)
  })
  afterAll(() => close(server))

  const scopeIn = async (name: string) => {
    expect((await mockFetch(base, auth, 'POST', '/v1/__mock/portal-scenario', { name })).status).toBeLessThan(300)
    return ((await (await mockFetch(base, auth, 'GET', '/v1/me/commission')).json()) as { summary: { scope: string } }).summary.scope
  }

  it('R114: commission summary carries the scope in every scenario', async () => {
    expect(await scopeIn('multi')).toBe('first_order')
    expect(await scopeIn('legacy')).toBe('every_order')
    expect(await scopeIn('default')).toBe('every_order')
  })
})
