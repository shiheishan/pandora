import type { Server } from 'node:http'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS, MOCK_DISABLED_ACCOUNT } from '../dev/mock-api'
import { close, serve } from './mock-helpers'

describe('mock api · portal signup & mail-link', () => {
  let server: Server
  let base: string
  beforeAll(async () => {
    ;({ server, base } = await serve('portal'))
  })
  afterAll(() => close(server))

  const post = (path: string, body: unknown) => fetch(`${base}${path}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  const setSignup = (body: Record<string, unknown>) => post('/__mock/portal-signup', body)

  afterEach(async () => {
    await setSignup({ reset: true })
  })

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

  it('site-config defaults match Go (open, no verification)', async () => {
    const site = await (await fetch(`${base}/v1/site-config`)).json()
    expect(site).toEqual({ registration_mode: 'open', email_verification: false, password_reset: true })
  })

  it('open registration without verification: start then complete with empty code', async () => {
    const start = await post('/v1/auth/register/start', { email: 'fresh1@example.test' })
    expect(start.status).toBe(200)
    const startBody = (await start.json()) as {
      registration_token: string
      verification_required: boolean
      message: string
      dev_code?: string
    }
    expect(startBody.verification_required).toBe(false)
    expect(startBody.message).toBe('若该邮箱可用于注册，验证码已发送')
    expect(startBody).not.toHaveProperty('dev_code')
    const complete = await post('/v1/auth/register/complete', {
      registration_token: startBody.registration_token,
      code: '',
      password: 'abc12345',
    })
    expect(complete.status).toBe(201)
    const start2 = await post('/v1/auth/register/start', { email: 'fresh1@example.test' })
    expect(start2.status).toBe(200)
    const start2Body = (await start2.json()) as { registration_token: string }
    const complete2 = await post('/v1/auth/register/complete', {
      registration_token: start2Body.registration_token,
      code: '',
      password: 'abc12345',
    })
    expect(complete2.status).toBe(403)
  })

  it('open registration rejects an invalid invite code', async () => {
    const start = await post('/v1/auth/register/start', { email: 'fresh2@example.test', invite_code: 'NOPE' })
    expect(start.status).toBe(403)
  })

  it('invite_only requires PANDORA and verification sends dev_code', async () => {
    expect((await setSignup({ registration_mode: 'invite_only', email_verification: true })).status).toBe(200)
    expect((await post('/v1/auth/register/start', { email: 'invite-only@example.test' })).status).toBe(403)
    const start = await post('/v1/auth/register/start', { email: 'invite-only@example.test', invite_code: 'PANDORA' })
    expect(start.status).toBe(200)
    const startBody = (await start.json()) as { verification_required: boolean; dev_code: string; registration_token: string }
    expect(startBody.verification_required).toBe(true)
    expect(startBody.dev_code).toBe('123456')
    const badComplete = await post('/v1/auth/register/complete', {
      registration_token: startBody.registration_token,
      code: '000000',
      password: 'abc12345',
    })
    expect(badComplete.status).toBe(403)
    const okComplete = await post('/v1/auth/register/complete', {
      registration_token: startBody.registration_token,
      code: '123456',
      password: 'abc12345',
    })
    expect(okComplete.status).toBe(201)
  })

  it('paused registration reads as closed', async () => {
    await setSignup({ registration_paused: true })
    const site = (await (await fetch(`${base}/v1/site-config`)).json()) as { registration_mode: string }
    expect(site.registration_mode).toBe('closed')
    const start = await post('/v1/auth/register/start', { email: 'paused@example.test' })
    expect(start.status).toBe(403)
    expect(await start.json()).toMatchObject({ error: { message: '注册当前不可用或邀请码无效' } })
  })

  it('mail paused: verification signup, mail link, site-config and signup-config', async () => {
    await setSignup({ mail_paused: true, email_verification: true })
    const regStart = await post('/v1/auth/register/start', { email: 'mail-paused@example.test', invite_code: 'PANDORA' })
    expect(regStart.status).toBe(503)
    expect(await regStart.json()).toMatchObject({
      error: { code: 'service_unavailable', message: '邮件发送暂停，暂时无法注册，请稍后再试' },
    })
    const mailStart = await post('/v1/auth/mail-link/start', { email: MOCK_ACCOUNTS.portal.email })
    expect(mailStart.status).toBe(503)
    expect(await mailStart.json()).toMatchObject({
      error: { message: '邮件发送暂停，暂时无法用邮件登录，请稍后再试' },
    })
    const site = (await (await fetch(`${base}/v1/site-config`)).json()) as { password_reset: boolean }
    expect(site.password_reset).toBe(false)
    const signupCfg = (await (await fetch(`${base}/v1/signup-config`)).json()) as { mail_link_login: boolean }
    expect(signupCfg.mail_link_login).toBe(false)
    await setSignup({ email_verification: false })
    const regStart2 = await post('/v1/auth/register/start', { email: 'fresh3@example.test' })
    expect(regStart2.status).toBe(200)
  })

  it('mail link login switched off returns 403', async () => {
    await setSignup({ mail_link_login: false })
    const mailStart = await post('/v1/auth/mail-link/start', { email: 'mail-off@example.test' })
    expect(mailStart.status).toBe(403)
    expect(await mailStart.json()).toMatchObject({ error: { message: '邮件链接登录未开启' } })
    const signupCfg = (await (await fetch(`${base}/v1/signup-config`)).json()) as { mail_link_login: boolean }
    expect(signupCfg.mail_link_login).toBe(false)
  })

  it('disabled account: right password 403, wrong password 401, no token', async () => {
    const forbidden = await post('/v1/auth/login', MOCK_DISABLED_ACCOUNT)
    expect(forbidden.status).toBe(403)
    const forbiddenBody = (await forbidden.json()) as { access_token?: string; error: { code: string; message: string } }
    expect(forbiddenBody).toMatchObject({ error: { code: 'forbidden', message: '账号已停用' } })
    expect(forbiddenBody.access_token).toBeUndefined()
    const wrong = await post('/v1/auth/login', { email: MOCK_DISABLED_ACCOUNT.email, password: 'wrong-pass-1' })
    expect(wrong.status).toBe(401)
    expect(await wrong.json()).toMatchObject({ error: { message: '邮箱或密码不正确' } })
    const nobody = await post('/v1/auth/login', { email: 'nobody-login@example.test', password: MOCK_DISABLED_ACCOUNT.password })
    expect(nobody.status).toBe(401)
    expect(await nobody.json()).toMatchObject({ error: { message: '邮箱或密码不正确' } })
  })

  it('portal-signup rejects unknown keys and bad types without changing anything', async () => {
    expect((await setSignup({ registration_mode: 'sometimes' })).status).toBe(422)
    expect((await setSignup({ nope: true })).status).toBe(422)
    expect((await setSignup({ mail_paused: 'yes' })).status).toBe(422)
    const site = await (await fetch(`${base}/v1/site-config`)).json()
    expect(site).toEqual({ registration_mode: 'open', email_verification: false, password_reset: true })
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
