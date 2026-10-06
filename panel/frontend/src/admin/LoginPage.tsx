import { useEffect, useState, type FormEvent } from 'react'
import { z } from 'zod'
import { isApiError } from '../core/api'
import { Logo } from '../shell/Logo'
import { useRuntime } from '../shell/runtime'
import { Button, Input } from '../ui'
import css from './LoginPage.module.css'

const loginSchema = z.object({
  access_token: z.string().min(1),
  token_type: z.literal('Bearer'),
  expires_in: z.number(),
  user_id: z.string(),
  permissions: z.array(z.string()).nullable(),
})

export function LoginPage() {
  const { api, tokens } = useRuntime()
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    document.title = '登录 · Pandora 控制台'
  }, [])

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!email.trim() || !password) {
      setError('请输入邮箱和密码')
      return
    }
    setBusy(true)
    setError(null)
    try {
      const out = await api.post('v1/auth/login', loginSchema, { auth: false, body: { email: email.trim(), password } })
      tokens.set(out.access_token)
    } catch (err) {
      setError(isApiError(err) ? err.message : '登录失败，请稍后重试')
      setBusy(false)
    }
  }

  return (
    <div className={css.page}>
      <aside className={css.brand}>
        <Logo className={css.logo} />
        <div className={css.pitch}>
          <div className={css.headline}>
            运营、账务与节点网络，
            <br />
            在同一张桌面上完成。
          </div>
          <div className={css.release}>管理控制台 · {__APP_RELEASE__}</div>
        </div>
        <div className={css.footnote}>aegis-admin · 敏感操作需二次认证</div>
      </aside>
      <main className={css.formSide}>
        <form className={css.form} onSubmit={submit} noValidate>
          <div>
            <h1 className={css.title}>登录控制台</h1>
            <p className={css.subtitle}>使用管理员账号登录</p>
          </div>
          <Input label="邮箱" type="email" autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} autoFocus />
          <Input
            label="密码"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => {
              setPassword(e.target.value)
              setError(null)
            }}
            error={error ?? undefined}
          />
          <Button type="submit" variant="primary" block busy={busy} className={css.submit}>
            登录
          </Button>
        </form>
      </main>
    </div>
  )
}
