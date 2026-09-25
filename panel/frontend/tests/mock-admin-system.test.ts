/**
 * [INPUT]: 依赖 vitest，依赖 ./mock-helpers，依赖 ../dev/mock-api 的 MOCK_ACCOUNTS，依赖 ../src/admin/screens/system/schemas 的邮件 / Telegram / 模板 / 钩子 schema
 * [OUTPUT]: 对外提供通知与插件（后台-09 前半）假接口的测试
 * [POS]: tests 的通知假后端守卫：只读账号只开放模板、SMTP 整体覆盖与密码保留 / 清空、注册校验、Telegram chat id 缺省不改与测试回落、模板变量白名单 / 预览 / 恢复默认 / 测试信要 reauth、钩子 upsert 一次性密钥、内网地址与未知事件 422、越界 500、投递记录 null、删除 404
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { deliveriesResponse, hookSaved, hooksResponse, hookTested, mailSettingsSchema, telegramSettingsSchema, templatePreview, templateReset, templateSaved, templatesResponse } from '../src/admin/screens/system/schemas'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · admin notifications and hooks', () => {
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

  it('lets the viewer read templates only', async () => {
    const res = await fetch(`${base}/v1/auth/login`, { method: 'POST', body: JSON.stringify(MOCK_ACCOUNTS.viewer) })
    const viewer = { Authorization: `Bearer ${((await res.json()) as { access_token: string }).access_token}` }
    expect((await fetch(`${base}/v1/mail/templates`, { headers: viewer })).status).toBe(200)
    for (const path of ['/v1/settings/mail', '/v1/settings/telegram', '/v1/plugin-hooks']) expect((await fetch(`${base}${path}`, { headers: viewer })).status, path).toBe(404)
  })

  it('overwrites SMTP fields, keeps the password when empty and validates registration', async () => {
    const saved = mailSettingsSchema.parse(await json(await call('GET', '/v1/settings/mail')))
    expect(saved).toMatchObject({ has_password: true, from_name: 'Pandora' })
    const smtp = { smtp_host: saved.smtp_host, smtp_port: saved.smtp_port, encryption: saved.encryption, smtp_username: saved.smtp_username, from_address: saved.from_address, from_name: '' }
    expect((await call('POST', '/v1/settings/mail', { ...smtp, smtp_password: '', extra: 1 })).status).toBe(400)
    expect(await json(await call('POST', '/v1/settings/mail', { ...smtp, smtp_port: 0 }))).toMatchObject({ error: { fields: { smtp_port: expect.any(String) } } })
    expect(await json(await call('POST', '/v1/settings/mail', { ...smtp, smtp_host: '', email_verification: true }))).toMatchObject({ error: { fields: { email_verification: expect.any(String) } } })
    expect((await call('POST', '/v1/settings/mail', { ...smtp, registration_mode: 'open', email_verification: true })).status).toBe(200)
    expect(mailSettingsSchema.parse(await json(await call('GET', '/v1/settings/mail')))).toMatchObject({ has_password: true, registration_mode: 'open', email_verification: true })
    expect((await call('POST', '/v1/settings/mail', { ...smtp, smtp_password: '-' })).status).toBe(200)
    expect(mailSettingsSchema.parse(await json(await call('GET', '/v1/settings/mail'))).has_password).toBe(false)
    expect(await json(await call('POST', '/v1/settings/mail/test', { to: '' }))).toMatchObject({ error: { fields: { to: '请填写收件地址' } } })
    expect(await json(await call('POST', '/v1/settings/mail/test', { to: 'me@x.run' }))).toEqual({ ok: true, to: 'me@x.run' })
  })

  it('keeps the Telegram chat id unless sent and falls back to it for tests', async () => {
    const tg = telegramSettingsSchema.parse(await json(await call('GET', '/v1/settings/telegram')))
    expect(tg.admin_chat_id).not.toBeNull()
    expect(await json(await call('POST', '/v1/settings/telegram', { enabled: true, bot_username: '', bot_token: '' }))).toMatchObject({ error: { fields: { bot_username: expect.any(String) } } })
    expect(await json(await call('POST', '/v1/settings/telegram', { enabled: true, bot_username: '@bot', bot_token: '' }))).toEqual({ ok: true, enabled: true })
    expect(telegramSettingsSchema.parse(await json(await call('GET', '/v1/settings/telegram')))).toMatchObject({ bot_username: 'bot', admin_chat_id: tg.admin_chat_id })
    expect(await json(await call('POST', '/v1/settings/telegram/test', {}))).toEqual({ sent: true })
    expect((await call('POST', '/v1/settings/telegram', { enabled: true, bot_username: 'bot', bot_token: '', admin_chat_id: null })).status).toBe(200)
    expect(await json(await call('POST', '/v1/settings/telegram/test', {}))).toMatchObject({ error: { fields: { chat_id: expect.any(String) } } })
    expect(await json(await call('POST', '/v1/settings/telegram', { enabled: false, bot_username: 'bot', bot_token: '', admin_chat_id: 0 }))).toMatchObject({ error: { fields: { admin_chat_id: expect.any(String) } } })
  })

  it('saves, previews, resets and test-sends templates within the variable whitelist', async () => {
    const list = templatesResponse.parse(await json(await call('GET', '/v1/mail/templates'))).templates
    expect(list).toHaveLength(12)
    expect(list.find((t) => t.code === 'admin.broadcast')).toMatchObject({ has_default: false, is_default: false })
    const t = list.find((x) => x.code === 'quota.warning' && x.channel === 'email')!
    expect(t).toMatchObject({ has_default: true, is_default: true })
    const preview = templatePreview.parse(await json(await call('POST', '/v1/mail/templates/preview', { code: t.code, channel: t.channel, subject: '{{plan}} {{nope}}', body: 'x' })))
    expect(preview).toEqual({ preview_subject: '旗舰套餐 {{nope}}', preview_body: 'x', unknown_variables: ['nope'] })
    expect(await json(await call('POST', '/v1/mail/templates', { code: t.code, channel: t.channel, subject: 'x', body: '{{nope}}' }))).toMatchObject({ error: { fields: { body: expect.stringContaining('nope') } } })
    const saved = templateSaved.parse(await json(await call('POST', '/v1/mail/templates', { code: t.code, channel: t.channel, subject: '新主题 {{percent}}%', body: '正文' })))
    expect(saved).toMatchObject({ template: { is_default: false, version: t.version + 1 }, preview_subject: '新主题 85%' })
    expect(templateReset.parse(await json(await call('POST', '/v1/mail/templates/reset', { code: t.code, channel: t.channel }))).template.is_default).toBe(true)
    expect((await call('POST', '/v1/mail/templates/reset', { code: 'admin.broadcast', channel: 'email' })).status).toBe(404)
    await fetch(`${base}/__mock/expire-reauth`, { method: 'POST' })
    expect(await json(await call('POST', '/v1/mail/templates/test', { code: t.code, channel: 'email', to: 'me@x.run' }))).toMatchObject({ error: { code: 'reauth_required' } })
  })

  it('upserts hooks, hands out a generated secret once and rejects private endpoints', async () => {
    const reauth = await call('POST', '/v1/auth/reauth', { password: MOCK_ACCOUNTS.admin.password })
    auth = { Authorization: `Bearer ${((await reauth.json()) as { access_token: string }).access_token}` }
    const listed = hooksResponse.parse(await json(await call('GET', '/v1/plugin-hooks')))
    expect(listed.events.map((e) => e.name)).toContain('giftcard.redeemed')
    const body = { code: 'hook-test01', name: 'x.run', description: '', enabled: true, events: ['order.paid'], endpoint_url: 'https://x.run/h', secret: '', timeout_ms: 5000, max_attempts: 5 }
    expect(await json(await call('POST', '/v1/plugin-hooks', { ...body, endpoint_url: 'https://10.0.0.8/h' }, 'h-1'))).toMatchObject({ error: { fields: { endpoint_url: '不允许指向内网或本机地址' } } })
    expect(await json(await call('POST', '/v1/plugin-hooks', { ...body, events: ['node.offline'] }, 'h-2'))).toMatchObject({ error: { fields: { events: '未知事件：node.offline' } } })
    expect((await call('POST', '/v1/plugin-hooks', { ...body, timeout_ms: 100 }, 'h-3')).status).toBe(500)
    const created = hookSaved.parse(await json(await call('POST', '/v1/plugin-hooks', body, 'h-4')))
    expect(created.secret).toMatch(/^whsec_/)
    const again = hookSaved.parse(await json(await call('POST', '/v1/plugin-hooks', { ...body, enabled: false }, 'h-5')))
    expect(again.secret).toBeUndefined()
    const row = hooksResponse.parse(await json(await call('GET', '/v1/plugin-hooks'))).hooks.find((h) => h.code === 'hook-test01')!
    expect(row).toMatchObject({ enabled: false, has_secret: true, sent_count_7d: 0 })
    expect(deliveriesResponse.parse(await json(await call('GET', '/v1/plugin-hooks/hook-test01/deliveries'))).deliveries).toBeNull()
    expect(hookTested.parse(await json(await call('POST', '/v1/plugin-hooks/hook-test01/test')))).toMatchObject({ sent: true, response_code: 200 })
    expect((await call('DELETE', '/v1/plugin-hooks/hook-test01')).status).toBe(200)
    expect((await call('DELETE', '/v1/plugin-hooks/hook-test01')).status).toBe(404)
  })
})
