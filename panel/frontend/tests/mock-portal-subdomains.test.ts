import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { lastProbeReport } from '../dev/mock/portal/subdomains'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · portal subdomains', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  let userId: string

  const call = (method: string, path: string, body?: unknown) =>
    mockFetch(base, auth, method, path, body)
  const setScenario = async (name: string) => {
    const res = await call('POST', '/v1/__mock/portal-scenario', { name })
    expect(res.status).toBe(200)
  }

  beforeAll(async () => {
    ;({ server, base } = await serve('portal'))
    const login = await loginAs(base, MOCK_ACCOUNTS.portal)
    auth = bearer(login.access_token)
    userId = login.user_id
  })

  afterAll(async () => {
    await setScenario('default')
    await close(server)
  })

  it('默认场景 GET /v1/me/subscription-domains 返回 []', async () => {
    const res = await call('GET', '/v1/me/subscription-domains')
    expect(res.status).toBe(200)
    const data = (await res.json()) as { domains: string[] }
    expect(data).toEqual({ domains: [] })
  })

  it('切到 multi 场景后 GET 返回两个域名，测试结束后可切回 default', async () => {
    await setScenario('multi')
    const resMulti = await call('GET', '/v1/me/subscription-domains')
    expect(resMulti.status).toBe(200)
    const dataMulti = (await resMulti.json()) as { domains: string[] }
    expect(dataMulti).toEqual({
      domains: ['https://sub-a.example.test', 'https://sub-b.example.test'],
    })

    await setScenario('default')
    const resDefault = await call('GET', '/v1/me/subscription-domains')
    expect(resDefault.status).toBe(200)
    const dataDefault = (await resDefault.json()) as { domains: string[] }
    expect(dataDefault).toEqual({ domains: [] })
  })

  it('POST /v1/me/subscription-domains/probe 合法数据回 204 并记录', async () => {
    const payload = {
      results: [
        { base_url: 'https://sub-a.example.test', ok: true },
        { base_url: 'https://sub-b.example.test', ok: false },
      ],
    }
    const res = await call('POST', '/v1/me/subscription-domains/probe', payload)
    expect(res.status).toBe(204)
    expect(lastProbeReport(userId)).toEqual(payload.results)
  })

  it('超过 10 条（11 条）报 422 校验失败与对应文案', async () => {
    const results = Array.from({ length: 11 }, (_, i) => ({
      base_url: `https://sub-${i}.example.test`,
      ok: true,
    }))
    const res = await call('POST', '/v1/me/subscription-domains/probe', { results })
    expect(res.status).toBe(422)
    const json = (await res.json()) as { error: { code: string; message: string; fields?: Record<string, string> } }
    expect(json.error.code).toBe('validation_failed')
    expect(json.error.message).toBe('请求参数校验未通过')
    expect(json.error.fields?.results).toBe('一次最多报告 10 个域名')
  })

  it('多余字段或格式不合规报 400 bad_request', async () => {
    // 顶层多余字段（readStrict 检查）
    const resExtraTop = await call('POST', '/v1/me/subscription-domains/probe', {
      results: [],
      extra_field: 123,
    })
    expect(resExtraTop.status).toBe(400)

    // results 不是数组
    const resNotArray = await call('POST', '/v1/me/subscription-domains/probe', {
      results: 'invalid',
    })
    expect(resNotArray.status).toBe(400)
    const jsonNotArray = (await resNotArray.json()) as { error: { code: string; message: string } }
    expect(jsonNotArray.error.code).toBe('bad_request')
    expect(jsonNotArray.error.message).toBe('请求体不是合法的 JSON')

    // 项有多余字段
    const resExtraItemField = await call('POST', '/v1/me/subscription-domains/probe', {
      results: [{ base_url: 'https://sub-a.example.test', ok: true, extra: 1 }],
    })
    expect(resExtraItemField.status).toBe(400)
    const jsonExtraItem = (await resExtraItemField.json()) as { error: { code: string; message: string } }
    expect(jsonExtraItem.error.code).toBe('bad_request')
    expect(jsonExtraItem.error.message).toBe('请求体不是合法的 JSON')

    // 项字段类型错误
    const resBadType = await call('POST', '/v1/me/subscription-domains/probe', {
      results: [{ base_url: 123, ok: true }],
    })
    expect(resBadType.status).toBe(400)
  })
})
