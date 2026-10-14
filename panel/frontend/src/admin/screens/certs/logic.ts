import type { TagTone } from '../../../ui'
import type { AcmeSettings, Certificate, DnsCredential, ExpiryLevel, Order, Provider } from './schemas'

// ---------------------------------------------------------------------------
// DNS 提供方：凭据字段与最小权限指引（与 Go 的 domain/certs providerFields 同键）
// ---------------------------------------------------------------------------
export interface ProviderField {
  key: string
  label: string
  placeholder: string
}

export interface ProviderMeta {
  label: string
  fields: readonly ProviderField[]
  /** 最小权限的做法，按步骤 */
  guide: readonly string[]
}

export const PROVIDER_META: Readonly<Record<Provider, ProviderMeta>> = {
  cloudflare: {
    label: 'Cloudflare',
    fields: [{ key: 'api_token', label: 'API 令牌', placeholder: 'Cloudflare API Token' }],
    guide: [
      '在 Cloudflare「我的个人资料 › API 令牌」创建自定义令牌，不要用 Global API Key。',
      '权限只给两项：Zone · DNS · Edit，Zone · Zone · Read。',
      'Zone Resources 只选这一个域名；可以再限制来源 IP（填面板出口 IP）和过期时间。',
      '高级：把 _acme-challenge 用 CNAME 委托到一个专门做验证的域名，令牌只授权那个域名。',
    ],
  },
  alidns: {
    label: '阿里云 DNS',
    fields: [
      { key: 'access_key_id', label: 'AccessKey ID', placeholder: 'LTAI…' },
      { key: 'access_key_secret', label: 'AccessKey Secret', placeholder: 'AccessKey Secret' },
    ],
    guide: [
      '在 RAM 控制台建一个只用于签证书的子用户，只开 OpenAPI 调用访问，不要用主账号的 AccessKey。',
      '授权策略用自定义策略，只给 alidns:DescribeDomains、DescribeDomainRecords、AddDomainRecord、DeleteDomainRecord。',
      '策略的资源限定为这个域名（acs:alidns:*:*:domain/你的域名）；不想写自定义策略可以给 AliyunDNSFullAccess，但范围更大。',
    ],
  },
  tencentcloud: {
    label: '腾讯云 DNSPod',
    fields: [
      { key: 'secret_id', label: 'SecretId', placeholder: 'AKID…' },
      { key: 'secret_key', label: 'SecretKey', placeholder: 'SecretKey' },
    ],
    guide: [
      '在访问管理（CAM）建一个只用于签证书的子用户，只开编程访问，不要用主账号的 API 密钥。',
      '授权策略用自定义策略，只给 dnspod:DescribeDomainList、DescribeRecordList、CreateRecord、DeleteRecord。',
      '策略的资源限定为这个域名；不想写自定义策略可以给 QcloudDNSPodFullAccess，但范围更大。',
    ],
  },
}

export const PROVIDER_OPTIONS = (Object.keys(PROVIDER_META) as Provider[]).map((p) => ({ value: p, label: PROVIDER_META[p].label }))

// ---------------------------------------------------------------------------
// 状态与到期
// ---------------------------------------------------------------------------
export interface View {
  label: string
  tone: TagTone
}

/** 证书当前在做什么：进行中的订单优先，其次暂停、凭据失效、失败、待签发、正常 */
export function statusView(c: Certificate): View {
  if (c.active_order?.state === 'running') return { label: '签发中', tone: 'info' }
  if (c.status === 'paused') return { label: c.paused_reason === 'failures' ? '已暂停（连续失败）' : '已暂停', tone: 'warn' }
  if (c.status === 'blocked_credential') return { label: 'DNS 凭据失效', tone: 'danger' }
  if (c.active_order?.state === 'queued') return { label: '排队中', tone: 'info' }
  if (c.consecutive_failures > 0 || (c.last_error && c.status === 'pending')) return { label: '签发失败', tone: 'danger' }
  if (c.status === 'pending') return { label: '待签发', tone: 'neutral' }
  return { label: '正常', tone: 'ok' }
}

export const EXPIRY_VIEW: Readonly<Record<ExpiryLevel, View>> = {
  none: { label: '未签发', tone: 'neutral' },
  ok: { label: '有效', tone: 'ok' },
  warning: { label: '即将到期', tone: 'warn' },
  critical: { label: '快到期', tone: 'danger' },
  expired: { label: '已过期', tone: 'danger' },
}

