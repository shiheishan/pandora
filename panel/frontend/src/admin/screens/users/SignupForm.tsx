import {
  Globe02Icon,
  Mail01Icon,
  MailRemove01Icon,
  Robot01Icon,
} from '@hugeicons-pro/core-stroke-rounded'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useId, useMemo, useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Card, Checkbox, Input, QueryView, Select, Switch, useToast } from '../../../ui'
import { RadioCards, SaveBar, SectionTitle, SettingRow, TagInput } from '../../kit'
import { useFailure } from '../../actions'
import { UK, usePlanOptions, useSignupSettings } from './api'
import styles from './Signup.module.css'
import {
  EMAIL_SUFFIX_MAX,
  captchaEnabled,
  isEmailSuffix,
  normalizeEmailSuffix,
  signupBody,
  signupFormFrom,
  signupSettingsSchema,
  type SignupForm,
  type SignupSettings,
} from './signupSettings'

export type SignupSection = 'growth' | 'security'

type CaptchaChoice = 'turnstile' | 'recaptcha_v3' | 'recaptcha_v2'

const CAPTCHA_RADIO_OPTIONS = [
  {
    value: 'turnstile' as const,
    title: 'Cloudflare Turnstile',
    description: '多数访客不用操作，可疑时要点一下',
  },
  {
    value: 'recaptcha_v3' as const,
    title: 'Google reCAPTCHA v3',
    description: '不打断访客，按分数拦截',
  },
  {
    value: 'recaptcha_v2' as const,
    title: 'Google reCAPTCHA v2',
    description: '勾选「我不是机器人」，可疑时要选图',
  },
]

/**
 * 注册设置面板：按 section 显示「安全（人机验证 + 注册限制）」或「营销（注册试用 + 邀请与登录）」
 */
export function SignupSettingsPanel({ section }: { section: SignupSection }) {
  const settings = useSignupSettings()
  return (
    <QueryView query={settings} rows={4} empty={null}>
      {(saved) => <SignupSettingsForm saved={saved} section={section} />}
    </QueryView>
  )
}

