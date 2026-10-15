import type { MockContext, MockModule, MockResult } from '../types.ts'

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------
const err = (status: number, code: string, message: string, fields?: Record<string, string>): MockResult => ({
  status,
  body: { error: { code, message, ...(fields ? { fields } : {}) } },
})
const invalid = (fields: Record<string, string>) => err(422, 'validation_failed', '请求参数校验未通过', fields)
const str = (v: unknown) => (typeof v === 'string' ? v : '')

interface State {
  captcha_provider: '' | 'recaptcha_v2' | 'recaptcha_v3' | 'turnstile'
  captcha_site_key: string
  has_captcha_secret: boolean
  recaptcha_v3_threshold: number
  email_suffix_whitelist_enabled: boolean
  email_suffixes: string[]
  gmail_alias_block: boolean
  ip_limit_enabled: boolean
  ip_limit_count: number
  ip_limit_window_minutes: number
  trial_plan_id: string | null
  trial_hours: number
  invite_max_codes: number
  invite_never_expire: boolean
  mail_link_login: boolean
  mail_configured: boolean
}

const state: State = {
  captcha_provider: '',
  captcha_site_key: '',
  has_captcha_secret: false,
  recaptcha_v3_threshold: 0.5,
  email_suffix_whitelist_enabled: false,
  email_suffixes: ['gmail.com', 'qq.com', '163.com', 'yahoo.com', 'sina.com', '126.com', 'outlook.com', 'yeah.net', 'foxmail.com'],
  gmail_alias_block: false,
  ip_limit_enabled: true,
  ip_limit_count: 10,
  ip_limit_window_minutes: 1,
  trial_plan_id: null,
  trial_hours: 0,
  invite_max_codes: 5,
  invite_never_expire: true,
  mail_link_login: false,
  mail_configured: true,
}

const ALLOWED_KEYS = new Set([
  'captcha_provider',
  'captcha_site_key',
  'captcha_secret',
  'recaptcha_v3_threshold',
  'email_suffix_whitelist_enabled',
  'email_suffixes',
  'gmail_alias_block',
  'ip_limit_enabled',
  'ip_limit_count',
  'ip_limit_window_minutes',
  'trial_plan_id',
  'trial_hours',
  'invite_max_codes',
  'invite_never_expire',
  'mail_link_login',
])

const EMAIL_SUFFIX_RE = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/

function normalizeEmailSuffixes(inList: string[]): { ok: true; suffixes: string[] } | { ok: false; error: string } {
  const seen = new Set<string>()
  const out: string[] = []
  for (const raw of inList) {
    let s = raw.trim().toLowerCase()
    if (s.startsWith('@')) s = s.slice(1)
    if (!s) continue
    if (seen.has(s)) continue
    if (s.length > 253 || !EMAIL_SUFFIX_RE.test(s)) {
      return { ok: false, error: `后缀格式不正确：${raw}` }
    }
    seen.add(s)
    out.push(s)
  }
  if (out.length > 100) return { ok: false, error: '最多 100 个后缀' }
  return { ok: true, suffixes: out }
}

