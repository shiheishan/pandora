import { useEffect, useRef, useState, type FormEvent } from 'react'
import { ArrowRight02Icon, Shield01Icon, ViewIcon, ViewOffSlashIcon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { z } from 'zod'
import { Logo } from '../shell/Logo'
import { useRuntime } from '../shell/runtime'
import { useToast } from '../ui/Toast'
import { hasLoginErrors, loginFailure, validateLogin, type LoginErrors } from './login-form'
import css from './LoginPage.module.css'
import { documentSiteName } from './site-name'

const loginSchema = z.object({
  access_token: z.string().min(1),
  token_type: z.literal('Bearer'),
  expires_in: z.number(),
  user_id: z.string(),
})

export function LoginPage() {
  const site = documentSiteName()
  const toast = useToast()
  const { api, tokens } = useRuntime()

  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [errors, setErrors] = useState<LoginErrors>({})
  const [busy, setBusy] = useState(false)

  const emailInputRef = useRef<HTMLInputElement>(null)
  const passwordInputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    document.title = `登录 · ${site} 控制台`
  }, [site])

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    const nextErrors = validateLogin(email, password)
    if (hasLoginErrors(nextErrors)) {
      setErrors(nextErrors)
      if (nextErrors.email) {
        emailInputRef.current?.focus()
      } else if (nextErrors.password) {
        passwordInputRef.current?.focus()
      }
      return
    }

    setBusy(true)
    setErrors({})

    try {
      const out = await api.post('v1/auth/login', loginSchema, {
        auth: false,
        withCookies: true,
        body: { email: email.trim(), password },
      })
      tokens.set(out.access_token)
      toast('登录成功，正在进入控制台')
    } catch (err) {
      const failErrors = loginFailure(err)
      setErrors(failErrors)
      setBusy(false)

      if (failErrors.email) {
        emailInputRef.current?.focus()
      } else if (failErrors.password) {
        passwordInputRef.current?.focus()
      }
    }
  }

  return (
    <div className={css.page}>
      <div className={css.card}>
        {/* 左栏：表单 */}
        <div className={css.leftSide}>
          <header className={css.brandRow}>
            <span className={css.brandLogoBox} aria-hidden="true">
              <Logo size={22} wordmark={false} />
            </span>
            <span className={css.brandName}>{site}</span>
          </header>

          <div className={css.formWrap}>
            <div className={css.header}>
              <h1 className={css.title}>登录 {site} 控制台</h1>
              <p className={css.subtitle}>运营、账务与节点网络，在同一张桌面上完成。</p>
            </div>

            <form className={css.form} onSubmit={submit} noValidate>
              {/* 邮箱 */}
              <div className={css.field}>
                <label htmlFor="login-email" className={css.label}>
                  管理员邮箱
                </label>
                <div className={css.inputWrap}>
                  <input
                    ref={emailInputRef}
                    id="login-email"
                    type="email"
                    autoComplete="username"
                    placeholder="admin@example.test"
                    value={email}
                    onChange={(e) => {
                      setEmail(e.target.value)
                      if (errors.email || errors.form) {
                        setErrors((prev) => ({ ...prev, email: undefined, form: undefined }))
                      }
                    }}
                    autoFocus
                    aria-invalid={Boolean(errors.email)}
                    aria-describedby={errors.email ? 'login-email-error' : undefined}
                    className={`${css.input}${errors.email ? ` ${css.inputInvalid}` : ''}`}
                  />
                </div>
                {errors.email && (
                  <span id="login-email-error" role="alert" className={css.errorText}>
                    {errors.email}
                  </span>
                )}
              </div>

              {/* 密码 */}
              <div className={css.field}>
                <label htmlFor="login-password" className={css.label}>
                  密码
                </label>
                <div className={css.inputWrap}>
                  <input
                    ref={passwordInputRef}
                    id="login-password"
                    type={showPassword ? 'text' : 'password'}
                    autoComplete="current-password"
                    placeholder="输入密码"
                    value={password}
                    onChange={(e) => {
                      setPassword(e.target.value)
                      if (errors.password || errors.form) {
                        setErrors((prev) => ({ ...prev, password: undefined, form: undefined }))
                      }
                    }}
                    aria-invalid={Boolean(errors.password)}
                    aria-describedby={errors.password ? 'login-password-error' : undefined}
                    className={`${css.input} ${css.inputWithToggle}${errors.password ? ` ${css.inputInvalid}` : ''}`}
                  />
                  <button
                    type="button"
                    onClick={() => setShowPassword((prev) => !prev)}
                    aria-label={showPassword ? '隐藏密码' : '显示密码'}
                    aria-pressed={showPassword}
                    className={css.togglePwBtn}
                  >
                    <HugeiconsIcon
                      icon={showPassword ? ViewOffSlashIcon : ViewIcon}
                      size={16}
                      strokeWidth={1.75}
                      aria-hidden="true"
                    />
                  </button>
                </div>
                {errors.password && (
                  <span id="login-password-error" role="alert" className={css.errorText}>
                    {errors.password}
                  </span>
                )}
              </div>

              {/* 表单级错误 */}
              {errors.form && (
                <div role="alert" className={css.formError}>
                  {errors.form}
                </div>
              )}

              {/* 提交按钮 */}
              <button type="submit" disabled={busy} className={css.submitBtn}>
                {busy && <span aria-hidden="true" className={css.spinner} />}
                <span>{busy ? '正在登录…' : '登录'}</span>
                {!busy && (
                  <span className={css.arrowIcon} aria-hidden="true">
                    <HugeiconsIcon icon={ArrowRight02Icon} size={16} strokeWidth={2} />
                  </span>
                )}
              </button>
            </form>
          </div>
        </div>

        {/* 右栏：纯装饰 */}
        <aside className={css.rightSide} aria-hidden="true">
          <div className={css.decorCanvas}>
            <div className={css.decorStage}>
              {/* 4 个同心圆 */}
              <div className={`${css.ring} ${css.ring160}`} />
              <div className={`${css.ring} ${css.ring280}`} />
              <div className={`${css.ring} ${css.ring400}`} />
              <div className={`${css.ring} ${css.ring520}`} />

              {/* 虚线圆（90s 旋转） */}
              <div className={css.dashRing} />

              {/* 正中 Logo 方块 */}
              <div className={css.centerLogoBox}>
                <Logo size={36} wordmark={false} />
              </div>

              {/* 3 个浮动小标签 */}
              <div className={`${css.floatingChip} ${css.chip1}`}>
                <span className={css.chipInner}>
                  <span className={css.chipIcon}>
                    <HugeiconsIcon icon={Shield01Icon} size={13} strokeWidth={2} />
                  </span>
                  设备已识别
                </span>
              </div>
              <div className={`${css.floatingChip} ${css.chip2}`}>
                <span className={css.chipInner}>
                  <span className={css.chipIcon}>
                    <HugeiconsIcon icon={Shield01Icon} size={13} strokeWidth={2} />
                  </span>
                  敏感操作二次认证
                </span>
              </div>
              <div className={`${css.floatingChip} ${css.chip3}`}>
                <span className={css.chipInner}>
                  <span className={css.chipIcon}>
                    <HugeiconsIcon icon={Shield01Icon} size={13} strokeWidth={2} />
                  </span>
                  操作全程留痕
                </span>
              </div>
            </div>
          </div>

          <div className={css.asideFooter}>
            <p className={css.asideText}>用户、套餐、订单、节点和工单都在这里。改动前看得到影响，改动后查得到记录。</p>
            <p className={css.asideRelease}>管理控制台 · {__APP_RELEASE__}</p>
          </div>
        </aside>
      </div>
    </div>
  )
}
