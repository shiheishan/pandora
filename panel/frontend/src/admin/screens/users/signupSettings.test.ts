import { describe, expect, it } from 'vitest'
import {
  EMAIL_SUFFIX_MAX,
  isEmailSuffix,
  normalizeEmailSuffix,
  signupBody,
  signupFormFrom,
  type SignupSettings,
} from './signupSettings'

const saved: SignupSettings = {
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

describe('signupBody', () => {
  it('未改时 body 为 {}', () => {
    const form = signupFormFrom(saved)
    expect(signupBody(form, saved)).toEqual({ body: {} })
  })

  it('改一个开关只发该键', () => {
    const form = signupFormFrom(saved)
    form.gmail_alias_block = true
    expect(signupBody(form, saved)).toEqual({ body: { gmail_alias_block: true } })
  })

  it('后缀切分去重转小写', () => {
    const form = signupFormFrom(saved)
    form.email_suffixes = ['Gmail.com', 'gmail.com', '@QQ.COM']
    const r = signupBody(form, saved)
    expect(r).toEqual({ body: { email_suffixes: ['gmail.com', 'qq.com'] } })
    expect(normalizeEmailSuffix('@Foo.COM ')).toBe('foo.com')
  })

  it('顺序不同但内容相同 → body 为 {}', () => {
    const form = signupFormFrom(saved)
    form.email_suffixes = [...saved.email_suffixes].reverse()
    expect(signupBody(form, saved)).toEqual({ body: {} })
  })

  it('101 个不同后缀返回 errors', () => {
    const form = signupFormFrom(saved)
    form.email_suffixes = Array.from({ length: EMAIL_SUFFIX_MAX + 1 }, (_, i) => `s${i}.example.test`)
    expect(signupBody(form, saved)).toEqual({ errors: { email_suffixes: '最多 100 个后缀' } })
  })

  it('数字越界返回 errors', () => {
    const form = signupFormFrom(saved)
    form.ip_limit_count = '0'
    expect(signupBody(form, saved)).toEqual({ errors: { ip_limit_count: '注册 IP 限额需在 1 到 1000 之间' } })
  })

  it('clear_secret 发 captcha_secret 空串', () => {
    const form = signupFormFrom(saved)
    form.clear_secret = true
    expect(signupBody(form, saved)).toEqual({ body: { captcha_secret: '' } })
  })

  it('secret 留空不发', () => {
    const form = signupFormFrom(saved)
    form.captcha_secret = ''
    form.captcha_site_key = 'test-site-key'
    expect(signupBody(form, saved)).toEqual({ body: { captcha_site_key: 'test-site-key' } })
  })

  it('trial 选不试用发 trial_plan_id 空串', () => {
    const withTrial = { ...saved, trial_plan_id: 'plan-a', trial_hours: 24 }
    const form = signupFormFrom(withTrial)
    form.trial_plan_id = ''
    expect(signupBody(form, withTrial)).toEqual({ body: { trial_plan_id: '' } })
  })
})

describe('isEmailSuffix', () => {
  it('合法与非法后缀', () => {
    expect(isEmailSuffix('qq.com')).toBe(true)
    expect(isEmailSuffix('qq')).toBe(false)
    expect(isEmailSuffix('a b.com')).toBe(false)
    expect(isEmailSuffix('.com')).toBe(false)
  })
})
