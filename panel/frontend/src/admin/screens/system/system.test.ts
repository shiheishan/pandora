import { describe, expect, it } from 'vitest'
import {
  adminAlertBody,
  deliveryRow,
  durationLabel,
  eventsLabel,
  extrasBody,
  extrasFormFrom,
  formatSender,
  hookBody,
  hookFormFrom,
  hookHealth,
  hookTone,
  hostOf,
  insertVariable,
  newHookCode,
  parseChatId,
  parseGroupChatId,
  parseSender,
  registrationBody,
  smtpBody,
  smtpFormFrom,
  smtpStatus,
  telegramBody,
  telegramFormFrom,
  telegramStatus,
  templateMeta,
  templateName,
  validateHook,
  validateSmtp,
  validateTemplate,
} from './logic'
import { deliveriesResponse, hooksResponse, telegramSettingsSchema, templatesResponse, type Hook, type MailSettings, type Template } from './schemas'

const mail: MailSettings = { smtp_host: 'smtp.qq.com', smtp_port: 465, encryption: 'ssl', smtp_username: 'u', has_password: true, from_address: 'noreply@example.test', from_name: 'Pandora', email_verification: false, registration_mode: 'closed' }

const hook: Hook = { id: '00000000-0000-4000-8000-000000000001', code: 'hook-abc123', name: 'ops', description: '', enabled: true, events: ['order.paid'], endpoint_url: 'https://ops.x/h', has_secret: true, timeout_ms: 5000, max_attempts: 5, queued_count: 0, failed_count: 1, sent_count_7d: 3 }

const template = (patch: Partial<Template> = {}): Template => ({ code: 'order.paid', channel: 'inapp', locale: 'zh-CN', category: 't', status: 'active', version: 1, subject: 's', body: 'b', allowed_variables: ['plan'], is_default: true, updated_at: '2026-09-01T00:00:00Z', has_default: true, description: '', preview_subject: 's', preview_body: 'b', ...patch })

describe('mail', () => {
  it('splits and joins the single sender box', () => {
    expect(formatSender('Pandora', 'a@b.c')).toBe('Pandora <a@b.c>')
    expect(formatSender('', 'a@b.c')).toBe('a@b.c')
    expect(parseSender('Pandora <a@b.c>')).toEqual({ from_name: 'Pandora', from_address: 'a@b.c' })
    expect(parseSender('"潘多拉 面板" <a@b.c>')).toEqual({ from_name: '潘多拉 面板', from_address: 'a@b.c' })
    expect(parseSender(' a@b.c ')).toEqual({ from_name: '', from_address: 'a@b.c' })
  })

  it('overwrites all six SMTP fields and leaves registration out', () => {
    const f = smtpFormFrom(mail)
    expect(smtpBody(f)).toEqual({ smtp_host: 'smtp.qq.com', smtp_port: 465, encryption: 'ssl', smtp_username: 'u', smtp_password: '', from_name: 'Pandora', from_address: 'noreply@example.test' })
    expect(smtpBody({ ...f, clearPassword: true, password: 'x' }).smtp_password).toBe('-')
    expect(validateSmtp({ ...f, port: '70000' }).smtp_port).toBeDefined()
    expect(validateSmtp({ ...f, sender: 'Pandora <bad>' }).from_address).toBeDefined()
    expect(smtpStatus(mail).tone).toBe('ok')
    expect(smtpStatus({ ...mail, smtp_host: '' })).toEqual({ label: '未配置', tone: 'muted' })
  })

  it('saves registration with the saved SMTP fields and no password', () => {
    const body = registrationBody(mail, 'open', true)
    expect(body).toMatchObject({ smtp_host: 'smtp.qq.com', from_address: 'noreply@example.test', registration_mode: 'open', email_verification: true })
    expect('smtp_password' in body).toBe(false)
  })
})

