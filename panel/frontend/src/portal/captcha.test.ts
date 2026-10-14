import { describe, expect, it } from 'vitest'
import { captchaScriptUrl } from './Captcha'

describe('captchaScriptUrl', () => {
  it('recaptcha_v2', () => {
    expect(captchaScriptUrl({ provider: 'recaptcha_v2', site_key: 'test-site-key' })).toBe(
      'https://www.google.com/recaptcha/api.js?render=explicit',
    )
  })

  it('recaptcha_v3 encodes site_key', () => {
    expect(captchaScriptUrl({ provider: 'recaptcha_v3', site_key: 'key&foo=1' })).toBe(
      'https://www.google.com/recaptcha/api.js?render=key%26foo%3D1',
    )
  })

  it('turnstile', () => {
    expect(captchaScriptUrl({ provider: 'turnstile', site_key: 'test-site-key' })).toBe(
      'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit',
    )
  })
})
