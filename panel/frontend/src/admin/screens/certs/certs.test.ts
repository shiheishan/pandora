import { describe, expect, it } from 'vitest'
import {
  acmeBody,
  acmeFormFrom,
  createCredentialBody,
  credentialFormFrom,
  emptyCredentialForm,
  errorCodeLabel,
  identifierNotices,
  parseIdentifiers,
  patchCredentialBody,
  PROVIDER_META,
  remainingText,
  statusView,
  summaryBanner,
  WILDCARD_WARNING,
} from './logic'
import { acmeSettingsSchema, certificateListSchema, PROVIDERS, type AcmeSettings, type Certificate, type DnsCredential } from './schemas'

const cert = (patch: Partial<Certificate> = {}): Certificate => ({
  id: 'c1',
  name: 'wild',
  identifiers: ['*.example.com'],
  wildcard: true,
  challenge: 'dns-01',
  dns_credential_id: 'd1',
  dns_credential_name: 'cf',
  dns_provider: 'cloudflare',
  key_type: 'ecdsa-p256',
  status: 'active',
  paused_reason: null,
  current_version: 1,
  current_ca: 'letsencrypt',
  not_before: '2026-10-01T00:00:00Z',
  not_after: '2026-12-30T00:00:00Z',
  renew_after: '2026-11-30T00:00:00Z',
  ari_window_start: null,
  ari_window_end: null,
  next_attempt_at: null,
  consecutive_failures: 0,
  last_error_code: null,
  last_error: null,
  expiry_level: 'ok',
  active_order: null,
  row_version: 1,
  created_at: '2026-10-01T00:00:00Z',
  updated_at: '2026-10-01T00:00:00Z',
  ...patch,
})

const cred: DnsCredential = {
  id: 'd1',
  name: 'cf',
  provider: 'alidns',
  zone: 'example.com',
  secret_hint: '0001',
  verify_status: 'ok',
  verified_at: '2026-10-08T00:00:00Z',
  verify_error: null,
  visible_zones: 1,
  certificate_count: 0,
  row_version: 1,
  created_at: '2026-10-08T00:00:00Z',
  updated_at: '2026-10-08T00:00:00Z',
}

describe('certificate status and expiry', () => {
  it('shows what the certificate is doing, running orders first', () => {
    expect(statusView(cert()).label).toBe('正常')
    expect(statusView(cert({ active_order: { id: 'o', state: 'running', reason: 'renewal', created_at: '' } })).label).toBe('签发中')
    expect(statusView(cert({ active_order: { id: 'o', state: 'queued', reason: 'manual', created_at: '' } })).label).toBe('排队中')
    expect(statusView(cert({ status: 'paused', paused_reason: 'failures' })).label).toBe('已暂停（连续失败）')
    expect(statusView(cert({ status: 'blocked_credential' })).tone).toBe('danger')
    expect(statusView(cert({ consecutive_failures: 2 })).label).toBe('签发失败')
    expect(statusView(cert({ status: 'pending', current_version: null })).label).toBe('待签发')
  })

  it('counts remaining time in days, then hours', () => {
    const now = new Date('2026-10-08T00:00:00Z')
    expect(remainingText('2026-10-20T00:00:00Z', now)).toBe('还剩 12 天')
    expect(remainingText('2026-10-08T15:30:00Z', now)).toBe('还剩 15 小时')
    expect(remainingText('2026-10-07T00:00:00Z', now)).toBe('已过期')
    expect(remainingText(null, now)).toBe('—')
  })

  it('raises the banner only when something needs attention', () => {
    expect(summaryBanner({ warning: 0, critical: 0, expired: 0, failing: 0 })).toBeNull()
    expect(summaryBanner({ warning: 2, critical: 1, expired: 0, failing: 1 })).toBe('需要处理：1 张快到期，2 张即将到期，1 张签发失败。')
    expect(errorCodeLabel('rate_limited_local')).toBe('预计超出 CA 每周限额')
    expect(errorCodeLabel('something_new')).toBe('something_new')
  })

  it('parses the list response the Go handler writes', () => {
    const parsed = certificateListSchema.parse({ items: [cert()], summary: { total: 1, warning: 0, critical: 0, expired: 0, failing: 0, paused: 0, blocked: 0 } })
    expect(parsed.items[0]!.wildcard).toBe(true)
  })
})

