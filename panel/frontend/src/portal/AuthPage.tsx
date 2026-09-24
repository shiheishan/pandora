/**
 * [INPUT]: 依赖 react 的 state / FormEvent，依赖 zod，依赖 ../core/api 的 isApiError / ApiClient，依赖 ../shell/runtime 的 useRuntime，依赖 ../shell/Logo，依赖 ../ui 的 Button / Input / Segmented / useToast，依赖 ./queries 的 useSiteConfig / useAppearance，依赖 ./entry-links，依赖 ./AuthPage.module.css
 * [OUTPUT]: 对外提供 AuthPage、AuthTab 与 loginWithPassword
 * [POS]: portal 未登录时的整页（用户门户.dc.html showAuth）：登录 / 两步注册 / 快捷登录三个标签；注册按 site-config 的 registration_mode 决定邀请码必填、选填或整个隐藏，完成后自动登录；快捷登录只接受已登录设备生成的链接（保留规则 1）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState, type FormEvent } from 'react'
import { z } from 'zod'
import { isApiError, type ApiClient } from '../core/api'
import type { TokenStore } from '../core/token'
import { Logo } from '../shell/Logo'
import { useRuntime } from '../shell/runtime'
import { Button, Input, Segmented, useToast } from '../ui'
import css from './AuthPage.module.css'
import { quickLoginTokenFromInput, readStoredInvite } from './entry-links'
import { useAppearance, useSiteConfig } from './queries'

export type AuthTab = 'login' | 'reg' | 'quick'

const tokenSchema = z.object({ access_token: z.string().min(1) })
const registerStartSchema = z.object({
  registration_token: z.string(),
  verification_required: z.boolean(),
  dev_code: z.string().optional(),
})
const registerCompleteSchema = z.object({ user_id: z.string(), email: z.string() })

const message = (err: unknown, fallback: string) => (isApiError(err) ? err.message : fallback)

/** 邮箱密码登录并写令牌；注册完成后也走它（complete 不签发令牌）。 */
export async function loginWithPassword(api: ApiClient, tokens: TokenStore, email: string, password: string): Promise<void> {
  const out = await api.post('v1/auth/login', tokenSchema, { auth: false, body: { email, password } })
  tokens.set(out.access_token)
}

function passwordProblem(value: string): string | null {
  if (value.length < 8) return '密码至少 8 位'
  if (!/[a-z]/i.test(value) || !/\d/.test(value)) return '密码必须同时包含字母和数字'
  return null
}

export function AuthPage({ initialTab = 'login', invite, notice }: { initialTab?: AuthTab; invite?: string | null; notice?: string | null }) {
  const site = useSiteConfig()
  const appearance = useAppearance()
  const mode = site.data?.registration_mode
  const [tab, setTab] = useState<AuthTab>(initialTab)
  const current: AuthTab = tab === 'reg' && mode === 'closed' ? 'login' : tab
  const loginNotice = appearance.data?.slots['portal.login.notice']

  const options = [
    { value: 'login' as const, label: '登录' },
    ...(mode === 'closed' ? [] : [{ value: 'reg' as const, label: '注册' }]),
    { value: 'quick' as const, label: '快捷登录' },
  ]

  return (
    <div className={css.page}>
      <div className={css.column}>
        <Logo size={26} className={css.logo} />
        {loginNotice && (
          // 插槽 HTML 由服务端按白名单净化（契约 GET v1/appearance）
          <div className={css.notice} dangerouslySetInnerHTML={{ __html: loginNotice }} />
        )}
        <div className={css.card}>
          <Segmented label="登录方式" options={options} value={current} onChange={setTab} className={css.switch} />
          {current === 'login' && <LoginForm />}
          {current === 'reg' && <RegisterForm inviteRequired={mode !== 'open'} invite={invite ?? readStoredInvite()} emailVerification={site.data?.email_verification ?? true} />}
          {current === 'quick' && <QuickLoginForm initialError={notice ?? null} />}
        </div>
        <div className={css.footer}>
          {mode === 'closed' ? 'Pandora · 注册已关闭' : mode === 'open' ? 'Pandora · 注册已开放' : mode === 'invite_only' ? 'Pandora · 注册已开放 · 需要邀请码' : 'Pandora'}
        </div>
      </div>
    </div>
  )
}

function LoginForm() {
  const { api, tokens } = useRuntime()
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!email.trim() || !password) return setError('请输入邮箱和密码')
    setBusy(true)
    setError(null)
    try {
      await loginWithPassword(api, tokens, email.trim(), password)
    } catch (err) {
      setError(message(err, '登录失败，请稍后重试'))
      setBusy(false)
    }
  }

  return (
    <form className={css.form} onSubmit={submit} noValidate>
      <Input label="邮箱" type="email" autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} />
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
      <Button type="submit" variant="primary" block busy={busy}>
        登录
      </Button>
    </form>
  )
}

