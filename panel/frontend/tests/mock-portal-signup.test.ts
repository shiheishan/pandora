import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { close, serve } from './mock-helpers'

describe('mock api · portal signup & mail-link', () => {
  let server: Server
  let base: string
  beforeAll(async () => {
    ;({ server, base } = await serve('portal'))
  })
  afterAll(() => close(server))

  const post = (path: string, body: unknown) => fetch(`${base}${path}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })

  it('GET /v1/signup-config returns the expected shape', async () => {
    const out = (await (await fetch(`${base}/v1/signup-config`)).json()) as {
      captcha: null
      email_suffixes: string[]
      mail_link_login: boolean
    }
    expect(out).toEqual({ captcha: null, email_suffixes: [], mail_link_login: true })
  })

  it('register/start accepts captcha_token string and rejects non-string', async () => {
    const ok = await post('/v1/auth/register/start', { email: 'newuser@example.test', invite_code: 'PANDORA', captcha_token: 'x' })
    expect(ok.status).toBe(200)
    const bad = await post('/v1/auth/register/start', { email: 'other@example.test', invite_code: 'PANDORA', captcha_token: 123 })
    expect(bad.status).toBe(422)
    expect(await bad.json()).toMatchObject({ error: { fields: { captcha_token: expect.any(String) } } })
  })

  it('mail-link start, login, and one-time token', async () => {
    const email = MOCK_ACCOUNTS.portal.email
    const start = await post('/v1/auth/mail-link/start', { email })
    expect(await start.json()).toEqual({ ok: true, message: '如果这个邮箱已注册，登录链接已发出，15 分钟内有效' })
    const mock = (await (await fetch(`${base}/__mock/mail-link`)).json()) as { token: string | null }
    expect(mock.token).toBeTruthy()
    const login = await post('/v1/auth/mail-link/login', { token: mock.token })
    expect(login.status).toBe(200)
    const tokens = (await login.json()) as { access_token: string }
    expect(tokens.access_token.length).toBeGreaterThan(0)
    const again = await post('/v1/auth/mail-link/login', { token: mock.token })
    expect(again.status).toBe(401)
  })

  it('mail-link start responds the same for unknown email and rate-limits after three sends', async () => {
    const unknown = (await (await post('/v1/auth/mail-link/start', { email: 'nobody-mail@example.test' })).json()) as {
      ok: boolean
      message: string
    }
    expect(unknown).toEqual({ ok: true, message: '如果这个邮箱已注册，登录链接已发出，15 分钟内有效' })

    const rateEmail = 'ratelimit-mail@example.test'
    for (let i = 0; i < 3; i++) {
      expect((await post('/v1/auth/mail-link/start', { email: rateEmail })).status).toBe(200)
    }
    const fourth = await post('/v1/auth/mail-link/start', { email: rateEmail })
    expect(fourth.status).toBe(429)
    expect(await fourth.json()).toMatchObject({ error: { code: 'rate_limited', message: '请求过于频繁，请稍后再试' } })
  })

  it('mail-link start accepts captcha_token string and rejects non-string', async () => {
    const ok = await post('/v1/auth/mail-link/start', { email: 'captcha-mail@example.test', captcha_token: 'x' })
    expect(ok.status).toBe(200)
    const bad = await post('/v1/auth/mail-link/start', { email: 'captcha-mail2@example.test', captcha_token: 123 })
    expect(bad.status).toBe(422)
    expect(await bad.json()).toMatchObject({ error: { fields: { captcha_token: expect.any(String) } } })
  })

  it('POST /v1/invite/visit returns ok', async () => {
    expect(await (await post('/v1/invite/visit', { code: 'INVITE1' })).json()).toEqual({ ok: true })
  })
})
