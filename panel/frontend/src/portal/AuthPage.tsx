import { useState, type FormEvent, type ReactNode } from 'react'
import { z } from 'zod'
import { isApiError, type ApiClient } from '../core/api'
import type { TokenStore } from '../core/token'
import { useRuntime } from '../shell/runtime'
import { Input, Segmented, useToast } from '../ui'
import css from './AuthPage.module.css'
import { quickLoginTokenFromInput, readStoredInvite } from './entry-links'
import { type CaptchaConfig, useCaptcha } from './Captcha'
import captchaCss from './Captcha.module.css'
import { MailLinkLoginForm } from './MailLinkLogin'
import { PasswordResetForm } from './PasswordReset'
import { useAppearance, useSignupConfig, useSiteConfig } from './queries'
import { SiteBrand } from './SiteBrand'
import { Btn, Icon } from './kit'

type View = 'login' | 'reg' | 'forgot' | 'mail' | 'quick'

const tokenSchema = z.object({ access_token: z.string().min(1) })
const registerStartSchema = z.object({
  registration_token: z.string(),
  verification_required: z.boolean(),
  dev_code: z.string().optional(),
})
const registerCompleteSchema = z.object({ user_id: z.string(), email: z.string() })

const message = (err: unknown, fallback: string) => (isApiError(err) ? err.message : fallback)

/** 邮箱密码登录并写令牌；注册完成后也走它（complete 不签发令牌）。 */
async function loginWithPassword(api: ApiClient, tokens: TokenStore, email: string, password: string): Promise<void> {
  const out = await api.post('v1/auth/login', tokenSchema, { auth: false, body: { email, password } })
  tokens.set(out.access_token)
}

function passwordProblem(value: string): string | null {
  if (value.length < 8) return '密码至少 8 位'
  if (!/[a-z]/i.test(value) || !/\d/.test(value)) return '密码必须同时包含字母和数字'
  return null
}

export function AuthPage({
  initialTab = 'login',
  invite,
  notice,
}: {
  initialTab?: 'login' | 'reg' | 'mail' | 'quick'
  invite?: string | null
  notice?: string | null
}) {
  const site = useSiteConfig()
  const signup = useSignupConfig()
  const appearance = useAppearance()
  const mode = site.data?.registration_mode ?? 'open'
  const [picked, setView] = useState<View>(initialTab)
  // 关闭注册时整个注册页签藏掉（样稿 ?reg=closed），带邀请码打开的也落到登录
  const regOpen = mode !== 'closed'
  const view: View = picked === 'reg' && !regOpen ? 'login' : picked
  const [resetDone, setResetDone] = useState<string | null>(null)
  const canReset = site.data?.password_reset === true
  const mailOn = signup.data?.mail_link_login === true || initialTab === 'mail'
  const loginNotice = appearance.data?.slots['portal.login.notice']
  const siteName = appearance.data?.site_name?.trim() || 'Pandora'

  const goView = (next: View) => {
    if (next !== 'login') setResetDone(null)
    setView(next)
  }

  const footText =
    mode === 'closed'
      ? `${siteName} · 注册已关闭`
      : mode === 'open'
        ? `${siteName} · 注册已开放`
        : mode === 'invite_only'
          ? `${siteName} · 注册已开放 · 需要邀请码`
          : siteName

  return (
    <div className={css.page}>
      <div className={css.au}>
        <div className={css.brand}>
          <SiteBrand size={30} withTagline />
        </div>
        {loginNotice && (
          <div className={css.notice}>
            <span className={css.noticeTag}>公告</span>
            <div className={css.noticeBody} dangerouslySetInnerHTML={{ __html: loginNotice }} />
          </div>
        )}
        <div className={css.card}>
          {regOpen && (view === 'login' || view === 'reg') && (
            <Segmented
              label="登录或注册"
              options={[{ value: 'login' as const, label: '登录' }, { value: 'reg' as const, label: '注册' }]}
              value={view === 'reg' ? 'reg' : 'login'}
              onChange={(v) => goView(v)}
              className={css.tabs}
            />
          )}
          {view === 'login' && (
            <>
              {resetDone && (
                <p className={css.ok} role="status">
                  密码已重设，所有设备上的登录都已退出，请用新密码登录。
                </p>
              )}
              <LoginForm
                key={resetDone ?? 'login'}
                initialEmail={resetDone ?? ''}
                canReset={canReset}
                mailOn={mailOn}
                onForgot={() => goView('forgot')}
                onMail={() => goView('mail')}
                onQuick={() => goView('quick')}
              />
            </>
          )}
          {view === 'reg' && (
            <RegisterForm
              mode={mode === 'invite_only' ? 'invite_only' : 'open'}
              invite={invite ?? readStoredInvite()}
              emailVerification={site.data?.email_verification ?? true}
              captcha={signup.data?.captcha ?? null}
              emailSuffixes={signup.data?.email_suffixes ?? []}
            />
          )}
          {view === 'forgot' && canReset && (
            <PasswordResetForm
              onCancel={() => goView('login')}
              onDone={(email) => {
                setResetDone(email)
                setView('login')
              }}
            />
          )}
          {view === 'mail' && (
            <MailLinkLoginForm
              initialError={initialTab === 'mail' ? (notice ?? null) : null}
              onBack={() => goView('login')}
            />
          )}
          {view === 'quick' && (
            <QuickLoginForm initialError={initialTab === 'quick' ? (notice ?? null) : null} onBack={() => goView('login')} />
          )}
        </div>
        <div className={css.foot}>{footText}</div>
      </div>
    </div>
  )
}

