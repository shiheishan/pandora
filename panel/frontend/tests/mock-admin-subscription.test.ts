import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import {
  subscriptionExtrasSchema,
  TEMPLATE_FORMATS,
  templateDetailSchema,
  templateListSchema,
  templatePreviewSchema,
} from '../src/admin/screens/nodes/subscription'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · admin subscription', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
  })
  afterAll(() => close(server))

  const call = (method: string, path: string, body?: unknown) => mockFetch(base, auth, method, path, body)
  const json = async (res: Response) => (await res.json()) as Record<string, unknown>

  it('reads subscription extras defaults and saves them; rejects link_base_urls as unknown field', async () => {
    const initial = subscriptionExtrasSchema.parse(await json(await call('GET', '/v1/settings/subscription')))
    expect(initial).toEqual({ info_nodes: false, protocol_prefix: false })

    const saved = subscriptionExtrasSchema.parse(
      await json(await call('POST', '/v1/settings/subscription', { info_nodes: true, protocol_prefix: true })),
    )
    expect(saved.info_nodes).toBe(true)
    expect(saved.protocol_prefix).toBe(true)
  })

  it('rejects link_base_urls with 400 bad_request', async () => {
    expect(await json(await call('POST', '/v1/settings/subscription', { link_base_urls: ['https://sub.example.com'] }))).toMatchObject({
      error: { code: 'bad_request' },
    })
  })

  it('lists six template formats and serves detail, save, reset, preview, and 404', async () => {
    const list = templateListSchema.parse(await json(await call('GET', '/v1/subscription-templates'))).templates
    expect(list.map((t) => t.format)).toEqual([...TEMPLATE_FORMATS])
    expect(list.every((t) => !t.customized && t.bytes === 0 && t.updated_at === null)).toBe(true)

    const detail = templateDetailSchema.parse(await json(await call('GET', '/v1/subscription-templates/clash')))
    expect(detail.customized).toBe(false)
    expect(detail.content).toBe(detail.default)
    expect(detail.max_bytes).toBe(262144)

    expect((await call('GET', '/v1/subscription-templates/nope')).status).toBe(404)

    const badSave = await json(await call('PUT', '/v1/subscription-templates/clash', { content: 'mode: rule\n' }))
    expect(badSave).toMatchObject({ error: { fields: { content: '缺少占位 {{proxies}}' } } })

    const good = detail.default
    const put = await call('PUT', '/v1/subscription-templates/clash', { content: good })
    expect(put.status).toBe(200)
    const afterSave = templateDetailSchema.parse(await json(put))
    expect(afterSave.customized).toBe(true)
    const listed = templateListSchema.parse(await json(await call('GET', '/v1/subscription-templates'))).templates.find((t) => t.format === 'clash')!
    expect(listed.customized).toBe(true)
    expect(listed.bytes).toBe(new TextEncoder().encode(good).length)
    expect(listed.updated_at).not.toBeNull()

    const preview = templatePreviewSchema.parse(
      await json(await call('POST', '/v1/subscription-templates/clash/preview', { content: good })),
    )
    expect(preview.node_count).toBe(4)
    expect(preview.content_type).toBe('text/yaml; charset=utf-8')
    expect(preview.body).not.toContain('{{proxies}}')
    expect(preview.body).toContain('示例 · 香港 01')

    expect((await call('DELETE', '/v1/subscription-templates/clash')).status).toBe(200)
    const reset = templateListSchema.parse(await json(await call('GET', '/v1/subscription-templates'))).templates.find((t) => t.format === 'clash')!
    expect(reset.customized).toBe(false)
    expect(reset.bytes).toBe(0)
  })

  it('returns reauth_required on PUT template after expire-reauth', async () => {
    const detail = templateDetailSchema.parse(await json(await call('GET', '/v1/subscription-templates/clash')))
    await fetch(`${base}/__mock/expire-reauth`, { method: 'POST' })
    const res = await call('PUT', '/v1/subscription-templates/clash', { content: detail.default })
    expect(res.status).toBe(403)
    expect(await json(res)).toMatchObject({ error: { code: 'reauth_required' } })
  })
})
