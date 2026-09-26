/**
 * [INPUT]: 依赖 vitest，依赖 ./logic，依赖 ./schemas
 * [OUTPUT]: 无（测试文件）
 * [POS]: admin/screens/system 纯函数层与 schema 边界的单元测试：发件人拆拼、SMTP 与注册卡请求体（整体覆盖、带已保存字段）、Telegram 状态与 chat id、模板名称 / 状态标记 / 变量插入 / 长度校验、钩子成功率 / 状态点 / code 避让 / 全量请求体 / 范围校验 / 投递行；界面交互在浏览器里对 dev 假后端验收
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it } from 'vitest'
import {
  deliveryRow,
  durationLabel,
  eventsLabel,
  formatSender,
  hookBody,
  hookFormFrom,
  hookTone,
  hostOf,
  insertVariable,
  newHookCode,
  parseChatId,
  parseSender,
  registrationBody,
  smtpBody,
  smtpFormFrom,
  smtpStatus,
  successRate,
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

const mail: MailSettings = { smtp_host: 'smtp.qq.com', smtp_port: 465, encryption: 'ssl', smtp_username: 'u', has_password: true, from_address: 'noreply@x.run', from_name: 'Pandora', email_verification: false, registration_mode: 'closed' }

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
    expect(smtpBody(f)).toEqual({ smtp_host: 'smtp.qq.com', smtp_port: 465, encryption: 'ssl', smtp_username: 'u', smtp_password: '', from_name: 'Pandora', from_address: 'noreply@x.run' })
    expect(smtpBody({ ...f, clearPassword: true, password: 'x' }).smtp_password).toBe('-')
    expect(validateSmtp({ ...f, port: '70000' }).smtp_port).toBeDefined()
    expect(validateSmtp({ ...f, sender: 'Pandora <bad>' }).from_address).toBeDefined()
    expect(smtpStatus(mail).tone).toBe('ok')
    expect(smtpStatus({ ...mail, smtp_host: '' })).toEqual({ label: '未配置', tone: 'muted' })
  })

  it('saves registration with the saved SMTP fields and no password', () => {
    const body = registrationBody(mail, 'open', true)
    expect(body).toMatchObject({ smtp_host: 'smtp.qq.com', from_address: 'noreply@x.run', registration_mode: 'open', email_verification: true })
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
    expect(telegramBody({ ...f, chatId: '42' }, saved)).toMatchObject({ admin_chat_id: 42 })
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

describe('hooks', () => {
  it('computes rate, tone and labels', () => {
    expect(successRate(hook)).toBe('75.0%')
    expect(successRate({ sent_count_7d: 0, failed_count: 0 })).toBe('—')
    expect(successRate({ sent_count_7d: 5, failed_count: 0 })).toBe('100%')
    expect(hookTone(hook)).toBe('warn')
    expect(hookTone({ ...hook, failed_count: 0 })).toBe('ok')
    expect(hookTone({ ...hook, enabled: false })).toBe('muted')
    expect(eventsLabel([], 10)).toBe('未订阅事件')
    expect(eventsLabel(['a', 'b'], 2)).toBe('全部事件')
    expect(eventsLabel(['a', 'b'], 10)).toBe('a, b')
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
    expect(deliveryRow(base)).toEqual({ code: '200', tone: 'ok', event: 'order.paid', duration: '84 ms', at: '09-24 21:40:19' })
    expect(deliveryRow({ ...base, status: 'failed', response_code: 0, attempts: 3, sent_at: '', duration_ms: null })).toMatchObject({ code: '—', tone: 'warn', event: 'order.paid · 第 3 次', duration: '—', at: '09-24 21:40:18' })
    expect(deliveryRow({ ...base, status: 'queued', response_code: 0, attempts: 0 }).code).toBe('排队')
    expect(deliveriesResponse.parse({ deliveries: null }).deliveries).toBeNull()
    expect(hooksResponse.parse({ hooks: [hook], events: [{ name: 'order.paid', desc: '订单支付成功' }] }).hooks).toHaveLength(1)
    expect(() => hooksResponse.parse({ hooks: [hook], events: [{ Name: 'x', Desc: 'y' }] })).toThrow()
  })
})