function LoginForm({
  initialEmail = '',
  canReset,
  mailOn,
  onForgot,
  onMail,
  onQuick,
}: {
  initialEmail?: string
  canReset: boolean
  mailOn: boolean
  onForgot: () => void
  onMail: () => void
  onQuick: () => void
}) {
  const { api, tokens } = useRuntime()
  const [email, setEmail] = useState(initialEmail)
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
    <>
      <form className={css.form} onSubmit={submit} noValidate>
        <Input label="邮箱" type="email" autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} />
        <div className={css.pwField}>
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
          {canReset && (
            <button type="button" className={css.forgot} onClick={onForgot}>
              忘记密码？
            </button>
          )}
        </div>
        <Btn type="submit" full busy={busy} className={css.submit}>
          登录
        </Btn>
      </form>
      {/* 次级登录方式放在登录按钮下面，各自是带返回的子页 */}
      <div className={css.or}>也可以</div>
      <div className={css.alt}>
        {mailOn && (
          <Btn tone="soft" full className={css.altBtn} onClick={onMail}>
            <Icon name="mail" size={18} />
            用邮件里的链接登录
            <small>不用密码</small>
          </Btn>
        )}
        <Btn tone="soft" full className={css.altBtn} onClick={onQuick}>
          <Icon name="phone" size={18} />
          用已登录的设备登录
          <small>扫码或粘贴链接</small>
        </Btn>
      </div>
    </>
  )
}

function RegisterSteps({ step }: { step: 1 | 2 }) {
  return (
    <ol className={css.steps}>
      <li className={step === 1 ? css.stepOn : css.stepDone}>
        <b>{step === 2 ? '✓' : '1'}</b>
        邮箱与邀请码
      </li>
      <li aria-hidden="true" className={css.stepLine} />
      <li className={step === 2 ? css.stepOn : undefined}>
        <b>2</b>
        验证与密码
      </li>
    </ol>
  )
}

