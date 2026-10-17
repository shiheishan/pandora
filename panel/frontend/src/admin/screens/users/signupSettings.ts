import { z } from 'zod'

// 注册与登录设置的 schema 与表单换算：只依赖 zod，页面与假后端测试共用（查询 hook 在 api.ts）

const CAPTCHA_PROVIDERS = ['', 'recaptcha_v2', 'recaptcha_v3', 'turnstile'] as const

export const signupSettingsSchema = z.object({
  captcha_provider: z.enum(CAPTCHA_PROVIDERS),
  captcha_site_key: z.string(),
  has_captcha_secret: z.boolean(),
  recaptcha_v3_threshold: z.number(),
  email_suffix_whitelist_enabled: z.boolean(),
  email_suffixes: z.array(z.string()),
  gmail_alias_block: z.boolean(),
  ip_limit_enabled: z.boolean(),
  ip_limit_count: z.number().int(),
  ip_limit_window_minutes: z.number().int(),
  trial_plan_id: z.string().nullable(),
  trial_hours: z.number().int(),
  invite_max_codes: z.number().int(),
  invite_never_expire: z.boolean(),
  mail_link_login: z.boolean(),
  mail_configured: z.boolean(),
})
export type SignupSettings = z.output<typeof signupSettingsSchema>

export interface SignupForm {
  captcha_provider: SignupSettings['captcha_provider']
  captcha_site_key: string
  captcha_secret: string
  clear_secret: boolean
  recaptcha_v3_threshold: string
  email_suffix_whitelist_enabled: boolean
  email_suffixes: string[]
  gmail_alias_block: boolean
  ip_limit_enabled: boolean
  ip_limit_count: string
  ip_limit_window_minutes: string
  trial_plan_id: string
  trial_hours: string
  invite_max_codes: string
  invite_never_expire: boolean
  mail_link_login: boolean
}

export const EMAIL_SUFFIX_MAX = 100

/** trim、去开头的 @、小写 */
export function normalizeEmailSuffix(raw: string): string {
  return raw.trim().replace(/^@/, '').toLowerCase()
}

/** 非空、不含空白与 @、含一个以上 .、不以 . 开头或结尾 */
export function isEmailSuffix(v: string): boolean {
  if (!v || /\s|@/.test(v)) return false
  if (v.startsWith('.') || v.endsWith('.')) return false
  const dots = v.split('.').length - 1
  return dots >= 1
}

function suffixListsEqual(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false
  const sa = [...a].sort()
  const sb = [...b].sort()
  return sa.every((v, i) => v === sb[i])
}

function normalizeSuffixList(raw: string[]): { suffixes: string[]; error?: string } {
  const seen = new Set<string>()
  const out: string[] = []
  for (const item of raw) {
    const s = normalizeEmailSuffix(item)
    if (!s || seen.has(s)) continue
    seen.add(s)
    out.push(s)
  }
  if (out.length > EMAIL_SUFFIX_MAX) {
    return { suffixes: out, error: '最多 100 个后缀' }
  }
  return { suffixes: out }
}

export function captchaEnabled(form: SignupForm): boolean {
  return form.captcha_provider !== ''
}

export function signupFormFrom(s: SignupSettings): SignupForm {
  return {
    captcha_provider: s.captcha_provider,
    captcha_site_key: s.captcha_site_key,
    captcha_secret: '',
    clear_secret: false,
    recaptcha_v3_threshold: String(s.recaptcha_v3_threshold),
    email_suffix_whitelist_enabled: s.email_suffix_whitelist_enabled,
    email_suffixes: [...s.email_suffixes],
    gmail_alias_block: s.gmail_alias_block,
    ip_limit_enabled: s.ip_limit_enabled,
    ip_limit_count: String(s.ip_limit_count),
    ip_limit_window_minutes: String(s.ip_limit_window_minutes),
    trial_plan_id: s.trial_plan_id ?? '',
    trial_hours: String(s.trial_hours),
    invite_max_codes: String(s.invite_max_codes),
    invite_never_expire: s.invite_never_expire,
    mail_link_login: s.mail_link_login,
  }
}

