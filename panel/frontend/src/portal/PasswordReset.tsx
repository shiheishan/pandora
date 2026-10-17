import { useState, type FormEvent } from 'react'
import { z } from 'zod'
import { isApiError } from '../core/api'
import { useRuntime } from '../shell/runtime'
import { Input } from '../ui'
import { useCaptcha } from './Captcha'
import captchaCss from './Captcha.module.css'
import css from './AuthPage.module.css'
import { Btn, Icon } from './kit'
import { useSignupConfig } from './queries'

// ---------------------------------------------------------------------------
// 找回密码（契约 POST v1/auth/password-reset/start、/complete；用户 2026-10-07 定：邮件验证码，
// 重置后吊销该账号的全部登录；没配邮件服务时 site-config.password_reset=false，入口不显示）。
// 第 1 步不论邮箱存不存在都回同一句话，不能借它探测邮箱（IAM-006）
// ---------------------------------------------------------------------------
const startSchema = z.object({
  expires_at: z.string(),
  message: z.string(),
  dev_code: z.string().optional(),
})
const completeSchema = z.object({ ok: z.literal(true) })

const message = (err: unknown, fallback: string) => (isApiError(err) ? err.message : fallback)

/** 与注册同一套本地预检：至少 8 位、同时有字母和数字（后端仍是最终裁判） */
function resetPasswordProblem(value: string): string | null {
  if (value.length < 8) return '密码至少 8 位'
  if (!/[a-z]/i.test(value) || !/\d/.test(value)) return '密码必须同时包含字母和数字'
  return null
}

export function PasswordResetForm({ onDone, onCancel }: { onDone: (email: string) => void; onCancel: () => void }) {
  const { api } = useRuntime()
  const signup = useSignupConfig()
  const cap = useCaptcha(signup.data?.captcha ?? null)
  const [email, setEmail] = useState('')
  const [started, setStarted] = useState<z.output<typeof startSchema> | null>(null)
  const [code, setCode] = useState('')
  const [password, setPassword] = useState('')
  const [errors, setErrors] = useState<{
    email?: string
    code?: string
    password?: string
    captcha?: string
  }>({})
  const [busy, setBusy] = useState(false)

  const start = async (event: FormEvent) => {
    event.preventDefault()
    if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email.trim())) return setErrors({ email: '请输入有效邮箱' })
    setBusy(true)
    setErrors({})
    // 没开人机验证时拿到空串；拿不到（没完成、加载失败）时显示原因并停在这一步。
    const captchaToken = await cap.getToken('password_reset').catch((e: unknown) => {
      setBusy(false)
      setErrors({ captcha: e instanceof Error ? e.message : String(e) })
      return null
    })
    if (captchaToken === null) return
    try {
      const body: { email: string; captcha_token?: string } = { email: email.trim() }
      if (captchaToken) body.captcha_token = captchaToken
      const out = await api.post('v1/auth/password-reset/start', startSchema, {
        auth: false,
        body,
      })
      setStarted(out)
    } catch (err) {
      cap.reset()
      const nextErr: typeof errors = {}
      if (isApiError(err) && err.fields.captcha) nextErr.captcha = err.fields.captcha
      if (isApiError(err) && err.fields.email) nextErr.email = err.fields.email
      else if (!nextErr.captcha) nextErr.email = message(err, '发送失败，请稍后重试')
      setErrors(nextErr)
    } finally {
      setBusy(false)
    }
  }

  const complete = async (event: FormEvent) => {
    event.preventDefault()
    const next: typeof errors = {}
    if (!/^\d{6}$/.test(code)) next.code = '验证码为 6 位数字'
    const problem = resetPasswordProblem(password)
    if (problem) next.password = problem
    if (next.code || next.password) return setErrors(next)
    setBusy(true)
    setErrors({})
    try {
      await api.post('v1/auth/password-reset/complete', completeSchema, {
        auth: false,
        body: { email: email.trim(), code, new_password: password },
      })
      onDone(email.trim())
    } catch (err) {
      setBusy(false)
      if (isApiError(err) && err.fields.password) return setErrors({ password: err.fields.password })
      setErrors({
        code: isApiError(err) && err.fields.code ? err.fields.code : message(err, '重设失败，请稍后重试'),
      })
    }
  }

  return (
    <>
      <button type="button" className={css.back} onClick={onCancel}>
        <Icon name="chev" size={16} />
        返回登录
      </button>
      <h2 className={css.h}>找回密码</h2>
      {!started ? (
        <>
          <p className={css.p}>输入注册时用的邮箱，我们会发一个验证码过去。</p>
          <form className={css.form} onSubmit={start} noValidate>
            <Input
              label="邮箱"
              type="email"
              autoComplete="username"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              error={errors.email}
            />
            {cap.element}
            {errors.captcha && (
              <p className={captchaCss.error} role="alert">
                {errors.captcha}
              </p>
            )}
            <Btn type="submit" full busy={busy} className={css.submit}>
              发送验证码
            </Btn>
          </form>
        </>
      ) : (
        <>
          <p className={css.p}>
            如果 <b>{email.trim()}</b> 已注册，验证码已发送，10 分钟内有效。重设后这个账号在所有设备上的登录都会退出。
          </p>
          <form className={css.form} onSubmit={complete} noValidate>
            <Input
              label="验证码"
              inputMode="numeric"
              autoComplete="one-time-code"
              maxLength={6}
              placeholder="6 位数字"
              className={css.otp}
              value={code}
              onChange={(e) => setCode(e.target.value.replace(/\D/g, '').slice(0, 6))}
              error={errors.code}
              hint={started.dev_code ? `开发模式验证码：${started.dev_code}` : undefined}
            />
            <Input
              label="新密码"
              type="password"
              autoComplete="new-password"
              hint="至少 8 位，同时包含字母和数字"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              error={errors.password}
            />
            <Btn type="submit" full busy={busy} className={css.submit}>
              重设密码
            </Btn>
          </form>
        </>
      )}
    </>
  )
}
