import { describe, expect, it } from 'vitest'
import { ApiError } from '../core/api'
import { hasLoginErrors, loginFailure, validateLogin } from './login-form'

describe('validateLogin', () => {
  it('returns both errors when both are empty', () => {
    expect(validateLogin('', '')).toEqual({
      email: '请输入邮箱',
      password: '请输入密码',
    })
    expect(validateLogin('   ', '')).toEqual({
      email: '请输入邮箱',
      password: '请输入密码',
    })
  })

  it('returns email error when email is missing but password provided', () => {
    expect(validateLogin('', 'secret123')).toEqual({
      email: '请输入邮箱',
    })
  })

  it('returns password error when password is missing but email provided', () => {
    expect(validateLogin('admin@example.test', '')).toEqual({
      password: '请输入密码',
    })
  })

  it('validates email format', () => {
    expect(validateLogin('not-an-email', 'secret123')).toEqual({
      email: '邮箱格式不对',
    })
    expect(validateLogin('user@', 'secret123')).toEqual({
      email: '邮箱格式不对',
    })
    expect(validateLogin('user@test', 'secret123')).toEqual({
      email: '邮箱格式不对',
    })
  })

  it('accepts valid email with leading or trailing spaces after trim', () => {
    expect(validateLogin('  admin@example.test  ', 'secret123')).toEqual({})
  })

  it('returns empty object when valid', () => {
    expect(validateLogin('admin@example.test', 'secret123')).toEqual({})
  })
})

describe('loginFailure', () => {
  it('maps 401 error to password field', () => {
    const err = new ApiError({
      status: 401,
      code: 'unauthorized',
      message: '邮箱或密码不正确',
    })
    expect(loginFailure(err)).toEqual({
      password: '邮箱或密码不正确',
    })
  })

  it('maps 422 with fields.email to email field', () => {
    const err = new ApiError({
      status: 422,
      code: 'validation_failed',
      message: '参数错误',
      fields: { email: '邮箱已被禁用' },
    })
    expect(loginFailure(err)).toEqual({
      email: '邮箱已被禁用',
    })
  })

  it('maps 422 with fields.password to password field', () => {
    const err = new ApiError({
      status: 422,
      code: 'validation_failed',
      message: '参数错误',
      fields: { password: '密码已过期' },
    })
    expect(loginFailure(err)).toEqual({
      password: '密码已过期',
    })
  })

  it('maps 422 with both fields to both', () => {
    const err = new ApiError({
      status: 422,
      code: 'validation_failed',
      message: '参数错误',
      fields: { email: '邮箱有误', password: '密码有误' },
    })
    expect(loginFailure(err)).toEqual({
      email: '邮箱有误',
      password: '密码有误',
    })
  })

  it('maps 422 without email or password fields to form error', () => {
    const err = new ApiError({
      status: 422,
      code: 'validation_failed',
      message: '校验失败',
      fields: { code: '验证码错误' },
    })
    expect(loginFailure(err)).toEqual({
      form: '校验失败',
    })
  })

  it('maps 429 rate limited to form error', () => {
    const err = new ApiError({
      status: 429,
      code: 'rate_limited',
      message: '尝试过于频繁，请稍后再试',
    })
    expect(loginFailure(err)).toEqual({
      form: '尝试过于频繁，请稍后再试',
    })
  })

  it('maps other ApiError to form error with message', () => {
    const err = new ApiError({
      status: 500,
      code: 'internal_error',
      message: '系统繁忙',
    })
    expect(loginFailure(err)).toEqual({
      form: '系统繁忙',
    })
  })

  it('maps non-ApiError to generic form error', () => {
    expect(loginFailure(new Error('Network down'))).toEqual({
      form: '登录失败，请稍后重试',
    })
    expect(loginFailure('unknown error')).toEqual({
      form: '登录失败，请稍后重试',
    })
  })
})

describe('hasLoginErrors', () => {
  it('checks if any field has value', () => {
    expect(hasLoginErrors({})).toBe(false)
    expect(hasLoginErrors({ email: 'err' })).toBe(true)
    expect(hasLoginErrors({ password: 'err' })).toBe(true)
    expect(hasLoginErrors({ form: 'err' })).toBe(true)
    expect(hasLoginErrors({ email: '', password: '' })).toBe(false)
  })
})