describe('telegram', () => {
  const saved = { enabled: true, bot_username: 'bot', has_token: true, admin_chat_id: -100123 }
  it('labels the state without probing', () => {
    expect(telegramStatus(saved).label).toBe('已启用 @bot')
    expect(telegramStatus({ ...saved, enabled: false }).label).toBe('已停用')
    expect(telegramStatus({ ...saved, has_token: false }).label).toBe('未配置 Token')
  })

  it('parses chat ids and only sends admin_chat_id when it changed', () => {
    expect(parseChatId('')).toEqual({ ok: true, value: null })
    expect(parseChatId('-1002231180042')).toEqual({ ok: true, value: -1002231180042 })
    expect(parseChatId('0').ok).toBe(false)
    expect(parseChatId('12a').ok).toBe(false)
    expect(parseChatId('99999999999999999999').ok).toBe(false)
    const f = telegramFormFrom(saved)
    expect(telegramBody({ ...f, username: '@bot' }, saved)).toEqual({ enabled: true, bot_username: 'bot', bot_token: '' })
    expect(telegramBody({ ...f, chatId: '' }, saved)).toMatchObject({ admin_chat_id: null })
    expect(parseChatId('42')).toEqual({ ok: true, value: 42 })
    expect(parseGroupChatId('42').ok).toBe(false)
    expect(parseGroupChatId('-42')).toEqual({ ok: true, value: -42 })
    expect(parseGroupChatId('')).toEqual({ ok: true, value: null })
    expect(telegramBody({ ...f, chatId: '-42' }, saved)).toMatchObject({ admin_chat_id: -42 })
    expect(telegramBody({ ...f, chatId: '42' }, saved)).toEqual({ error: expect.any(String) })
    expect(telegramBody({ ...f, chatId: 'x' }, saved)).toEqual({ error: expect.any(String) })
    expect(telegramSettingsSchema.parse({ ...saved, admin_chat_id: null }).admin_chat_id).toBeNull()
  })
})

describe('templates', () => {
  it('names, marks and validates templates', () => {
    expect(templateName('auth.email_verify')).toBe('注册验证码')
    expect(templateName('x.y')).toBe('x.y')
    expect(templateMeta(template(), undefined).label).toBe('默认')
    expect(templateMeta(template({ is_default: false }), undefined).label).toBe('已自定义')
    expect(templateMeta(template({ has_default: false, is_default: false }), undefined).label).toBe('无内置默认')
    expect(templateMeta(template(), { subject: 's', body: 'b2' })).toEqual({ label: '未保存的修改', tone: 'warn' })
    expect(templateMeta(template(), { subject: 's', body: 'b' }).label).toBe('默认')
    expect(validateTemplate({ subject: ' ', body: 'x' }).subject).toBeDefined()
    expect(validateTemplate({ subject: 'x', body: 'y'.repeat(20001) }).body).toBeDefined()
  })

  it('inserts {{name}} at the cursor', () => {
    expect(insertVariable('ab', 1, 1, 'plan')).toEqual({ text: 'a{{plan}}b', cursor: 9 })
    expect(insertVariable('abc', 1, 2, 'x')).toEqual({ text: 'a{{x}}c', cursor: 6 })
    expect(insertVariable('ab', 99, 99, 'x').text).toBe('ab{{x}}')
  })

  it('accepts the list shape with has_default', () => {
    expect(templatesResponse.parse({ templates: [template()] }).templates).toHaveLength(1)
    expect(() => templatesResponse.parse({ templates: [{ ...template(), channel: 'sms' }] })).toThrow()
  })
})

describe('telegram extras', () => {
  const extras = { sub_command_enabled: false, start_message: '', admin_paid_notify: true, admin_ticket_notify: false, join_review_enabled: false, user_group_chat_id: -1001, discuss_link: '' }

  it('keeps the saved admin alerts when the dialog saves its five keys', () => {
    const f = { ...extrasFormFrom(extras), subCommand: true, userGroupChatId: '' }
    expect(extrasBody(f, extras)).toEqual({ ...extras, sub_command_enabled: true, user_group_chat_id: null })
    expect(extrasBody({ ...f, userGroupChatId: '123' }, extras)).toEqual({ error: { user_group_chat_id: '用户群 chat id 必须是负数（群组的 chat id 以 - 开头）' } })
    expect(extrasFormFrom(extras).userGroupChatId).toBe('-1001')
  })

  it('flips one admin alert and sends the other six keys unchanged', () => {
    expect(adminAlertBody(extras, 'admin_ticket_notify', true)).toEqual({ ...extras, admin_ticket_notify: true })
    expect(adminAlertBody(extras, 'admin_paid_notify', false)).toEqual({ ...extras, admin_paid_notify: false })
  })
})