describe('new certificate form', () => {
  it('splits identifiers on lines, commas and spaces', () => {
    expect(parseIdentifiers('*.Example.com\nexample.com, a.example.net  example.com.')).toEqual(['*.example.com', 'example.com', 'a.example.net'])
  })

  it('warns about shared private keys on wildcards and about names outside the zone', () => {
    expect(identifierNotices(['*.example.com'], 'example.com')).toEqual([WILDCARD_WARNING])
    expect(identifierNotices(['node.example.com'], 'example.com')).toEqual([])
    const outside = identifierNotices(['node.example.net'], 'example.com')
    expect(outside).toHaveLength(1)
    expect(outside[0]).toContain('CNAME')
  })
})

describe('DNS credential bodies are write-only', () => {
  it('knows the fields of every provider the backend accepts', () => {
    expect(Object.keys(PROVIDER_META).sort()).toEqual([...PROVIDERS].sort())
    expect(PROVIDER_META.cloudflare.fields.map((f) => f.key)).toEqual(['api_token'])
    expect(PROVIDER_META.alidns.fields.map((f) => f.key)).toEqual(['access_key_id', 'access_key_secret'])
    expect(PROVIDER_META.tencentcloud.fields.map((f) => f.key)).toEqual(['secret_id', 'secret_key'])
  })

  it('requires every secret field on create', () => {
    const form = { ...emptyCredentialForm('tencentcloud'), name: 'tc', zone: 'example.com', secret: { secret_id: 'AKID' } }
    expect(createCredentialBody(form)).toEqual({ errors: { 'secret.secret_key': '填写SecretKey' } })
    expect(createCredentialBody({ ...form, secret: { secret_id: ' AKID ', secret_key: 'k' } })).toEqual({
      body: { name: 'tc', provider: 'tencentcloud', zone: 'example.com', secret: { secret_id: 'AKID', secret_key: 'k' } },
    })
  })

  it('sends only what changed on edit and leaves blank secrets out', () => {
    const form = credentialFormFrom(cred)
    expect(patchCredentialBody(form, cred)).toBeNull()
    expect(patchCredentialBody({ ...form, name: 'renamed' }, cred)).toEqual({ name: 'renamed' })
    expect(patchCredentialBody({ ...form, secret: { access_key_secret: 'new', access_key_id: '' } }, cred)).toEqual({ secret: { access_key_secret: 'new' } })
  })
})

describe('ACME settings', () => {
  const saved: AcmeSettings = acmeSettingsSchema.parse({
    contact_email: '',
    use_staging: false,
    zerossl_enabled: false,
    zerossl_eab_kid: '',
    zerossl_eab_hmac_set: true,
    directory_override: false,
  })

  it('keeps the saved HMAC unless replaced or explicitly cleared', () => {
    const form = { ...acmeFormFrom(saved), zerosslEnabled: true, eabKid: 'kid' }
    expect(acmeBody(form, saved)).toEqual({ body: { contact_email: '', use_staging: false, zerossl_enabled: true, zerossl_eab_kid: 'kid' } })
    expect(acmeBody({ ...form, eabHmac: 'abc' }, saved)).toMatchObject({ body: { zerossl_eab_hmac: 'abc' } })
    expect(acmeBody({ ...form, clearHmac: true }, saved)).toEqual({ errors: { zerossl_eab_hmac: '启用 ZeroSSL 要填 EAB HMAC' } })
    expect(acmeBody({ ...acmeFormFrom(saved), clearHmac: true }, saved)).toMatchObject({ body: { zerossl_eab_hmac: '' } })
  })

  it('validates the contact email and the EAB KID', () => {
    expect(acmeBody({ ...acmeFormFrom(saved), contactEmail: 'not-mail' }, saved)).toEqual({ errors: { contact_email: '邮箱格式不对' } })
    expect(acmeBody({ ...acmeFormFrom(saved), zerosslEnabled: true }, saved)).toEqual({ errors: { zerossl_eab_kid: '启用 ZeroSSL 要填 EAB KID' } })
  })
})
