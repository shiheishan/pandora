import { useEffect, useState, type FormEvent } from 'react'
import { z } from 'zod'
import { isApiError, type ApiClient } from '../core/api'
import type { TokenStore } from '../core/token'
import { useRuntime } from '../shell/runtime'
import { Button, Input } from '../ui'
import { useCaptcha } from './Captcha'
import captchaCss from './Captcha.module.css'
import css from './AuthPage.module.css'
import { useAppearance, useSignupConfig } from './queries'

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
  const appearance = useAppearance()
  const siteName = appearance.data?.site_name?.trim() || 'Pandora'
  const cap = useCaptcha(signup.data?.captcha ?? null)
  const [email, setEmail] = useState('')
  const [error, setError] = useState<string | null>(initialError)
  const [captchaError, setCaptchaError] = useState<string | null>(null)
  const [sent, setSent] = useState(false)
  const [cool, setCool] = useState(0)
  const [busy, setBusy] = useState(false)

  const cooling = cool > 0
  useEffect(() => {
    if (!cooling) return
    const id = window.setInterval(() => {
      setCool((n) => (n <= 1 ? 0 : n - 1))
    }, 1000)
    return () => window.clearInterval(id)
  }, [cooling])

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email.trim())) {
      if (!sent) return setError('请输入有效邮箱')
      return
    }
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
      await api.post('v1/auth/mail-link/start', startSchema, { auth: false, body })
      setSent(true)
      setCool(60)
    } catch (err) {
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
      {!sent ? (
        <>
          <p className={css.muted}>不用输入密码：我们把登录链接发到你的邮箱，点开就能登录，15 分钟内有效。</p>
          <Input label="邮箱" type="email" autoComplete="email" value={email} onChange={(e) => setEmail(e.target.value)} error={error ?? undefined} />
        </>
      ) : (
        <div className={css.sent} role="status">
          <b className={css.sentTitle}>登录链接已发送</b>
          <span>
            如果 {email.trim()} 已注册，去它的收件箱找一封来自 {siteName} 的邮件，点里面的按钮登录。没收到的话看看垃圾邮件。
          </span>
        </div>
      )}
      {cap.element}
      {captchaError && (
        <p className={captchaCss.error} role="alert">
          {captchaError}
        </p>
      )}
      {sent && error && (
        <p className={captchaCss.error} role="alert">
          {error}
        </p>
      )}
      <Button type="submit" variant={sent ? undefined : 'primary'} block busy={busy} disabled={sent && cool > 0}>
        {sent ? (cool > 0 ? `${cool} 秒后可以重发` : '重新发送') : '发送登录链接'}
      </Button>
    </form>
  )
}
