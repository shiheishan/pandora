import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Card, Input, QueryView, Select, Switch, TextArea, useToast } from '../../../ui'
import { useFailure } from '../../actions'
import { UK, usePlanOptions, useSignupSettings } from './api'
import ops from './Ops.module.css'
import css from './Users.module.css'
import {
  signupBody,
  signupFormFrom,
  signupSettingsSchema,
  type SignupForm,
  type SignupSettings,
} from './signupSettings'

const CAPTCHA_OPTIONS = [
  { value: '', label: '不开' },
  { value: 'recaptcha_v2', label: 'reCAPTCHA v2' },
  { value: 'recaptcha_v3', label: 'reCAPTCHA v3' },
  { value: 'turnstile', label: 'Cloudflare Turnstile' },
] as const

export function SignupTab() {
  const settings = useSignupSettings()
  return (
    <QueryView query={settings} rows={4} empty={null}>
      {(saved) => <SignupSettingsForm saved={saved} />}
    </QueryView>
  )
}

function SignupSettingsForm({ saved }: { saved: SignupSettings }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const client = useQueryClient()
  const plans = usePlanOptions()
  const [draft, setDraft] = useState<SignupForm | null>(null)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const baseline = signupFormFrom(saved)
  const form = draft ?? baseline
  const dirty = draft !== null && JSON.stringify(draft) !== JSON.stringify(baseline)
  const edit = (patch: Partial<SignupForm>) => setDraft({ ...form, ...patch })

  const planOptions = useMemo(
    () => (plans.data ?? []).filter((p) => p.status !== 'archived').map((p) => ({ value: p.id, label: p.name })),
    [plans.data],
  )

  const save = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.post('v1/settings/signup', signupSettingsSchema, { body }),
    onSuccess: () => {
      toast('注册与邀请设置已保存')
      void client.invalidateQueries({ queryKey: [...UK, 'signup-settings'] }).then(() => setDraft(null))
    },
    onError: (e) => fail(e, (f) => setErrors(f)),
  })

  const submit = () => {
    const r = signupBody(form, saved)
    if ('errors' in r) return setErrors(r.errors)
    setErrors({})
    save.mutate(r.body)
  }

  const disabled = save.isPending
  const mailLinkDisabled = !saved.mail_configured && !form.mail_link_login

  return (
    <div className={ops.formStack}>
      <div className={ops.splitDevices}>
        <Card title="人机验证">
          <div className={ops.formStack}>
            <p className={css.small}>开启后注册第 1 步与找回密码第 1 步要求通过人机验证；第三方脚本由门户按需加载。</p>
            <Select
              label="提供方"
              value={form.captcha_provider}
              options={[...CAPTCHA_OPTIONS]}
              disabled={disabled}
              onChange={(e) => edit({ captcha_provider: e.target.value as SignupForm['captcha_provider'] })}
            />
            <Input
              label="站点密钥（site key）"
              mono
              value={form.captcha_site_key}
              error={errors.captcha_site_key}
              disabled={disabled}
              onChange={(e) => edit({ captcha_site_key: e.target.value })}
            />
            <Input
              label="服务端密钥（secret）"
              type="password"
              autoComplete="off"
              mono
              value={form.captcha_secret}
              placeholder={saved.has_captcha_secret ? '已保存，留空不改' : '未设置'}
              error={errors.captcha_secret}
              disabled={disabled || form.clear_secret}
              onChange={(e) => edit({ captcha_secret: e.target.value })}
            />
            {saved.has_captcha_secret && (
              <Switch
                label="清除已保存的 secret"
                checked={form.clear_secret}
                disabled={disabled}
                onChange={(e) => edit({ clear_secret: e.target.checked, captcha_secret: e.target.checked ? '' : form.captcha_secret })}
              />
            )}
            {form.captcha_provider === 'recaptcha_v3' && (
              <Input
                label="reCAPTCHA v3 阈值"
                mono
                inputMode="decimal"
                value={form.recaptcha_v3_threshold}
                error={errors.recaptcha_v3_threshold}
                hint="0 到 1 之间，分数低于阈值视为未通过"
                disabled={disabled}
                onChange={(e) => edit({ recaptcha_v3_threshold: e.target.value })}
              />
            )}
          </div>
        </Card>

        <Card title="注册限制">
          <div className={ops.formStack}>
            <Switch
              label="邮箱后缀白名单"
              checked={form.email_suffix_whitelist_enabled}
              disabled={disabled}
              onChange={(e) => edit({ email_suffix_whitelist_enabled: e.target.checked })}
            />
            <TextArea
              label="允许的后缀"
              value={form.email_suffixes_text}
              error={errors.email_suffixes}
              hint="每行一个域名，也可用逗号分隔"
              disabled={disabled || !form.email_suffix_whitelist_enabled}
              onChange={(e) => edit({ email_suffixes_text: e.target.value })}
            />
            <Switch
              label="禁止 Gmail 别名"
              checked={form.gmail_alias_block}
              disabled={disabled}
              onChange={(e) => edit({ gmail_alias_block: e.target.checked })}
            />
            <p className={css.small}>开启后 gmail.com / googlemail.com 带 . 或 + 的地址不能注册</p>
            <Switch
              label="注册 IP 限额"
              checked={form.ip_limit_enabled}
              disabled={disabled}
              onChange={(e) => edit({ ip_limit_enabled: e.target.checked })}
            />
            <Input
              label="次数"
              mono
              inputMode="numeric"
              value={form.ip_limit_count}
              error={errors.ip_limit_count}
              disabled={disabled || !form.ip_limit_enabled}
              onChange={(e) => edit({ ip_limit_count: e.target.value })}
            />
            <Input
              label="分钟"
              mono
              inputMode="numeric"
              value={form.ip_limit_window_minutes}
              error={errors.ip_limit_window_minutes}
              hint="同一 IP 在窗口内最多发起这么多次注册"
              disabled={disabled || !form.ip_limit_enabled}
              onChange={(e) => edit({ ip_limit_window_minutes: e.target.value })}
            />
          </div>
        </Card>
      </div>

      <div className={ops.splitDevices}>
        <Card title="注册试用">
          <div className={ops.formStack}>
            <Select
              label="试用套餐"
              emptyOption="不试用"
              value={form.trial_plan_id}
              options={planOptions}
              error={errors.trial_plan_id}
              disabled={disabled}
              onChange={(e) => edit({ trial_plan_id: e.target.value })}
            />
            <Input
              label="试用时长（小时）"
              mono
              inputMode="numeric"
              value={form.trial_hours}
              error={errors.trial_hours}
              hint="0 表示关闭；每人只发一次，不建订单"
              disabled={disabled}
              onChange={(e) => edit({ trial_hours: e.target.value })}
            />
          </div>
        </Card>

        <Card title="邀请与登录">
          <div className={ops.formStack}>
            <Input
              label="每人最多同时持有的邀请码"
              mono
              inputMode="numeric"
              value={form.invite_max_codes}
              error={errors.invite_max_codes}
              disabled={disabled}
              onChange={(e) => edit({ invite_max_codes: e.target.value })}
            />
            <Switch
              label="邀请码永不失效"
              checked={form.invite_never_expire}
              disabled={disabled}
              onChange={(e) => edit({ invite_never_expire: e.target.checked })}
            />
            <p className={css.small}>关闭后个人邀请码用一次就失效（后台发的码不受影响）</p>
            <Switch
              label="邮件链接登录"
              checked={form.mail_link_login}
              disabled={disabled || mailLinkDisabled}
              onChange={(e) => edit({ mail_link_login: e.target.checked })}
            />
            <p className={css.small}>
              {saved.mail_configured ? '门户登录页多一个「邮件登录」，链接 15 分钟内有效、只能用一次' : '需要先在 系统 → 通知渠道 配好发信'}
            </p>
            {errors.mail_link_login && (
              <p className={css.tone_danger} role="alert">
                {errors.mail_link_login}
              </p>
            )}
          </div>
        </Card>
      </div>

      <Button variant="primary" disabled={!dirty} busy={save.isPending} onClick={submit}>
        保存
      </Button>
    </div>
  )
}
