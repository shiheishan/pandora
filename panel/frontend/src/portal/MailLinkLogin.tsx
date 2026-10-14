import { useState, type FormEvent } from 'react'
import { z } from 'zod'
import { isApiError, type ApiClient } from '../core/api'
import type { TokenStore } from '../core/token'
import { useRuntime } from '../shell/runtime'
import { Button, Input } from '../ui'
import css from './AuthPage.module.css'

const startSchema = z.object({ ok: z.literal(true), message: z.string() })
const loginSchema = z.object({ access_token: z.string().min(1) })

const message = (err: unknown, fallback: string) => (isApiError(err) ? err.message : fallback)

/** POST v1/auth/mail-link/login：邮件里的 token 换访问令牌。 */
export async function consumeMailLogin(api: ApiClient, tokens: TokenStore, token: string): Promise<void> {
  const out = await api.post('v1/auth/mail-link/login', loginSchema, { auth: false, body: { token } })
  tokens.set(out.access_token)
}

export function MailLinkLoginForm({ initialError }: { initialError: string | null }) {
  const { api } = useRuntime()
  const [email, setEmail] = useState('')
  const [error, setError] = useState<string | null>(initialError)
  const [successMessage, setSuccessMessage] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email.trim())) return setError('请输入有效邮箱')
    setBusy(true)
    setError(null)
    try {
      const out = await api.post('v1/auth/mail-link/start', startSchema, { auth: false, body: { email: email.trim() } })
      setSuccessMessage(out.message)
    } catch (err) {
      setSuccessMessage(null)
      setError(message(err, '发送失败，请稍后重试'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form className={css.form} onSubmit={submit} noValidate>
      <p className={css.muted}>输入注册邮箱，我们会发一封带登录链接的邮件，15 分钟内点开即可登录，不用输入密码。</p>
      <Input label="邮箱" type="email" autoComplete="email" value={email} onChange={(e) => setEmail(e.target.value)} error={error ?? undefined} />
      {successMessage && <p className={css.muted}>{successMessage}</p>}
      <Button type="submit" variant="primary" block busy={busy}>
        {successMessage ? '重新发送' : '发送登录链接'}
      </Button>
    </form>
  )
}
