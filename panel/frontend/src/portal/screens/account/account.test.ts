/**
 * [INPUT]: 依赖 vitest，依赖 ../../../core/api 的 ApiError，依赖 ./api 的 schema，依赖 ./model 的纯逻辑
 * [OUTPUT]: 无（测试）
 * [POS]: 第 ⑥ 步账号安全的单元测试：会话 / Telegram / 偏好 schema（omitempty 缺席、枚举收紧）、设备名解析、会话排序、新密码本地校验（与后端同序）、改密错误落位（401 落当前密码、fields.password 落新密码）、倒计时与 t.me 深链
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it } from 'vitest'
import { ApiError } from '../../../core/api'
import { preferenceSchema, quickLoginSchema, sessionSchema, telegramSchema } from './api'
import { deviceName, formatCountdown, passwordErrors, secondsLeft, shortUserId, sortSessions, telegramDeepLink, validateNewPassword } from './model'

const UA = {
  chromeMac: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36',
  edgeWin: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36 Edg/129.0.0.0',
  safariPhone: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1',
  firefoxLinux: 'Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0',
  chromeAndroid: 'Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Mobile Safari/537.36',
  chromeIpad: 'Mozilla/5.0 (iPad; CPU OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/129.0.0.0 Mobile/15E148 Safari/604.1',
}

const err = (status: number, message = '', fields?: Record<string, string>) =>
  new ApiError({ status, code: status === 401 ? 'unauthorized' : status === 400 ? 'bad_request' : status === 422 ? 'validation_failed' : 'internal_error', message, fields })

describe('账号安全', () => {
  it('会话 schema：country / last_seen_at / expires_at 可缺席', () => {
    const s = sessionSchema.parse({ id: 'x', current: true, user_agent: '', created_at: '2026-09-24T00:00:00Z' })
    expect(s.country).toBeUndefined()
  })

  it('Telegram schema：username 与 bot_username 是 omitempty，bound / enabled 必填', () => {
    expect(telegramSchema.parse({ bound: false, enabled: false })).toEqual({ bound: false, enabled: false })
    expect(() => telegramSchema.parse({ bound: false })).toThrow()
  })

  it('偏好 schema 只收 3 类 × 2 渠道；快捷登录 expires_in 必须为正', () => {
    expect(() => preferenceSchema.parse({ category: 'security', channel: 'email', enabled: true, locked: false })).toThrow()
    expect(() => preferenceSchema.parse({ category: 'service', channel: 'sms', enabled: true, locked: false })).toThrow()
    expect(() => quickLoginSchema.parse({ token: 't', expires_at: 'x', expires_in: 0 })).toThrow()
  })

  it('设备名：浏览器 · 系统，认不出给「未知设备」', () => {
    expect(deviceName(UA.chromeMac)).toBe('Chrome · macOS')
    expect(deviceName(UA.edgeWin)).toBe('Edge · Windows')
    expect(deviceName(UA.safariPhone)).toBe('Safari · iPhone')
    expect(deviceName(UA.firefoxLinux)).toBe('Firefox · Linux')
    expect(deviceName(UA.chromeAndroid)).toBe('Chrome · Android')
    expect(deviceName(UA.chromeIpad)).toBe('Chrome · iPad')
    expect(deviceName('Shadowrocket/2070 CFNetwork/1498 Darwin/23.6.0 iPhone15,2')).toBe('Shadowrocket · iPhone')
    expect(deviceName('curl/8.7.1')).toBe('未知设备')
    expect(deviceName('')).toBe('未知设备')
  })

  it('当前会话排最前，其余按登录时间倒序', () => {
    const list = sortSessions([
      { id: 'a', current: false, created_at: '2026-09-20T00:00:00Z' },
      { id: 'b', current: true, created_at: '2026-09-01T00:00:00Z' },
      { id: 'c', current: false, created_at: '2026-09-23T00:00:00Z' },
    ])
    expect(list.map((s) => s.id)).toEqual(['b', 'c', 'a'])
  })

  it('新密码：长度、字节上限、字母与数字，与后端同序', () => {
    expect(validateNewPassword('abc123')).toBe('密码至少需要 8 个字符')
    expect(validateNewPassword('abcdefgh')).toBe('密码必须同时包含字母和数字')
    expect(validateNewPassword('12345678')).toBe('密码必须同时包含字母和数字')
    expect(validateNewPassword('密码安全很重要12')).toBeNull()
    expect(validateNewPassword(`${'密'.repeat(86)}1`)).toBe('密码过长')
    expect(validateNewPassword('pandora-2026')).toBeNull()
  })

  it('改密错误落位：401 落当前密码、400 与 fields.password 落新密码、其余落表单', () => {
    expect(passwordErrors(err(401, '当前密码不正确'))).toEqual({ old: '当前密码不正确' })
    expect(passwordErrors(err(400, '新密码不能与当前密码相同'))).toEqual({ next: '新密码不能与当前密码相同' })
    expect(passwordErrors(err(422, 'x', { password: '密码必须同时包含字母和数字' }))).toEqual({ next: '密码必须同时包含字母和数字' })
    expect(passwordErrors(err(422, 'x', { old_password: '必填', new_password: '必填' }))).toEqual({ old: '必填', next: '必填' })
    expect(passwordErrors(err(500, '服务暂时不可用'))).toEqual({ form: '服务暂时不可用' })
    expect(passwordErrors(new Error('x'))).toEqual({ form: '修改失败，请稍后重试' })
  })

  it('倒计时、用户 ID 与 t.me 深链', () => {
    expect(secondsLeft(10_500, 10_000)).toBe(1)
    expect(secondsLeft(10_000, 10_000)).toBe(0)
    expect(secondsLeft(9_000, 10_000)).toBe(0)
    expect(formatCountdown(600)).toBe('10:00')
    expect(formatCountdown(65)).toBe('1:05')
    expect(shortUserId('3b1f6c2e-5d7a-4c11-9e2b-7a0d4f8c6e21')).toBe('3b1f6c2e')
    expect(telegramDeepLink('pandora_notify_bot', 'ABCD2345')).toBe('https://t.me/pandora_notify_bot?start=ABCD2345')
  })
})