const DAY = 24 * 60 * 60 * 1000

/** 「还剩 12 天」「还剩 15 小时」「已过期」 */
export function remainingText(notAfter: string | null, now: Date = new Date()): string {
  if (!notAfter) return '—'
  const ms = new Date(notAfter).getTime() - now.getTime()
  if (ms <= 0) return '已过期'
  if (ms < 2 * DAY) return `还剩 ${Math.max(1, Math.floor(ms / 3_600_000))} 小时`
  return `还剩 ${Math.floor(ms / DAY)} 天`
}

export const CA_LABEL: Readonly<Record<string, string>> = {
  letsencrypt: "Let's Encrypt",
  letsencrypt_staging: "Let's Encrypt 测试环境",
  zerossl: 'ZeroSSL',
  custom: '测试 CA',
}

export const ORDER_REASON_LABEL: Readonly<Record<Order['reason'], string>> = {
  initial: '首次签发',
  renewal: '到期续期',
  ari: 'CA 建议续期',
  manual: '手动',
}

export const ORDER_STATE_VIEW: Readonly<Record<Order['state'], View>> = {
  queued: { label: '排队中', tone: 'info' },
  running: { label: '签发中', tone: 'info' },
  succeeded: { label: '成功', tone: 'ok' },
  failed: { label: '失败', tone: 'danger' },
  cancelled: { label: '已作废', tone: 'neutral' },
}

/** 失败码的中文（后端 error_code）；没收录的原样显示 */
export const ERROR_CODE_LABEL: Readonly<Record<string, string>> = {
  credential_rejected: 'DNS 凭据被拒绝',
  dns_api_unavailable: 'DNS 提供方暂时不可用',
  rate_limited: 'CA 限流',
  rate_limited_local: '预计超出 CA 每周限额',
  acme_account_error: 'ACME 账号注册失败',
  dns_provider_error: '写 DNS 记录失败',
  dns_propagation_timeout: 'DNS 记录生效超时',
  acme_error: 'CA 验证或签发失败',
  interrupted: '签发多次中断',
  not_active: '证书已暂停，订单作废',
  paused: '暂停时作废',
}

export function errorCodeLabel(code: string | null): string {
  if (!code) return ''
  return ERROR_CODE_LABEL[code] ?? code
}

/** 横幅：只在有需要处理的证书时出现（用户 10-07 定：到期与失败只在后台显示） */
export function summaryBanner(s: { warning: number; critical: number; expired: number; failing: number }): string | null {
  const parts: string[] = []
  if (s.expired > 0) parts.push(`${s.expired} 张已过期`)
  if (s.critical > 0) parts.push(`${s.critical} 张快到期`)
  if (s.warning > 0) parts.push(`${s.warning} 张即将到期`)
  if (s.failing > 0) parts.push(`${s.failing} 张签发失败`)
  return parts.length > 0 ? `需要处理：${parts.join('，')}。` : null
}

// ---------------------------------------------------------------------------
// 新建证书
// ---------------------------------------------------------------------------

/** 一行一个或用逗号、空格分隔 */
export function parseIdentifiers(text: string): string[] {
  const out: string[] = []
  for (const raw of text.split(/[\s,，;；]+/)) {
    const id = raw.trim().toLowerCase().replace(/\.$/, '')
    if (id && !out.includes(id)) out.push(id)
  }
  return out
}

export const WILDCARD_WARNING =
  '通配符证书会被所有选用它的服务器共用同一把私钥：任何一台服务器失陷，这把私钥就要当作已泄露。服务器退役或怀疑失陷时，点「立即续期」换一张新证书（每次签发都换新私钥）。只给一台服务器用时，签单独的主机名证书更稳妥。'

/** 表单提示：通配符共享私钥、域名不在凭据的 zone 下（要配 CNAME 委托才能签） */
export function identifierNotices(ids: readonly string[], zone: string | null): string[] {
  const notes: string[] = []
  if (ids.some((id) => id.startsWith('*.'))) notes.push(WILDCARD_WARNING)
  if (zone) {
    const outside = ids.filter((id) => {
      const name = id.replace(/^\*\./, '')
      return name !== zone && !name.endsWith(`.${zone}`)
    })
    if (outside.length > 0) {
      notes.push(`${outside.join('、')} 不在凭据的域名 ${zone} 下：只有把它的 _acme-challenge 用 CNAME 委托到 ${zone} 时才能签发。`)
    }
  }
  return notes
}

