import { z } from 'zod'

// ---------------------------------------------------------------------------
// 节点证书（Go：domain/certs 的读形状，经 api/admin/certificates.go 原样输出）。
// 指针字段 nullable，切片后端恒回数组；读形状里没有任何密文或令牌字段。
// ---------------------------------------------------------------------------
export const PROVIDERS = ['cloudflare', 'alidns', 'tencentcloud'] as const
export type Provider = (typeof PROVIDERS)[number]

export const CERT_STATUSES = ['pending', 'active', 'paused', 'blocked_credential'] as const
export type CertStatus = (typeof CERT_STATUSES)[number]

export const EXPIRY_LEVELS = ['none', 'ok', 'warning', 'critical', 'expired'] as const
export type ExpiryLevel = (typeof EXPIRY_LEVELS)[number]

export const KEY_TYPES = ['ecdsa-p256', 'rsa-2048'] as const
export type KeyType = (typeof KEY_TYPES)[number]

export const ORDER_STATES = ['queued', 'running', 'succeeded', 'failed', 'cancelled'] as const
export const ORDER_REASONS = ['initial', 'renewal', 'ari', 'manual'] as const
export const CAS = ['letsencrypt', 'letsencrypt_staging', 'zerossl', 'custom'] as const

export const orderBriefSchema = z.object({
  id: z.string(),
  state: z.enum(ORDER_STATES),
  reason: z.enum(ORDER_REASONS),
  created_at: z.string(),
})
export type OrderBrief = z.output<typeof orderBriefSchema>

export const certificateSchema = z.object({
  id: z.string(),
  name: z.string(),
  identifiers: z.array(z.string()),
  wildcard: z.boolean(),
  challenge: z.string(),
  dns_credential_id: z.string(),
  dns_credential_name: z.string(),
  dns_provider: z.enum(PROVIDERS),
  key_type: z.enum(KEY_TYPES),
  status: z.enum(CERT_STATUSES),
  paused_reason: z.enum(['manual', 'failures']).nullable(),
  current_version: z.number().int().nullable(),
  current_ca: z.enum(CAS).nullable(),
  not_before: z.string().nullable(),
  not_after: z.string().nullable(),
  renew_after: z.string().nullable(),
  ari_window_start: z.string().nullable(),
  ari_window_end: z.string().nullable(),
  next_attempt_at: z.string().nullable(),
  consecutive_failures: z.number().int(),
  last_error_code: z.string().nullable(),
  last_error: z.string().nullable(),
  expiry_level: z.enum(EXPIRY_LEVELS),
  active_order: orderBriefSchema.nullable(),
  row_version: z.number().int(),
  created_at: z.string(),
  updated_at: z.string(),
})
export type Certificate = z.output<typeof certificateSchema>

export const certificateSummarySchema = z.object({
  total: z.number().int(),
  warning: z.number().int(),
  critical: z.number().int(),
  expired: z.number().int(),
  failing: z.number().int(),
  paused: z.number().int(),
  blocked: z.number().int(),
})
export type CertificateSummary = z.output<typeof certificateSummarySchema>

export const certificateListSchema = z.object({
  items: z.array(certificateSchema),
  summary: certificateSummarySchema,
})

export const versionSchema = z.object({
  id: z.string(),
  version: z.number().int(),
  ca: z.enum(CAS),
  identifiers: z.array(z.string()),
  is_renewal: z.boolean(),
  serial: z.string(),
  not_before: z.string(),
  not_after: z.string(),
  chain_sha256: z.string(),
  public_key_sha256: z.string(),
  current: z.boolean(),
  created_at: z.string(),
})
export type Version = z.output<typeof versionSchema>

export const orderSchema = z.object({
  id: z.string(),
  reason: z.enum(ORDER_REASONS),
  state: z.enum(ORDER_STATES),
  ca: z.enum(CAS).nullable(),
  replaces: z.boolean(),
  attempt: z.number().int(),
  error_code: z.string().nullable(),
  error_detail: z.string().nullable(),
  version: z.number().int().nullable(),
  created_at: z.string(),
  started_at: z.string().nullable(),
  finished_at: z.string().nullable(),
})
export type Order = z.output<typeof orderSchema>

export const certificateDetailSchema = z.object({
  certificate: certificateSchema,
  versions: z.array(versionSchema),
  orders: z.array(orderSchema),
})
export type CertificateDetail = z.output<typeof certificateDetailSchema>

// ---------------------------------------------------------------------------
// DNS 凭据
// ---------------------------------------------------------------------------
export const VERIFY_STATUSES = ['unverified', 'ok', 'error'] as const

export const dnsCredentialSchema = z.object({
  id: z.string(),
  name: z.string(),
  provider: z.enum(PROVIDERS),
  zone: z.string(),
  secret_hint: z.string(),
  verify_status: z.enum(VERIFY_STATUSES),
  verified_at: z.string().nullable(),
  verify_error: z.string().nullable(),
  visible_zones: z.number().int().nullable(),
  certificate_count: z.number().int(),
  row_version: z.number().int(),
  created_at: z.string(),
  updated_at: z.string(),
})
export type DnsCredential = z.output<typeof dnsCredentialSchema>

export const dnsCredentialListSchema = z.object({ items: z.array(dnsCredentialSchema) })

export const verifyResultSchema = z.object({
  credential: dnsCredentialSchema,
  ok: z.boolean(),
  message: z.string(),
  warnings: z.array(z.string()),
  resumed: z.number().int(),
})
export type VerifyResult = z.output<typeof verifyResultSchema>

// ---------------------------------------------------------------------------
// ACME 设置（EAB HMAC 只回「配没配」）
// ---------------------------------------------------------------------------
export const acmeSettingsSchema = z.object({
  contact_email: z.string(),
  use_staging: z.boolean(),
  zerossl_enabled: z.boolean(),
  zerossl_eab_kid: z.string(),
  zerossl_eab_hmac_set: z.boolean(),
  directory_override: z.boolean(),
})
export type AcmeSettings = z.output<typeof acmeSettingsSchema>