function RegisterForm({
  mode,
  invite,
  emailVerification,
  captcha,
  emailSuffixes,
}: {
  mode: 'open' | 'invite_only'
  invite: string | null
  emailVerification: boolean
  captcha: CaptchaConfig | null
  emailSuffixes: string[]
}) {
  const { api, tokens } = useRuntime()
  const toast = useToast()
  const cap = useCaptcha(captcha)
  const inviteRequired = mode === 'invite_only'
  const [email, setEmail] = useState('')
  const [inviteCode, setInviteCode] = useState(invite ?? '')
  const [started, setStarted] = useState<z.output<typeof registerStartSchema> | null>(null)
  const [code, setCode] = useState('')
  const [password, setPassword] = useState('')
  const [errors, setErrors] = useState<{ email?: string; invite?: string; code?: string; password?: string; captcha?: string }>({})
  const [formError, setFormError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const needsCode = started ? started.verification_required : emailVerification
  const showSteps = emailVerification || (started?.verification_required ?? false)

  const emailHint =
    emailSuffixes.length > 0
      ? `可用邮箱：${emailSuffixes.slice(0, 6).join('、')}${emailSuffixes.length > 6 ? '等' : ''}`
      : undefined

  const inviteLabel: ReactNode = (
    <>
      邀请码
      <small className={css.labelNote}>{inviteRequired ? '必填，可向好友要' : '选填'}</small>
    </>
  )

  const mapStartError = (err: unknown, hadInvite: boolean) => {
    const nextErr: typeof errors = {}
    let nextForm: string | null = null
    if (isApiError(err) && err.fields.captcha) nextErr.captcha = err.fields.captcha
    // 一步注册时 complete 的密码规则错误也走这里
    if (isApiError(err) && err.fields.password) nextErr.password = err.fields.password
    if (isApiError(err) && err.fields.email) nextErr.email = err.fields.email
    else if (isApiError(err) && err.status === 403 && hadInvite) {
      nextErr.invite = mode === 'open' ? '邀请码无效，清空后也可以直接注册' : message(err, '发送失败，请稍后重试')
    } else if (!nextErr.captcha && !nextErr.password && !nextErr.email && !nextErr.invite) {
      nextForm = message(err, '发送失败，请稍后重试')
    }
    return { nextErr, nextForm }
  }

  const start = async (event: FormEvent) => {
    event.preventDefault()
    const next: typeof errors = {}
    if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email.trim())) next.email = '请输入有效邮箱'
    if (inviteRequired && !inviteCode.trim()) next.invite = '请输入邀请码'
    if (next.email || next.invite) return setErrors(next)
    setBusy(true)
    setErrors({})
    setFormError(null)
    const captchaToken = await cap.getToken('register').catch((e: unknown) => {
      setBusy(false)
      setErrors({ captcha: e instanceof Error ? e.message : String(e) })
      return null
    })
    if (captchaToken === null) return
    try {
      const body: { email: string; invite_code?: string; captcha_token?: string } = inviteCode.trim()
        ? { email: email.trim(), invite_code: inviteCode.trim().toUpperCase() }
        : { email: email.trim() }
      if (captchaToken) body.captcha_token = captchaToken
      const out = await api.post('v1/auth/register/start', registerStartSchema, { auth: false, body })
      setStarted(out)
      if (out.verification_required) toast('验证码已发送')
    } catch (err) {
      cap.reset()
      const { nextErr, nextForm } = mapStartError(err, Boolean(inviteCode.trim()))
      setErrors(nextErr)
      setFormError(nextForm)
    } finally {
      setBusy(false)
    }
  }

  const oneStepSubmit = async (event: FormEvent) => {
    event.preventDefault()
    const next: typeof errors = {}
    if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email.trim())) next.email = '请输入有效邮箱'
    if (inviteRequired && !inviteCode.trim()) next.invite = '请输入邀请码'
    const problem = passwordProblem(password)
    if (problem) next.password = problem
    if (next.email || next.invite || next.password) return setErrors(next)
    setBusy(true)
    setErrors({})
    setFormError(null)
    const captchaToken = await cap.getToken('register').catch((e: unknown) => {
      setBusy(false)
      setErrors({ captcha: e instanceof Error ? e.message : String(e) })
      return null
    })
    if (captchaToken === null) return
    try {
      const body: { email: string; invite_code?: string; captcha_token?: string } = inviteCode.trim()
        ? { email: email.trim(), invite_code: inviteCode.trim().toUpperCase() }
        : { email: email.trim() }
      if (captchaToken) body.captcha_token = captchaToken
      const out = await api.post('v1/auth/register/start', registerStartSchema, { auth: false, body })
      if (out.verification_required) {
        setStarted(out)
        toast('验证码已发送')
        setBusy(false)
        return
      }
      const done = await api.post('v1/auth/register/complete', registerCompleteSchema, {
        auth: false,
        body: { registration_token: out.registration_token, code: '', password },
      })
      await loginWithPassword(api, tokens, done.email, password)
      toast('注册成功，欢迎使用')
    } catch (err) {
      cap.reset()
      const { nextErr, nextForm } = mapStartError(err, Boolean(inviteCode.trim()))
      setErrors(nextErr)
      setFormError(nextForm)
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
    setFormError(null)
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
      setErrors({ code: isApiError(err) && err.status === 403 ? '验证码错误或已过期' : message(err, '注册失败，请稍后重试') })
    }
  }

  if (started) {
    return (
      <>
        {showSteps && <RegisterSteps step={2} />}
        <form className={css.form} onSubmit={complete} noValidate>
          {needsCode && (
            <>
              <p className={css.sentBox}>
                <span>
                  验证码已发送至 <b>{email.trim()}</b>
                </span>
                <button type="button" onClick={() => setStarted(null)}>
                  改邮箱
                </button>
              </p>
              <Input
                label="验证码"
                mono
                inputMode="numeric"
                autoComplete="one-time-code"
                placeholder="6 位数字"
                maxLength={6}
                className={css.otp}
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
            error={errors.password}
          />
          {formError && (
            <p className={css.formError} role="alert">
              {formError}
            </p>
          )}
          <div className={css.acts}>
            <Btn tone="soft" onClick={() => setStarted(null)} disabled={busy}>
              上一步
            </Btn>
            <Btn type="submit" grow busy={busy}>
              完成注册
            </Btn>
          </div>
        </form>
      </>
    )
  }

  if (!emailVerification) {
    return (
      <form className={css.form} onSubmit={oneStepSubmit} noValidate>
        <Input
          label="邮箱"
          type="email"
          autoComplete="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          error={errors.email}
          hint={emailHint}
        />
        <Input
          label={inviteLabel}
          mono
          className={css.upper}
          value={inviteCode}
          onChange={(e) => setInviteCode(e.target.value)}
          error={errors.invite}
        />
        <Input
          label="设置密码"
          type="password"
          autoComplete="new-password"
          hint="至少 8 位，同时包含字母和数字"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          error={errors.password}
        />
        {cap.element}
        {errors.captcha && (
          <p className={captchaCss.error} role="alert">
            {errors.captcha}
          </p>
        )}
        {formError && (
          <p className={css.formError} role="alert">
            {formError}
          </p>
        )}
        <Btn type="submit" full busy={busy} className={css.submit}>
          注册
        </Btn>
      </form>
    )
  }

  return (
    <>
      <RegisterSteps step={1} />
      <form className={css.form} onSubmit={start} noValidate>
        <Input
          label="邮箱"
          type="email"
          autoComplete="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          error={errors.email}
          hint={emailHint}
        />
        <Input
          label={inviteLabel}
          mono
          className={css.upper}
          value={inviteCode}
          onChange={(e) => setInviteCode(e.target.value)}
          error={errors.invite}
        />
        {cap.element}
        {errors.captcha && (
          <p className={captchaCss.error} role="alert">
            {errors.captcha}
          </p>
        )}
        {formError && (
          <p className={css.formError} role="alert">
            {formError}
          </p>
        )}
        <Btn type="submit" full busy={busy} className={css.submit}>
          发送验证码
        </Btn>
      </form>
    </>
  )
}

function QuickLoginForm({ initialError, onBack }: { initialError: string | null; onBack: () => void }) {
  const { api, tokens } = useRuntime()
  const [value, setValue] = useState('')
  const [error, setError] = useState<string | null>(initialError)
  const [busy, setBusy] = useState(false)

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    const token = quickLoginTokenFromInput(value)
    if (!token) return setError('请粘贴完整的登录链接')
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
    <>
      <button type="button" className={css.back} onClick={onBack}>
        <Icon name="chev" size={16} />
        返回
      </button>
      <h2 className={css.h}>用已登录的设备登录</h2>
      <ol className={css.how}>
        <li>
          <span>
            在已经登录的手机或电脑上，打开<b>账号安全 → 在新设备上登录</b>，会出现一个二维码。
          </span>
        </li>
        <li>
          <span>用这台设备的相机扫码，直接就登录了。扫不了的话，把那边复制的链接粘贴到下面。</span>
        </li>
      </ol>
      <form className={css.form} onSubmit={submit} noValidate>
        <Input
          label="登录链接"
          placeholder="粘贴链接"
          mono
          autoComplete="off"
          spellCheck={false}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          error={error ?? undefined}
        />
        <Btn type="submit" full busy={busy} className={css.submit}>
          登录
        </Btn>
      </form>
    </>
  )
}

/** POST v1/auth/quick-login：令牌一次性、60 秒有效；响应不含 token_type。 */
export async function consumeQuickLogin(api: ApiClient, tokens: TokenStore, token: string): Promise<void> {
  const out = await api.post('v1/auth/quick-login', tokenSchema, { auth: false, body: { token } })
  tokens.set(out.access_token)
}