// ---------------------------------------------------------------------------
// DNS 凭据表单 → 请求体（只写不读：编辑时空着的密钥字段不带，后端保留原值）
// ---------------------------------------------------------------------------
export interface CredentialForm {
  name: string
  provider: Provider
  zone: string
  secret: Record<string, string>
}

export function emptyCredentialForm(provider: Provider = 'cloudflare'): CredentialForm {
  return { name: '', provider, zone: '', secret: {} }
}

export function credentialFormFrom(c: DnsCredential): CredentialForm {
  return { name: c.name, provider: c.provider, zone: c.zone, secret: {} }
}

export function filledSecret(form: CredentialForm): Record<string, string> {
  const out: Record<string, string> = {}
  for (const f of PROVIDER_META[form.provider].fields) {
    const v = (form.secret[f.key] ?? '').trim()
    if (v) out[f.key] = v
  }
  return out
}

export function createCredentialBody(form: CredentialForm): { body: object } | { errors: Record<string, string> } {
  const errors: Record<string, string> = {}
  if (!form.name.trim()) errors.name = '填写名称'
  if (!form.zone.trim()) errors.zone = '填写这个凭据管理的域名，例如 example.com'
  const secret = filledSecret(form)
  for (const f of PROVIDER_META[form.provider].fields) if (!secret[f.key]) errors[`secret.${f.key}`] = `填写${f.label}`
  if (Object.keys(errors).length > 0) return { errors }
  return { body: { name: form.name.trim(), provider: form.provider, zone: form.zone.trim(), secret } }
}

/** 编辑：只带改了的字段；密钥字段留空表示不改 */
export function patchCredentialBody(form: CredentialForm, saved: DnsCredential): object | null {
  const body: Record<string, unknown> = {}
  if (form.name.trim() !== saved.name) body.name = form.name.trim()
  if (form.zone.trim() !== saved.zone) body.zone = form.zone.trim()
  const secret = filledSecret(form)
  if (Object.keys(secret).length > 0) body.secret = secret
  return Object.keys(body).length > 0 ? body : null
}

export const VERIFY_VIEW: Readonly<Record<DnsCredential['verify_status'], View>> = {
  unverified: { label: '未校验', tone: 'neutral' },
  ok: { label: '校验通过', tone: 'ok' },
  error: { label: '校验失败', tone: 'danger' },
}

// ---------------------------------------------------------------------------
// ACME 设置
// ---------------------------------------------------------------------------
export interface AcmeForm {
  contactEmail: string
  useStaging: boolean
  zerosslEnabled: boolean
  eabKid: string
  /** 留空不改 */
  eabHmac: string
  clearHmac: boolean
}

export function acmeFormFrom(s: AcmeSettings): AcmeForm {
  return { contactEmail: s.contact_email, useStaging: s.use_staging, zerosslEnabled: s.zerossl_enabled, eabKid: s.zerossl_eab_kid, eabHmac: '', clearHmac: false }
}

export function acmeBody(form: AcmeForm, saved: AcmeSettings): { body: object } | { errors: Record<string, string> } {
  const errors: Record<string, string> = {}
  const email = form.contactEmail.trim()
  if (email && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) errors.contact_email = '邮箱格式不对'
  const hmacSet = form.eabHmac.trim() ? true : form.clearHmac ? false : saved.zerossl_eab_hmac_set
  if (form.zerosslEnabled) {
    if (!form.eabKid.trim()) errors.zerossl_eab_kid = '启用 ZeroSSL 要填 EAB KID'
    if (!hmacSet) errors.zerossl_eab_hmac = '启用 ZeroSSL 要填 EAB HMAC'
  }
  if (Object.keys(errors).length > 0) return { errors }
  const body: Record<string, unknown> = {
    contact_email: email,
    use_staging: form.useStaging,
    zerossl_enabled: form.zerosslEnabled,
    zerossl_eab_kid: form.eabKid.trim(),
  }
  if (form.eabHmac.trim()) body.zerossl_eab_hmac = form.eabHmac.trim()
  else if (form.clearHmac) body.zerossl_eab_hmac = ''
  return { body }
}