function parseIntField(raw: string, key: string, min: number, max: number, message: string, errors: Record<string, string>): number | null {
  const t = raw.trim()
  if (!/^-?\d+$/.test(t)) {
    errors[key] = message
    return null
  }
  const n = Number(t)
  if (!Number.isInteger(n) || n < min || n > max) {
    errors[key] = message
    return null
  }
  return n
}

function parseThreshold(raw: string, errors: Record<string, string>): number | null {
  const t = raw.trim()
  const n = Number(t)
  if (t === '' || Number.isNaN(n) || n < 0 || n > 1) {
    errors.recaptcha_v3_threshold = '阈值需在 0 到 1 之间'
    return null
  }
  return n
}

export function signupBody(form: SignupForm, saved: SignupSettings): { body: Record<string, unknown> } | { errors: Record<string, string> } {
  const errors: Record<string, string> = {}
  const body: Record<string, unknown> = {}

  if (form.captcha_provider !== saved.captcha_provider) body.captcha_provider = form.captcha_provider
  if (form.captcha_site_key !== saved.captcha_site_key) body.captcha_site_key = form.captcha_site_key

  if (form.clear_secret) {
    body.captcha_secret = ''
  } else if (form.captcha_secret.trim()) {
    body.captcha_secret = form.captcha_secret.trim()
  }

  if (form.captcha_provider === 'recaptcha_v3') {
    const th = parseThreshold(form.recaptcha_v3_threshold, errors)
    if (th !== null && th !== saved.recaptcha_v3_threshold) body.recaptcha_v3_threshold = th
  } else if (form.recaptcha_v3_threshold.trim() !== String(saved.recaptcha_v3_threshold)) {
    const th = parseThreshold(form.recaptcha_v3_threshold, errors)
    if (th !== null) body.recaptcha_v3_threshold = th
  }

  if (form.email_suffix_whitelist_enabled !== saved.email_suffix_whitelist_enabled) {
    body.email_suffix_whitelist_enabled = form.email_suffix_whitelist_enabled
  }
  const { suffixes, error: suffixError } = normalizeSuffixList(form.email_suffixes)
  if (suffixError) errors.email_suffixes = suffixError
  else if (!suffixListsEqual(suffixes, saved.email_suffixes)) body.email_suffixes = suffixes

  if (form.gmail_alias_block !== saved.gmail_alias_block) body.gmail_alias_block = form.gmail_alias_block
  if (form.ip_limit_enabled !== saved.ip_limit_enabled) body.ip_limit_enabled = form.ip_limit_enabled

  const ipCount = parseIntField(form.ip_limit_count, 'ip_limit_count', 1, 1000, '注册 IP 限额需在 1 到 1000 之间', errors)
  if (ipCount !== null && ipCount !== saved.ip_limit_count) body.ip_limit_count = ipCount

  const ipWin = parseIntField(
    form.ip_limit_window_minutes,
    'ip_limit_window_minutes',
    1,
    1440,
    '时间窗口需在 1 到 1440 分钟之间',
    errors,
  )
  if (ipWin !== null && ipWin !== saved.ip_limit_window_minutes) body.ip_limit_window_minutes = ipWin

  const savedTrial = saved.trial_plan_id ?? ''
  if (form.trial_plan_id !== savedTrial) {
    body.trial_plan_id = form.trial_plan_id === '' ? '' : form.trial_plan_id
  }

  const trialHours = parseIntField(form.trial_hours, 'trial_hours', 0, 8760, '试用时长需在 0 到 8760 小时之间', errors)
  if (trialHours !== null && trialHours !== saved.trial_hours) body.trial_hours = trialHours

  const inviteMax = parseIntField(form.invite_max_codes, 'invite_max_codes', 1, 50, '邀请码上限需在 1 到 50 之间', errors)
  if (inviteMax !== null && inviteMax !== saved.invite_max_codes) body.invite_max_codes = inviteMax

  if (form.invite_never_expire !== saved.invite_never_expire) body.invite_never_expire = form.invite_never_expire
  if (form.mail_link_login !== saved.mail_link_login) body.mail_link_login = form.mail_link_login

  if (Object.keys(errors).length > 0) return { errors }
  return { body }
}