async function setSignup(ctx: MockContext): Promise<MockResult> {
  const body = await ctx.body()
  if (!body || typeof body !== 'object' || Array.isArray(body)) {
    return err(400, 'bad_request', '请求体不是合法的 JSON')
  }

  for (const k of Object.keys(body)) {
    if (!ALLOWED_KEYS.has(k)) {
      return err(400, 'bad_request', `请求体包含未知字段 "${k}"`)
    }
  }

  const fields: Record<string, string> = {}

  if ('captcha_provider' in body) {
    const p = str(body.captcha_provider)
    if (p !== '' && p !== 'recaptcha_v2' && p !== 'recaptcha_v3' && p !== 'turnstile') {
      fields.captcha_provider = '人机验证类型只能是 recaptcha_v2、recaptcha_v3、turnstile 或留空'
    }
  }

  if ('recaptcha_v3_threshold' in body) {
    const t = typeof body.recaptcha_v3_threshold === 'number' ? body.recaptcha_v3_threshold : -1
    if (Number.isNaN(t) || t < 0 || t > 1) {
      fields.recaptcha_v3_threshold = '阈值需在 0 到 1 之间'
    }
  }

  let normalizedSuffixes: string[] | undefined
  if ('email_suffixes' in body) {
    if (!Array.isArray(body.email_suffixes) || !body.email_suffixes.every((x) => typeof x === 'string')) {
      fields.email_suffixes = '后缀格式不正确'
    } else {
      const norm = normalizeEmailSuffixes(body.email_suffixes as string[])
      if (!norm.ok) {
        fields.email_suffixes = norm.error
      } else {
        normalizedSuffixes = norm.suffixes
      }
    }
  }

  if ('ip_limit_count' in body) {
    const c = typeof body.ip_limit_count === 'number' ? body.ip_limit_count : -1
    if (!Number.isInteger(c) || c < 1 || c > 1000) {
      fields.ip_limit_count = '注册 IP 限额需在 1 到 1000 之间'
    }
  }

  if ('ip_limit_window_minutes' in body) {
    const w = typeof body.ip_limit_window_minutes === 'number' ? body.ip_limit_window_minutes : -1
    if (!Number.isInteger(w) || w < 1 || w > 1440) {
      fields.ip_limit_window_minutes = '时间窗口需在 1 到 1440 分钟之间'
    }
  }

  if ('trial_hours' in body) {
    const h = typeof body.trial_hours === 'number' ? body.trial_hours : -1
    if (!Number.isInteger(h) || h < 0 || h > 8760) {
      fields.trial_hours = '试用时长需在 0 到 8760 小时之间'
    }
  }

  if ('invite_max_codes' in body) {
    const m = typeof body.invite_max_codes === 'number' ? body.invite_max_codes : -1
    if (!Number.isInteger(m) || m < 1 || m > 50) {
      fields.invite_max_codes = '邀请码上限需在 1 到 50 之间'
    }
  }

  if (Object.keys(fields).length > 0) return invalid(fields)

  // 预合并以进行组合校验
  const nextProvider = ('captcha_provider' in body ? body.captcha_provider : state.captcha_provider) as State['captcha_provider']
  const nextSiteKey = ('captcha_site_key' in body ? str(body.captcha_site_key) : state.captcha_site_key).trim()

  let nextHasSecret = state.has_captcha_secret
  if ('captcha_secret' in body) {
    nextHasSecret = str(body.captcha_secret).trim() !== ''
  }

  const combo: Record<string, string> = {}
  if (nextProvider !== '') {
    if (!nextSiteKey) {
      combo.captcha_site_key = '开启人机验证前请填写站点密钥（site key）'
    }
    if (!nextHasSecret) {
      combo.captcha_secret = '开启人机验证前请填写服务端密钥（secret）'
    }
  }

  const nextSuffixWhitelistEnabled = 'email_suffix_whitelist_enabled' in body
    ? Boolean(body.email_suffix_whitelist_enabled)
    : state.email_suffix_whitelist_enabled
  const nextSuffixes = normalizedSuffixes !== undefined ? normalizedSuffixes : state.email_suffixes
  if (nextSuffixWhitelistEnabled && nextSuffixes.length === 0) {
    combo.email_suffixes = '开启后缀白名单时至少填一个后缀'
  }

  const nextTrialHours = 'trial_hours' in body && typeof body.trial_hours === 'number'
    ? body.trial_hours
    : state.trial_hours
  let nextTrialPlanId = state.trial_plan_id
  if ('trial_plan_id' in body) {
    const plan = str(body.trial_plan_id).trim()
    nextTrialPlanId = plan === '' ? null : plan
  }
  if (nextTrialHours > 0 && !nextTrialPlanId) {
    combo.trial_plan_id = '开启注册试用前请选择试用套餐'
  }

  if ('mail_link_login' in body && Boolean(body.mail_link_login) && !state.mail_link_login && !state.mail_configured) {
    combo.mail_link_login = '开启邮件链接登录前请先在通知渠道里配好发信'
  }

  if (Object.keys(combo).length > 0) return invalid(combo)

  // 应用更新
  if ('captcha_provider' in body) state.captcha_provider = nextProvider
  if ('captcha_site_key' in body) state.captcha_site_key = str(body.captcha_site_key)
  if ('captcha_secret' in body) {
    const sec = str(body.captcha_secret).trim()
    state.has_captcha_secret = sec !== ''
  }
  if ('recaptcha_v3_threshold' in body && typeof body.recaptcha_v3_threshold === 'number') {
    state.recaptcha_v3_threshold = body.recaptcha_v3_threshold
  }
  if ('email_suffix_whitelist_enabled' in body) {
    state.email_suffix_whitelist_enabled = Boolean(body.email_suffix_whitelist_enabled)
  }
  if (normalizedSuffixes !== undefined) state.email_suffixes = normalizedSuffixes
  if ('gmail_alias_block' in body) state.gmail_alias_block = Boolean(body.gmail_alias_block)
  if ('ip_limit_enabled' in body) state.ip_limit_enabled = Boolean(body.ip_limit_enabled)
  if ('ip_limit_count' in body && typeof body.ip_limit_count === 'number') {
    state.ip_limit_count = body.ip_limit_count
  }
  if ('ip_limit_window_minutes' in body && typeof body.ip_limit_window_minutes === 'number') {
    state.ip_limit_window_minutes = body.ip_limit_window_minutes
  }
  if ('trial_plan_id' in body) {
    state.trial_plan_id = nextTrialPlanId
  }
  if ('trial_hours' in body && typeof body.trial_hours === 'number') {
    state.trial_hours = body.trial_hours
  }
  if ('invite_max_codes' in body && typeof body.invite_max_codes === 'number') {
    state.invite_max_codes = body.invite_max_codes
  }
  if ('invite_never_expire' in body) state.invite_never_expire = Boolean(body.invite_never_expire)
  if ('mail_link_login' in body) state.mail_link_login = Boolean(body.mail_link_login)

  return { status: 200, body: { ...state } }
}

// ---------------------------------------------------------------------------
// 路由定义
// ---------------------------------------------------------------------------
export const signupSettings: MockModule = {
  routes: {
    'GET /v1/settings/signup': (ctx) => {
      ctx.send(200, { ...state })
    },
    'POST /v1/settings/signup': async (ctx) => {
      if (!ctx.requireReauth()) return
      const res = await setSignup(ctx)
      ctx.send(res.status, res.body)
    },
  },
}
