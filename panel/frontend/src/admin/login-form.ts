import { isApiError } from '../core/api'

export interface LoginErrors {
  email?: string
  password?: string
  form?: string
}

const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

/** 任一字段有值即视为有错误 */
export function hasLoginErrors(e: LoginErrors): boolean {
  return Boolean(e.email || e.password || e.form)
}

/** 登录前端校验：先 trim 邮箱 */
export function validateLogin(email: string, password: string): LoginErrors {
  const errors: LoginErrors = {}
  const trimmed = email.trim()

  if (!trimmed) {
    errors.email = '请输入邮箱'
  } else if (!EMAIL_PATTERN.test(trimmed)) {
    errors.email = '邮箱格式不对'
  }

  if (!password) {
    errors.password = '请输入密码'
  }

  return errors
}

/** 将登录异常转为表单错误 */
export function loginFailure(err: unknown): LoginErrors {
  if (!isApiError(err)) {
    return { form: '登录失败，请稍后重试' }
  }

  if (err.status === 422) {
    const fields = err.fields
    const errors: LoginErrors = {}
    if (fields.email) {
      errors.email = fields.email
    }
    if (fields.password) {
      errors.password = fields.password
    }
    if (errors.email || errors.password) {
      return errors
    }
  }

  if (err.status === 401) {
    return { password: err.message }
  }

  return { form: err.message }
}
