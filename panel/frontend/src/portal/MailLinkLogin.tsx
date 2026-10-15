import { useState, type FormEvent } from 'react'
import { z } from 'zod'
import { isApiError, type ApiClient } from '../core/api'
import type { TokenStore } from '../core/token'
import { useRuntime } from '../shell/runtime'
import { Button, Input } from '../ui'
import { useCaptcha } from './Captcha'
import captchaCss from './Captcha.module.css'
import css from './AuthPage.module.css'
import { useSignupConfig } from './queries'

const startSchema = z.object({ ok: z.literal(true), message: z.string() })
const loginSchema = z.object({ access_token: z.string().min(1) })

const message = (err: unknown, fallback: string) => (isApiError(err) ? err.message : fallback)

/** POST v1/auth/mail-link/login：邮件里的 token 换访问令牌。 */
export async function consumeMailLogin(api: ApiClient, tokens: TokenStore, token: string): Promise<void> {
  const out = await api.post('v1/auth/mail-link/login', loginSchema, { auth: false, body: { token } })
  tokens.set(out.access_token)
}

/** 开了人机验证时，与找回密码第 1 步同口径：发信前先过人机验证（action 为 mail_login，与后端一致）。 */
export function MailLinkLoginForm({ initialError }: { initialError: string | null }) {
  const { api } = useRuntime()
  const signup = useSignupConfig()
  const cap = useCaptcha(signup.data?.captcha ?? null)
  const [email, setEmail] = useState('')
  const [error, setError] = useState<string | null>(initialError)
  const [captchaError, setCaptchaError] = useState<string | null>(null)
  const [successMessage, setSuccessMessage] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email.trim())) return setError('请输入有效邮箱')
    setBusy(true)
    setError(null)
    setCaptchaError(null)
    // 没开人机验证时拿到空串；拿不到（没完成、加载失败）时显示原因并停在这一步。
    const captchaToken = await cap.getToken('mail_login').catch((e: unknown) => {
      setBusy(false)
      setCaptchaError(e instanceof Error ? e.message : String(e))
      return null
    })
    if (captchaToken === null) return
    try {
      const body: { email: string; captcha_token?: string } = { email: email.trim() }
      if (captchaToken) body.captcha_token = captchaToken
      const out = await api.post('v1/auth/mail-link/start', startSchema, { auth: false, body })
      setSuccessMessage(out.message)
    } catch (err) {
      setSuccessMessage(null)
      if (isApiError(err) && err.fields.captcha) setCaptchaError(err.fields.captcha)
      else setError(message(err, '发送失败，请稍后重试'))
    } finally {
      // 令牌一次性：不论成败都换一个，重新发送时要重新过人机验证
      cap.reset()
      setBusy(false)
    }
  }

  return (
    <form className={css.form} onSubmit={submit} noValidate>
      <p className={css.muted}>输入注册邮箱，我们会发一封带登录链接的邮件，15 分钟内点开即可登录，不用输入密码。</p>
      <Input label="邮箱" type="email" autoComplete="email" value={email} onChange={(e) => setEmail(e.target.value)} error={error ?? undefined} />
      {cap.element}
      {captchaError && (
        <p className={captchaCss.error} role="alert">
          {captchaError}
        </p>
      )}
      {successMessage && <p className={css.muted}>{successMessage}</p>}
      <Button type="submit" variant="primary" block busy={busy}>
        {successMessage ? '重新发送' : '发送登录链接'}
      </Button>
    </form>
  )
}