function RegisterForm({ inviteRequired, invite, emailVerification }: { inviteRequired: boolean; invite: string | null; emailVerification: boolean }) {
  const { api, tokens } = useRuntime()
  const toast = useToast()
  const [email, setEmail] = useState('')
  const [inviteCode, setInviteCode] = useState(invite ?? '')
  const [started, setStarted] = useState<z.output<typeof registerStartSchema> | null>(null)
  const [code, setCode] = useState('')
  const [password, setPassword] = useState('')
  const [errors, setErrors] = useState<{ email?: string; invite?: string; code?: string; password?: string }>({})
  const [busy, setBusy] = useState(false)
  // site-config 说不验证时预先隐藏验证码框，最终以 register/start 的 verification_required 为准
  const needsCode = started ? started.verification_required : emailVerification

  const start = async (event: FormEvent) => {
    event.preventDefault()
    const next: typeof errors = {}
    if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email.trim())) next.email = '请输入有效邮箱'
    if (inviteRequired && !inviteCode.trim()) next.invite = '请输入邀请码'
    if (next.email || next.invite) return setErrors(next)
    setBusy(true)
    setErrors({})
    try {
      const body = inviteCode.trim() ? { email: email.trim(), invite_code: inviteCode.trim().toUpperCase() } : { email: email.trim() }
      const out = await api.post('v1/auth/register/start', registerStartSchema, { auth: false, body })
      setStarted(out)
      if (out.verification_required) toast('验证码已发送')
    } catch (err) {
      if (isApiError(err) && err.fields.email) setErrors({ email: err.fields.email })
      else setErrors({ invite: message(err, '发送失败，请稍后重试') })
    } finally {
      setBusy(false)
    }
  }

  const complete = async (event: FormEvent) => {
    event.preventDefault()
    if (!started) return
    const next: typeof errors = {}
    if (needsCode && !/^\d{6}$/.test(code)) next.code = '验证码为 6 位数字'
    const problem = passwordProblem(password)
    if (problem) next.password = problem
    if (next.code || next.password) return setErrors(next)
    setBusy(true)
    setErrors({})
    try {
      const out = await api.post('v1/auth/register/complete', registerCompleteSchema, {
        auth: false,
        body: { registration_token: started.registration_token, code: needsCode ? code : '', password },
      })
      await loginWithPassword(api, tokens, out.email, password)
      toast('注册成功，欢迎使用')
    } catch (err) {
      setBusy(false)
      if (isApiError(err) && err.fields.password) return setErrors({ password: err.fields.password })
      // 403 = 令牌过期、验证码错或尝试过多（契约统一成一条文案）
      setErrors({ code: isApiError(err) && err.status === 403 ? '验证码错误或已过期' : message(err, '注册失败，请稍后重试') })
    }
  }

  return (
    <>
      <div className={css.steps}>
        <span className={started ? css.stepDone : css.stepOn}>1 邮箱与邀请码</span>
        <span aria-hidden="true">—</span>
        <span className={started ? css.stepOn : css.stepDone}>2 验证与密码</span>
      </div>
      {!started ? (
        <form className={css.form} onSubmit={start} noValidate>
          <Input label="邮箱" type="email" autoComplete="email" value={email} onChange={(e) => setEmail(e.target.value)} error={errors.email} />
          <Input
            label={
              <>
                邀请码 <span className={css.labelNote}>{inviteRequired ? '（必填，可向好友索取）' : '（选填）'}</span>
              </>
            }
            mono
            className={css.upper}
            value={inviteCode}
            onChange={(e) => setInviteCode(e.target.value)}
            error={errors.invite}
          />
          <Button type="submit" variant="primary" block busy={busy}>
            {needsCode ? '发送验证码' : '下一步'}
          </Button>
        </form>
      ) : (
        <form className={css.form} onSubmit={complete} noValidate>
          {needsCode && (
            <>
              <p className={css.muted}>验证码已发送至 {email.trim()}</p>
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
            </>
          )}
          <Input
            label="设置密码"
            type="password"
            autoComplete="new-password"
            hint="至少 8 位，同时包含字母和数字"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            error={errors.password ?? (!needsCode ? errors.code : undefined)}
          />
          <div className={css.row}>
            <Button onClick={() => setStarted(null)} disabled={busy}>
              上一步
            </Button>
            <Button type="submit" variant="primary" busy={busy} className={css.grow}>
              完成注册
            </Button>
          </div>
        </form>
      )}
    </>
  )
}

function QuickLoginForm({ initialError }: { initialError: string | null }) {
  const { api, tokens } = useRuntime()
  const [value, setValue] = useState('')
  const [error, setError] = useState<string | null>(initialError)
  const [busy, setBusy] = useState(false)

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    const token = quickLoginTokenFromInput(value)
    if (!token) return setError('请粘贴完整的快捷登录链接或令牌')
    setBusy(true)
    setError(null)
    try {
      await consumeQuickLogin(api, tokens, token)
    } catch (err) {
      setError(message(err, '登录失败，请稍后重试'))
      setBusy(false)
    }
  }

  return (
    <form className={css.form} onSubmit={submit} noValidate>
      <p className={css.muted}>在已登录的设备上打开「账号安全 → 快捷登录」生成链接，60 秒内在本设备打开，或把链接粘贴到下面。</p>
      <Input label="快捷登录链接" placeholder="粘贴链接或令牌" value={value} onChange={(e) => setValue(e.target.value)} error={error ?? undefined} />
      <Button type="submit" variant="primary" block busy={busy}>
        登录
      </Button>
    </form>
  )
}

/** POST v1/auth/quick-login：令牌一次性、60 秒有效；响应不含 token_type。 */
export async function consumeQuickLogin(api: ApiClient, tokens: TokenStore, token: string): Promise<void> {
  const out = await api.post('v1/auth/quick-login', tokenSchema, { auth: false, body: { token } })
  tokens.set(out.access_token)
}