describe('hooks', () => {
  it('computes health, tone and labels', () => {
    expect(hookHealth(hook)).toBe('近 7 天有 1 次失败')
    expect(hookHealth({ ...hook, failed_count: 0 })).toBe('正常')
    expect(hookHealth({ ...hook, enabled: false })).toBe('已停用')
    expect(hookTone(hook)).toBe('warn')
    expect(hookTone({ ...hook, failed_count: 0 })).toBe('ok')
    expect(hookTone({ ...hook, enabled: false })).toBe('muted')
    const catalog = [
      { name: 'order.paid', desc: '订单支付成功' },
      { name: 'order.cancelled', desc: '订单取消' },
      { name: 'user.registered', desc: '用户完成注册' },
    ]
    expect(eventsLabel([], catalog)).toBe('未订阅事件')
    expect(eventsLabel(['order.paid', 'order.cancelled', 'user.registered'], catalog)).toBe('全部事件')
    expect(eventsLabel(['order.paid', 'x.unknown'], catalog)).toBe('订单支付成功、x.unknown')
    expect(eventsLabel(['order.paid'], [])).toBe('order.paid')
    expect(durationLabel(null)).toBe('—')
    expect(durationLabel(88)).toBe('88 ms')
    expect(durationLabel(10000)).toBe('10.0 s')
    expect(hostOf('https://a.b.c/x')).toBe('a.b.c')
    expect(hostOf('nope')).toBe('')
  })

  it('avoids existing codes when generating a new one', () => {
    let i = 0
    const seq = [0, 0, 0, 0, 0, 0, 0.99, 0.99, 0.99, 0.99, 0.99, 0.99]
    const code = newHookCode(['hook-aaaaaa'], () => seq[i++ % seq.length]!)
    expect(code).toBe('hook-999999')
    expect(newHookCode([])).toMatch(/^hook-[a-z0-9]{6}$/)
  })

  it('refills every field for the upsert and checks the DB ranges first', () => {
    const f = hookFormFrom(hook)
    expect(hookBody(hook.code, { ...f, enabled: false })).toEqual({ code: 'hook-abc123', name: 'ops', description: '', enabled: false, events: ['order.paid'], endpoint_url: 'https://ops.x/h', secret: '', timeout_ms: 5000, max_attempts: 5 })
    expect(validateHook(f)).toEqual({})
    expect(Object.keys(validateHook({ ...f, endpoint: 'http://x', timeoutMs: '100', maxAttempts: '11', events: [], name: ' ' })).sort()).toEqual(['endpoint_url', 'events', 'max_attempts', 'name', 'timeout_ms'])
  })

  it('formats delivery rows and accepts null lists', () => {
    const base = { event: 'order.paid', status: 'sent' as const, attempts: 1, response_code: 200, error_message: '', created_at: '2026-09-24 21:40:18', sent_at: '2026-09-24 21:40:19', duration_ms: 84 }
    expect(deliveryRow(base)).toEqual({ at: '09-24 21:40', event: 'order.paid', code: '200', tone: 'ok', duration: '84 ms', attempts: '1' })
    expect(deliveryRow({ ...base, status: 'failed', response_code: 0, attempts: 3, sent_at: '', duration_ms: null })).toEqual({ at: '09-24 21:40', event: 'order.paid', code: '—', tone: 'warn', duration: '—', attempts: '3' })
    expect(deliveryRow({ ...base, status: 'queued', response_code: 0, attempts: 0 })).toMatchObject({ code: '排队', tone: 'muted', attempts: '—' })
    expect(deliveriesResponse.parse({ deliveries: null }).deliveries).toBeNull()
    expect(hooksResponse.parse({ hooks: [hook], events: [{ name: 'order.paid', desc: '订单支付成功' }] }).hooks).toHaveLength(1)
    expect(() => hooksResponse.parse({ hooks: [hook], events: [{ Name: 'x', Desc: 'y' }] })).toThrow()
  })
})