function SignupSettingsForm({ saved, section }: { saved: SignupSettings; section: SignupSection }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const client = useQueryClient()
  const plans = usePlanOptions()
  const [draft, setDraft] = useState<SignupForm | null>(null)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [lastProvider, setLastProvider] = useState<CaptchaChoice>(() =>
    saved.captcha_provider === 'recaptcha_v2' || saved.captcha_provider === 'recaptcha_v3' || saved.captcha_provider === 'turnstile'
      ? saved.captcha_provider
      : 'turnstile',
  )
  const inviteNeverId = useId()
  const mailLinkId = useId()
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
      toast(section === 'security' ? '注册防护设置已保存' : '注册试用与邀请设置已保存')
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
  const captchaOn = captchaEnabled(form)
  const captchaProvider: CaptchaChoice =
    form.captcha_provider === 'recaptcha_v2' || form.captcha_provider === 'recaptcha_v3' || form.captcha_provider === 'turnstile'
      ? form.captcha_provider
      : lastProvider

  const mailLinkDesc = saved.mail_configured
    ? '用户端登录页多一个「邮件登录」，链接 15 分钟内有效、只能用一次'
    : '需要先在「通知与插件 › 通知渠道」配好发信'

  if (section === 'security') {
    return (
      <div className={styles.stack}>
        <SectionTitle variant="group" hint="注册、找回密码、邮件登录的第 1 步先过验证">
          人机验证
        </SectionTitle>
        <Card flush>
          <SettingRow
            icon={Robot01Icon}
            title="开启人机验证"
            description="关闭时注册、找回密码、邮件登录都不做验证"
            checked={captchaOn}
            disabled={disabled}
            onChange={(on) => {
              if (!on) {
                if (form.captcha_provider) setLastProvider(form.captcha_provider as CaptchaChoice)
                edit({ captcha_provider: '' })
                return
              }
              edit({ captcha_provider: lastProvider })
            }}
          >
            <div className={styles.revealStack}>
              <p className={styles.fieldLabel}>验证服务</p>
              <RadioCards
                label="验证服务"
                options={CAPTCHA_RADIO_OPTIONS}
                value={captchaProvider}
                columns={3}
                onChange={(v) => {
                  setLastProvider(v)
                  edit({ captcha_provider: v })
                }}
              />
              <div className={styles.fieldGrid}>
                <Input
                  label="站点密钥"
                  mono
                  hint="site key，会下发到用户端页面"
                  value={form.captcha_site_key}
                  error={errors.captcha_site_key}
                  disabled={disabled}
                  onChange={(e) => edit({ captcha_site_key: e.target.value })}
                />
                <Input
                  label="服务端密钥"
                  type="password"
                  autoComplete="off"
                  hint="secret，只能写入，保存后不再显示"
                  value={form.captcha_secret}
                  placeholder={saved.has_captcha_secret ? '已保存，留空不改' : '未设置'}
                  error={errors.captcha_secret}
                  disabled={disabled || form.clear_secret}
                  onChange={(e) => edit({ captcha_secret: e.target.value })}
                />
              </div>
              {saved.has_captcha_secret && (
                <Checkbox
                  label="清除已保存的服务端密钥"
                  checked={form.clear_secret}
                  disabled={disabled}
                  onChange={(e) =>
                    edit({
                      clear_secret: e.target.checked,
                      captcha_secret: e.target.checked ? '' : form.captcha_secret,
                    })
                  }
                />
              )}
              {form.captcha_provider === 'recaptcha_v3' && (
                <Input
                  label="分数阈值"
                  mono
                  inputMode="decimal"
                  hint="0–1，低于这个分数拒绝；0.5 是 Google 建议的起点"
                  value={form.recaptcha_v3_threshold}
                  error={errors.recaptcha_v3_threshold}
                  className={styles.inputThreshold}
                  disabled={disabled}
                  onChange={(e) => edit({ recaptcha_v3_threshold: e.target.value })}
                />
              )}
            </div>
          </SettingRow>
        </Card>

        <SectionTitle variant="group">注册限制</SectionTitle>
        <Card flush>
          <SettingRow
            icon={Mail01Icon}
            title="邮箱后缀白名单"
            description="只允许这些后缀的邮箱注册；关闭时不限"
            checked={form.email_suffix_whitelist_enabled}
            disabled={disabled}
            onChange={(on) => edit({ email_suffix_whitelist_enabled: on })}
          >
            <TagInput
              label="邮箱后缀"
              values={form.email_suffixes}
              max={EMAIL_SUFFIX_MAX}
              placeholder="输入后缀，回车添加"
              normalize={normalizeEmailSuffix}
              validate={isEmailSuffix}
              hint="粘贴多个时用逗号或换行分隔"
              disabled={disabled || !form.email_suffix_whitelist_enabled}
              onChange={(next) => edit({ email_suffixes: next })}
            />
            {errors.email_suffixes && (
              <p className={styles.alert} role="alert">
                {errors.email_suffixes}
              </p>
            )}
          </SettingRow>
          <SettingRow
            icon={MailRemove01Icon}
            title="禁止 Gmail 别名"
            description="拦掉 gmail.com、googlemail.com 里带「.」或「+」的地址，例如 k.liu+1@gmail.com"
            checked={form.gmail_alias_block}
            disabled={disabled}
            onChange={(on) => edit({ gmail_alias_block: on })}
          />
          <SettingRow
            icon={Globe02Icon}
            title="注册 IP 限额"
            description="同一个 IP 短时间内注册太多次就拒绝"
            checked={form.ip_limit_enabled}
            disabled={disabled}
            onChange={(on) => edit({ ip_limit_enabled: on })}
          >
            <div className={styles.sent}>
              同一 IP 每
              <Input
                aria-label="分钟"
                mono
                inputMode="numeric"
                value={form.ip_limit_window_minutes}
                error={errors.ip_limit_window_minutes}
                className={styles.inputMinutes}
                disabled={disabled || !form.ip_limit_enabled}
                onChange={(e) => edit({ ip_limit_window_minutes: e.target.value })}
              />
              分钟内最多注册
              <Input
                aria-label="次数"
                mono
                inputMode="numeric"
                value={form.ip_limit_count}
                error={errors.ip_limit_count}
                className={styles.inputCount}
                disabled={disabled || !form.ip_limit_enabled}
                onChange={(e) => edit({ ip_limit_count: e.target.value })}
              />
              次
            </div>
            <p className={styles.hint}>分钟 1–1440，次数 1–1000</p>
          </SettingRow>
        </Card>

        <SaveBar
          dirty={dirty}
          busy={save.isPending}
          hint="保存时要再验证一次身份"
          onSave={submit}
          onReset={() => {
            setDraft(null)
            setErrors({})
          }}
        />
      </div>
    )
  }

  return (
    <div className={styles.growthCards}>
      <Card title="注册试用">
        <div className={styles.fieldGrid}>
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
            disabled={disabled}
            onChange={(e) => edit({ trial_hours: e.target.value })}
          />
        </div>
        <p className={styles.hint}>0 表示关闭；每人只发一次，不建订单</p>
      </Card>

      <Card
        title="邀请与登录"
        footer={
          <div className={styles.growthFooter}>
            {dirty && <span className={styles.footerHint}>有未保存的改动</span>}
            <span className={styles.footerSpacer} />
            <Button size="sm" variant="primary" disabled={!dirty} busy={save.isPending} onClick={submit}>
              保存
            </Button>
          </div>
        }
      >
        <div className={styles.growthBody}>
          <Input
            label="每人最多同时持有的邀请码"
            mono
            inputMode="numeric"
            value={form.invite_max_codes}
            error={errors.invite_max_codes}
            className={styles.inputInviteMax}
            disabled={disabled}
            onChange={(e) => edit({ invite_max_codes: e.target.value })}
          />
          <div className={styles.swr}>
            <div className={styles.swrText}>
              <span className={styles.swrTitle} id={inviteNeverId}>邀请码永不失效</span>
              <span className={styles.swrDesc}>关闭后每个个人邀请码只能邀请 1 人，有人用它注册后就失效，用户可以再生成新的；用户端邀请页会写明这一点。后台发的码不受影响</span>
            </div>
            <Switch
              checked={form.invite_never_expire}
              disabled={disabled}
              aria-labelledby={inviteNeverId}
              onChange={(e) => edit({ invite_never_expire: e.target.checked })}
            />
          </div>
          <div className={styles.swr}>
            <div className={styles.swrText}>
              <span className={styles.swrTitle} id={mailLinkId}>邮件链接登录</span>
              <span className={styles.swrDesc}>{mailLinkDesc}</span>
            </div>
            <Switch
              checked={form.mail_link_login}
              disabled={disabled || mailLinkDisabled}
              aria-labelledby={mailLinkId}
              onChange={(e) => edit({ mail_link_login: e.target.checked })}
            />
          </div>
          {errors.mail_link_login && (
            <p className={styles.alert} role="alert">
              {errors.mail_link_login}
            </p>
          )}
        </div>
      </Card>
    </div>
  )
}
