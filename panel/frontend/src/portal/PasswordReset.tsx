import { useState, type FormEvent } from 'react'
import { z } from 'zod'
import { isApiError } from '../core/api'
import { useRuntime } from '../shell/runtime'
import { Button, Input, useToast } from '../ui'
import css from './AuthPage.module.css'

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
export function resetPasswordProblem(value: string): string | null {
  if (value.length < 8) return '密码至少 8 位'
  if (!/[a-z]/i.test(value) || !/\d/.test(value)) return '密码必须同时包含字母和数字'
  return null
}

export function PasswordResetForm({ onDone, onCancel }: { onDone: (email: string) => void; onCancel: () => void }) {
  const { api } = useRuntime()
  const toast = useToast()
  const [email, setEmail] = useState('')
  const [started, setStarted] = useState<z.output<typeof startSchema> | null>(null)
  const [code, setCode] = useState('')
  const [password, setPassword] = useState('')
  const [errors, setErrors] = useState<{
    email?: string
    code?: string
    password?: string
  }>({})
  const [busy, setBusy] = useState(false)

  const start = async (event: FormEvent) => {
    event.preventDefault()
    if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email.trim())) return setErrors({ email: '请输入有效邮箱' })
    setBusy(true)
    setErrors({})
    try {
      const out = await api.post('v1/auth/password-reset/start', startSchema, {
        auth: false,
        body: { email: email.trim() },
      })
      setStarted(out)
      toast(out.message)
    } catch (err) {
      setErrors({
        email: isApiError(err) && err.fields.email ? err.fields.email : message(err, '发送失败，请稍后重试'),
      })
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
      toast('密码已重置，所有设备上的登录都已退出，请用新密码登录')
      onDone(email.trim())
    } catch (err) {
      setBusy(false)
      if (isApiError(err) && err.fields.password) return setErrors({ password: err.fields.password })
      setErrors({
        code: isApiError(err) && err.fields.code ? err.fields.code : message(err, '重置失败，请稍后重试'),
      })
    }
  }

  return (
    <>
      <div className={css.steps}>
        <span className={started ? css.stepDone : css.stepOn}>1 填写邮箱</span>
        <span aria-hidden="true">—</span>
        <span className={started ? css.stepOn : css.stepDone}>2 验证码与新密码</span>
      </div>
      {!started ? (
        <form className={css.form} onSubmit={start} noValidate>
          <p className={css.muted}>填写注册时用的邮箱，我们会往这个邮箱发一个 6 位验证码。</p>
          <Input label="邮箱" type="email" autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} error={errors.email} />
          <div className={css.row}>
            <Button onClick={onCancel} disabled={busy}>
              返回登录
            </Button>
            <Button type="submit" variant="primary" busy={busy} className={css.grow}>
              发送验证码
            </Button>
          </div>
        </form>
      ) : (
        <form className={css.form} onSubmit={complete} noValidate>
          <p className={css.muted}>若 {email.trim()} 已注册，验证码已发送，10 分钟内有效。重置后这个账号在所有设备上的登录都会退出。</p>
          <Input
            label="验证码"
            mono
            inputMode="numeric"
            autoComplete="one-time-code"
            placeholder="6 位数字"
            className={css.code}
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
          <div className={css.row}>
            <Button onClick={() => setStarted(null)} disabled={busy}>
              上一步
            </Button>
            <Button type="submit" variant="primary" busy={busy} className={css.grow}>
              重置密码
            </Button>
          </div>
        </form>
      )}
    </>
  )
}
