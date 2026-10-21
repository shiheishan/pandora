import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { close, loginAs, serve } from './mock-helpers'

// 找回密码（identity.StartPasswordReset / CompletePasswordReset）：第 1 步对存在与不存在的邮箱
// 回同一句话；验证码错到上限后正确的也不认；成功后旧密码失效、旧会话全部退出
describe('mock api · portal password reset', () => {
  let server: Server
  let base: string
  beforeAll(async () => {
    ;({ server, base } = await serve('portal'))
  })
  afterAll(() => close(server))

  const post = (path: string, body: unknown, headers: Record<string, string> = {}) => fetch(`${base}${path}`, { method: 'POST', headers, body: JSON.stringify(body) })

  it('advertises the capability and keeps both answers alike', async () => {
    // 字段形状由冒烟（tests/smoke/portal.smoke.ts）对真实网关核对；这里只看开关
    const site = (await (await fetch(`${base}/v1/site-config`)).json()) as { password_reset: boolean }
    expect(site.password_reset).toBe(true)
    const known = (await (await post('/v1/auth/password-reset/start', { email: MOCK_ACCOUNTS.portal.email })).json()) as Record<string, unknown>
    const unknown = (await (await post('/v1/auth/password-reset/start', { email: 'nobody@example.test' })).json()) as Record<string, unknown>
    expect(unknown.message).toBe(known.message)
    expect((await post('/v1/auth/password-reset/start', { email: 'not-an-email' })).status).toBe(422)
  })

  it('resets the password once and signs every session out', async () => {
    const before = await loginAs(base, MOCK_ACCOUNTS.portal)
    await post('/v1/auth/password-reset/start', { email: MOCK_ACCOUNTS.portal.email })
    const wrong = await post('/v1/auth/password-reset/complete', { email: MOCK_ACCOUNTS.portal.email, code: '000000', new_password: 'NewPass2026' })
    expect(wrong.status).toBe(422)
    expect(await wrong.json()).toMatchObject({ error: { fields: { code: expect.any(String) } } })
    const ok = await post('/v1/auth/password-reset/complete', { email: MOCK_ACCOUNTS.portal.email, code: '123456', new_password: 'NewPass2026' })
    expect(await ok.json()).toEqual({ ok: true })
    // 一次性：同一枚验证码再用就不认
    expect((await post('/v1/auth/password-reset/complete', { email: MOCK_ACCOUNTS.portal.email, code: '123456', new_password: 'Other2026x' })).status).toBe(422)
    // 旧会话已退出，旧密码登不进去，新密码可以
    expect((await fetch(`${base}/v1/me`, { headers: { Authorization: `Bearer ${before.access_token}` } })).status).toBe(401)
    expect((await post('/v1/auth/login', MOCK_ACCOUNTS.portal)).status).toBe(401)
    expect((await post('/v1/auth/login', { email: MOCK_ACCOUNTS.portal.email, password: 'NewPass2026' })).status).toBe(200)
  })

  it('mail paused: start returns 503, complete still accepts an earlier code', async () => {
    const start1 = await post('/v1/auth/password-reset/start', { email: MOCK_ACCOUNTS.portal.email })
    const startBody = (await start1.json()) as { dev_code: string }
    expect(startBody.dev_code).toBe('123456')
    await post('/__mock/portal-signup', { mail_paused: true })
    const site = (await (await fetch(`${base}/v1/site-config`)).json()) as { password_reset: boolean }
    expect(site.password_reset).toBe(false)
    const start2 = await post('/v1/auth/password-reset/start', { email: MOCK_ACCOUNTS.portal.email })
    expect(start2.status).toBe(503)
    expect(await start2.json()).toMatchObject({
      error: { code: 'service_unavailable', message: '邮件发送暂停，暂时无法找回密码，请稍后再试' },
    })
    const complete = await post('/v1/auth/password-reset/complete', {
      email: MOCK_ACCOUNTS.portal.email,
      code: startBody.dev_code,
      new_password: 'newpass123',
    })
    expect(complete.status).toBe(200)
    await post('/__mock/portal-signup', { reset: true })
    // 把演示账号密码改回，避免影响同进程里其它用例（若有）
    const restoreStart = await post('/v1/auth/password-reset/start', { email: MOCK_ACCOUNTS.portal.email })
    const restoreCode = ((await restoreStart.json()) as { dev_code: string }).dev_code
    await post('/v1/auth/password-reset/complete', {
      email: MOCK_ACCOUNTS.portal.email,
      code: restoreCode,
      new_password: MOCK_ACCOUNTS.portal.password,
    })
  })
})
