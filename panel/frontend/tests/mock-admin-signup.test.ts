import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { signupSettingsSchema } from '../src/admin/screens/users/signupSettings'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · admin signup settings', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>

  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
  })
  afterAll(() => close(server))

  const call = (method: string, path: string, body?: unknown, key?: string) => mockFetch(base, auth, method, path, body, key)
  const json = async (res: Response) => (await res.json()) as Record<string, unknown>

  it('GET 缺省值', async () => {
    const res = await call('GET', '/v1/settings/signup')
    expect(res.status).toBe(200)
    const data = signupSettingsSchema.parse(await json(res))
    expect(data).toMatchObject({
      captcha_provider: '',
      captcha_site_key: '',
      has_captcha_secret: false,
      recaptcha_v3_threshold: 0.5,
      email_suffix_whitelist_enabled: false,
      gmail_alias_block: false,
      ip_limit_enabled: true,
      ip_limit_count: 10,
      ip_limit_window_minutes: 1,
      trial_plan_id: null,
      trial_hours: 0,
      invite_max_codes: 5,
      invite_never_expire: true,
      mail_link_login: false,
      mail_configured: true,
    })
    expect(data.email_suffixes).toContain('gmail.com')
  })

  it('POST 改 invite_max_codes 后 GET 能读到', async () => {
    const postRes = await call('POST', '/v1/settings/signup', { invite_max_codes: 12 })
    expect(postRes.status).toBe(200)
    const postData = signupSettingsSchema.parse(await json(postRes))
    expect(postData.invite_max_codes).toBe(12)

    const getRes = await call('GET', '/v1/settings/signup')
    expect(getRes.status).toBe(200)
    const getData = signupSettingsSchema.parse(await json(getRes))
    expect(getData.invite_max_codes).toBe(12)
  })

  it('ip_limit_count: 0 回 422 且 fields.ip_limit_count', async () => {
    const res = await call('POST', '/v1/settings/signup', { ip_limit_count: 0 })
    expect(res.status).toBe(422)
    const body = await json(res)
    expect(body).toMatchObject({
      error: {
        code: 'validation_failed',
        fields: {
          ip_limit_count: '注册 IP 限额需在 1 到 1000 之间',
        },
      },
    })
  })

  it('开 captcha_provider: turnstile 不给 site key / secret 回 422', async () => {
    const res = await call('POST', '/v1/settings/signup', { captcha_provider: 'turnstile' })
    expect(res.status).toBe(422)
    const body = await json(res)
    expect(body).toMatchObject({
      error: {
        code: 'validation_failed',
        fields: {
          captcha_site_key: expect.any(String),
          captcha_secret: expect.any(String),
        },
      },
    })
  })

  it('给齐 captcha_site_key 和 secret 后成功且 has_captcha_secret 为 true、响应里没有 captcha_secret 键', async () => {
    const res = await call('POST', '/v1/settings/signup', {
      captcha_provider: 'turnstile',
      captcha_site_key: 'test-site-key',
      captcha_secret: 'test-secret',
    })
    expect(res.status).toBe(200)
    const raw = await json(res)
    expect(raw).not.toHaveProperty('captcha_secret')
    const data = signupSettingsSchema.parse(raw)
    expect(data.captcha_provider).toBe('turnstile')
    expect(data.captcha_site_key).toBe('test-site-key')
    expect(data.has_captcha_secret).toBe(true)
  })

  it('未知字段 400', async () => {
    const res = await call('POST', '/v1/settings/signup', { unknown_field: 'bad' })
    expect(res.status).toBe(400)
    const body = await json(res)
    expect(body).toMatchObject({
      error: {
        code: 'bad_request',
        message: expect.stringContaining('未知字段'),
      },
    })
  })
})
