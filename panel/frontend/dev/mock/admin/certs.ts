import { randomUUID } from 'node:crypto'
import type { Json, MockContext, MockModule, MockResult } from '../types.ts'

// ---------------------------------------------------------------------------
// 节点证书（Go：api/admin/certificates.go + domain/certs）。形状、错误码、fields 键名、中文文案照 Go 写；
// 签发由 Go 的 worker 异步完成，这里在下一次读列表时把排队的订单直接判成功（演示用）。
// 凭据「校验」：令牌 / 密钥里带 bad 的判为服务商拒绝，其余通过。
// ---------------------------------------------------------------------------
const DAY = 86_400_000
const err = (status: number, code: string, message: string, fields?: Record<string, string>): MockResult => ({ status, body: { error: { code, message, ...(fields ? { fields } : {}) } } })
const invalid = (fields: Record<string, string>) => err(422, 'validation_failed', '请求参数校验未通过', fields)
const NOT_FOUND = err(404, 'not_found', '资源不存在或无权访问')
const reply = (ctx: MockContext, r: MockResult) => ctx.send(r.status, r.body)
const iso = (ms: number) => new Date(ms).toISOString()

const FIELDS: Readonly<Record<string, readonly string[]>> = {
  cloudflare: ['api_token'],
  alidns: ['access_key_id', 'access_key_secret'],
  tencentcloud: ['secret_id', 'secret_key'],
}

interface Cred {
  id: string
  name: string
  provider: string
  zone: string
  secret: Record<string, string>
  verify_status: 'unverified' | 'ok' | 'error'
  verified_at: string | null
  verify_error: string | null
  visible_zones: number | null
  row_version: number
  created_at: string
  updated_at: string
}

interface Version {
  id: string
  version: number
  ca: string
  identifiers: string[]
  is_renewal: boolean
  serial: string
  not_before: string
  not_after: string
  created_at: string
}

interface Order {
  id: string
  reason: 'initial' | 'renewal' | 'ari' | 'manual'
  state: 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'
  ca: string | null
  replaces: boolean
  attempt: number
  error_code: string | null
  error_detail: string | null
  version: number | null
  created_at: string
  started_at: string | null
  finished_at: string | null
}

interface Cert {
  id: string
  name: string
  identifiers: string[]
  dns_credential_id: string
  key_type: string
  status: 'pending' | 'active' | 'paused' | 'blocked_credential'
  paused_reason: 'manual' | 'failures' | null
  consecutive_failures: number
  last_error_code: string | null
  last_error: string | null
  next_attempt_at: string | null
  versions: Version[]
  orders: Order[]
  row_version: number
  created_at: string
  updated_at: string
}

const now = Date.now()
const creds: Cred[] = [
  { id: '0199f000-0000-7000-8000-00000000c001', name: '主域名', provider: 'cloudflare', zone: 'example.com', secret: { api_token: 'mock-token-ab12' }, verify_status: 'ok', verified_at: iso(now - 3 * DAY), verify_error: null, visible_zones: 1, row_version: 1, created_at: iso(now - 40 * DAY), updated_at: iso(now - 3 * DAY) },
  { id: '0199f000-0000-7000-8000-00000000c002', name: '备用域名', provider: 'alidns', zone: 'example.net', secret: { access_key_id: 'LTAImock9f3c', access_key_secret: 'x' }, verify_status: 'error', verified_at: null, verify_error: '阿里云拒绝了这个 AccessKey（InvalidAccessKeyId.NotFound）', visible_zones: null, row_version: 2, created_at: iso(now - 10 * DAY), updated_at: iso(now - DAY) },
]

const version = (n: number, ids: string[], start: number, days: number, renewal: boolean): Version => ({
  id: randomUUID(), version: n, ca: 'letsencrypt', identifiers: ids, is_renewal: renewal, serial: (0x4a1f00 + n).toString(16),
  not_before: iso(start), not_after: iso(start + days * DAY), created_at: iso(start),
})
const done = (reason: Order['reason'], at: number, v: number | null, code: string | null = null, detail: string | null = null): Order => ({
  id: randomUUID(), reason, state: code ? 'failed' : 'succeeded', ca: 'letsencrypt', replaces: reason !== 'initial', attempt: 1,
  error_code: code, error_detail: detail, version: v, created_at: iso(at), started_at: iso(at), finished_at: iso(at + 60_000),
})

const certs: Cert[] = [
  {
    id: '0199f000-0000-7000-8000-0000000ce001', name: '主域名通配符', identifiers: ['*.example.com', 'example.com'], dns_credential_id: creds[0]!.id, key_type: 'ecdsa-p256',
    status: 'active', paused_reason: null, consecutive_failures: 0, last_error_code: null, last_error: null, next_attempt_at: null,
    versions: [version(2, ['*.example.com', 'example.com'], now - 20 * DAY, 90, true), version(1, ['*.example.com', 'example.com'], now - 80 * DAY, 90, false)],
    orders: [done('ari', now - 20 * DAY, 2), done('initial', now - 80 * DAY, 1)], row_version: 3, created_at: iso(now - 80 * DAY), updated_at: iso(now - 20 * DAY),
  },
  {
    id: '0199f000-0000-7000-8000-0000000ce002', name: '香港节点', identifiers: ['hk1.example.com'], dns_credential_id: creds[0]!.id, key_type: 'ecdsa-p256',
    status: 'active', paused_reason: null, consecutive_failures: 1, last_error_code: 'dns_propagation_timeout', last_error: 'DNS 记录在 4 分钟内没有生效', next_attempt_at: iso(now + 3_600_000),
    versions: [version(1, ['hk1.example.com'], now - 80 * DAY, 90, false)],
    orders: [done('renewal', now - 3_600_000, null, 'dns_propagation_timeout', 'DNS 记录在 4 分钟内没有生效'), done('initial', now - 80 * DAY, 1)], row_version: 2, created_at: iso(now - 80 * DAY), updated_at: iso(now - 3_600_000),
  },
  {
    id: '0199f000-0000-7000-8000-0000000ce003', name: '备用站', identifiers: ['*.example.net'], dns_credential_id: creds[1]!.id, key_type: 'ecdsa-p256',
    status: 'blocked_credential', paused_reason: null, consecutive_failures: 0, last_error_code: 'credential_rejected', last_error: '阿里云拒绝了这个 AccessKey（InvalidAccessKeyId.NotFound）', next_attempt_at: null,
    versions: [], orders: [done('initial', now - DAY, null, 'credential_rejected', '阿里云拒绝了这个 AccessKey（InvalidAccessKeyId.NotFound）')], row_version: 2, created_at: iso(now - 2 * DAY), updated_at: iso(now - DAY),
  },
]

let acme = { contact_email: '', use_staging: false, zerossl_enabled: false, zerossl_eab_kid: '', hmac: '' }

// ---------------------------------------------------------------------------
// 读形状（照 Go 的结构体）
// ---------------------------------------------------------------------------
function expiryLevel(nb: string | null, na: string | null): string {
  if (!nb || !na) return 'none'
  const remaining = Date.parse(na) - Date.now()
  const life = Date.parse(na) - Date.parse(nb)
  if (remaining <= 0) return 'expired'
  if (remaining < Math.min(3 * DAY, life / 10)) return 'critical'
  if (remaining < Math.min(14 * DAY, life / 4)) return 'warning'
  return 'ok'
}

/** 演示：排队的订单下一次读就算签好 */
function settle(c: Cert) {
  for (const o of c.orders) {
    if (o.state !== 'queued') continue
    const prev = c.versions[0]
    const ids = c.identifiers
    const v = version((prev?.version ?? 0) + 1, ids, Date.now(), 90, prev !== undefined && prev.identifiers.join(',') === ids.join(','))
    c.versions.unshift(v)
    Object.assign(o, { state: 'succeeded', ca: 'letsencrypt', attempt: 1, version: v.version, started_at: iso(Date.now()), finished_at: iso(Date.now()) })
    Object.assign(c, { status: c.status === 'pending' ? 'active' : c.status, consecutive_failures: 0, last_error_code: null, last_error: null, next_attempt_at: null })
  }
}

function credView(c: Cred): Json {
  const first = c.secret[FIELDS[c.provider]![0]!] ?? ''
  const rest: Json = { ...c }
  delete rest.secret
  return { ...rest, secret_hint: first.length > 4 ? first.slice(-4) : '', certificate_count: certs.filter((x) => x.dns_credential_id === c.id).length }
}

function certView(c: Cert): Json {
  settle(c)
  const cur = c.versions[0] ?? null
  const cred = creds.find((d) => d.id === c.dns_credential_id)!
  const active = c.orders.find((o) => o.state === 'queued' || o.state === 'running') ?? null
  const life = cur ? Date.parse(cur.not_after) - Date.parse(cur.not_before) : 0
  return {
    id: c.id, name: c.name, identifiers: c.identifiers, wildcard: c.identifiers.some((i) => i.startsWith('*.')), challenge: 'dns-01',
    dns_credential_id: c.dns_credential_id, dns_credential_name: cred.name, dns_provider: cred.provider, key_type: c.key_type,
    status: c.status, paused_reason: c.paused_reason, current_version: cur?.version ?? null, current_ca: cur?.ca ?? null,
    not_before: cur?.not_before ?? null, not_after: cur?.not_after ?? null, renew_after: cur ? iso(Date.parse(cur.not_after) - life / 3) : null,
    ari_window_start: null, ari_window_end: null, next_attempt_at: c.next_attempt_at, consecutive_failures: c.consecutive_failures,
    last_error_code: c.last_error_code, last_error: c.last_error, expiry_level: expiryLevel(cur?.not_before ?? null, cur?.not_after ?? null),
    active_order: active ? { id: active.id, state: active.state, reason: active.reason, created_at: active.created_at } : null,
    row_version: c.row_version, created_at: c.created_at, updated_at: c.updated_at,
  }
}

function queue(c: Cert, reason: Order['reason']): Order {
  const open = c.orders.find((o) => o.state === 'queued' || o.state === 'running')
  if (open) return open
  const o: Order = { id: randomUUID(), reason, state: 'queued', ca: null, replaces: reason !== 'initial', attempt: 0, error_code: null, error_detail: null, version: null, created_at: iso(Date.now()), started_at: null, finished_at: null }
  c.orders.unshift(o)
  return o
}

function verify(c: Cred): Json {
  const bad = Object.values(c.secret).some((v) => v.includes('bad'))
  c.verify_status = bad ? 'error' : 'ok'
  c.verify_error = bad ? '服务商拒绝了这个凭据' : null
  c.verified_at = bad ? c.verified_at : iso(Date.now())
  c.visible_zones = bad ? c.visible_zones : 1
  let resumed = 0
  for (const x of certs.filter((x) => x.dns_credential_id === c.id)) {
    if (bad && (x.status === 'pending' || x.status === 'active')) x.status = 'blocked_credential'
    if (!bad && x.status === 'blocked_credential') {
      x.status = x.versions.length > 0 ? 'active' : 'pending'
      x.next_attempt_at = null
      resumed++
    }
  }
  return { credential: credView(c), ok: !bad, message: bad ? '服务商拒绝了这个凭据' : '', warnings: [], resumed }
}

// ---------------------------------------------------------------------------
// 校验（照 domain/certs 的 normalizeIdentifiers / mergeSecret）
// ---------------------------------------------------------------------------
const LABEL = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/
function normalizeIds(raw: unknown): string[] | string {
  if (!Array.isArray(raw)) return '至少填一个域名'
  const out: string[] = []
  for (const r of raw) {
    const id = String(r).trim().toLowerCase().replace(/\.$/, '')
    if (!id) continue
    if (/^[\d.]+$/.test(id) || id.includes(':')) return `不支持 IP 证书：${id}`
    const name = id.startsWith('*.') ? id.slice(2) : id
    const labels = name.split('.')
    if (labels.length < 2 || !labels.every((l) => LABEL.test(l))) return `域名格式不对（只能有字母、数字、连字符，通配符只能在最左边）：${id}`
    if (!out.includes(id)) out.push(id)
  }
  if (out.length === 0) return '至少填一个域名'
  if (out.length > 20) return '一张证书最多 20 个域名'
  return out.sort()
}

function mergeSecret(provider: string, old: Record<string, string>, given: unknown): Record<string, string> | Record<string, string>[] {
  const fields = FIELDS[provider]
  if (!fields) return [{ provider: '不支持的 DNS 提供方' }]
  const input = (given ?? {}) as Record<string, unknown>
  const bad: Record<string, string> = {}
  const out: Record<string, string> = {}
  for (const k of Object.keys(input)) if (!fields.includes(k)) bad[`secret.${k}`] = '这个提供方没有这个字段'
  for (const f of fields) {
    const v = typeof input[f] === 'string' ? (input[f] as string).trim() : undefined
    if (v === '') bad[`secret.${f}`] = '不能为空'
    else if (v !== undefined) out[f] = v
    else if (old[f]) out[f] = old[f]!
    else bad[`secret.${f}`] = '必填'
  }
  return Object.keys(bad).length > 0 ? [bad] : out
}

/** httpx.DecodeJSON：非法 JSON 与未知字段都回 400 */
async function readBody(ctx: MockContext, allowed: readonly string[]): Promise<{ ok: true; body: Json } | { ok: false; result: MockResult }> {
  const body = await ctx.body()
  const bad = { ok: false as const, result: err(400, 'bad_request', '请求体不是合法的 JSON') }
  if (!body) return bad
  for (const k of Object.keys(body)) if (!allowed.includes(k)) return bad
  return { ok: true, body }
}

const findCert = (id: string) => certs.find((c) => c.id === id)
const findCred = (id: string) => creds.find((c) => c.id === id)

export const certsModule: MockModule = {
  routes: {
    'GET /v1/certificates': (ctx) => {
      if (!ctx.requirePermission('node.certificate.read')) return
      const items = [...certs].sort((a, b) => a.name.localeCompare(b.name)).map(certView)
      const summary = { total: items.length, warning: 0, critical: 0, expired: 0, failing: 0, paused: 0, blocked: 0 }
      for (const c of items) {
        if (c.expiry_level === 'warning') summary.warning++
        if (c.expiry_level === 'critical') summary.critical++
        if (c.expiry_level === 'expired') summary.expired++
        if ((c.consecutive_failures as number) > 0 || c.status === 'blocked_credential') summary.failing++
        if (c.status === 'paused') summary.paused++
        if (c.status === 'blocked_credential') summary.blocked++
      }
      ctx.send(200, { items, summary })
    },
    'GET /v1/certificates/:id': (ctx) => {
      if (!ctx.requirePermission('node.certificate.read')) return
      const c = findCert(ctx.params.id!)
      if (!c) return reply(ctx, NOT_FOUND)
      const view = certView(c)
      ctx.send(200, {
        certificate: view,
        versions: c.versions.map((v, i) => ({ ...v, chain_sha256: 'ab'.repeat(32), public_key_sha256: 'cd'.repeat(32), current: i === 0 })),
        orders: c.orders.slice(0, 50),
      })
    },
    'POST /v1/certificates': async (ctx) => {
      if (!ctx.requirePermission('node.certificate.write')) return
      await ctx.idempotent('certificate_create', async () => {
        const read = await readBody(ctx, ['name', 'identifiers', 'dns_credential_id', 'key_type'])
        if (!read.ok) return read.result
        const body = read.body
        const bad: Record<string, string> = {}
        const name = String(body.name ?? '').trim()
        if (!name) bad.name = '填写名称'
        if (certs.some((c) => c.name.toLowerCase() === name.toLowerCase())) bad.name = '已有同名的证书'
        const ids = normalizeIds(body.identifiers)
        if (typeof ids === 'string') bad.identifiers = ids
        const keyType = String(body.key_type ?? '') || 'ecdsa-p256'
        if (keyType !== 'ecdsa-p256' && keyType !== 'rsa-2048') bad.key_type = '密钥类型只能是 ecdsa-p256 或 rsa-2048'
        if (!findCred(String(body.dns_credential_id ?? ''))) bad.dns_credential_id = '选一个 DNS 凭据'
        if (Object.keys(bad).length > 0) return invalid(bad)
        const c: Cert = {
          id: randomUUID(), name, identifiers: ids as string[], dns_credential_id: String(body.dns_credential_id), key_type: keyType, status: 'pending', paused_reason: null,
          consecutive_failures: 0, last_error_code: null, last_error: null, next_attempt_at: null, versions: [], orders: [], row_version: 1, created_at: iso(Date.now()), updated_at: iso(Date.now()),
        }
        queue(c, 'initial')
        certs.push(c)
        const view = { ...certView(c) }
        return { status: 201, body: view }
      })
    },
    'PATCH /v1/certificates/:id': async (ctx) => {
      if (!ctx.requirePermission('node.certificate.write')) return
      const c = findCert(ctx.params.id!)
      if (!c) return reply(ctx, NOT_FOUND)
      const read = await readBody(ctx, ['name', 'identifiers', 'dns_credential_id', 'key_type'])
      if (!read.ok) return reply(ctx, read.result)
      const body = read.body
      const bad: Record<string, string> = {}
      if (body.name !== undefined && !String(body.name).trim()) bad.name = '填写名称'
      const ids = body.identifiers !== undefined ? normalizeIds(body.identifiers) : c.identifiers
      if (typeof ids === 'string') bad.identifiers = ids
      if (body.dns_credential_id !== undefined && !findCred(String(body.dns_credential_id))) bad.dns_credential_id = '选一个 DNS 凭据'
      if (Object.keys(bad).length > 0) return reply(ctx, invalid(bad))
      const reissue = (ids as string[]).join(',') !== c.identifiers.join(',') || (body.key_type !== undefined && body.key_type !== c.key_type)
      if (body.name !== undefined) c.name = String(body.name).trim()
      c.identifiers = ids as string[]
      if (body.dns_credential_id !== undefined) c.dns_credential_id = String(body.dns_credential_id)
      if (body.key_type !== undefined) c.key_type = String(body.key_type)
      c.row_version++
      if (reissue && (c.status === 'active' || c.status === 'pending')) queue(c, 'manual')
      ctx.send(200, certView(c))
    },
    'DELETE /v1/certificates/:id': (ctx) => {
      if (!ctx.requirePermission('node.certificate.write') || !ctx.requireReauth()) return
      const i = certs.findIndex((c) => c.id === ctx.params.id)
      if (i < 0) return reply(ctx, NOT_FOUND)
      certs.splice(i, 1)
      ctx.send(204)
    },
    'POST /v1/certificates/:id/renew': (ctx) => {
      if (!ctx.requirePermission('node.certificate.write')) return
      const c = findCert(ctx.params.id!)
      if (!c) return reply(ctx, NOT_FOUND)
      if (c.status === 'paused') return reply(ctx, err(409, 'conflict', '证书已暂停自动签发，先点「恢复」'))
      if (c.status === 'blocked_credential') return reply(ctx, err(409, 'conflict', '证书的 DNS 凭据校验没通过，先去「DNS 凭据」修好并重新校验'))
      const o = queue(c, 'manual')
      ctx.send(202, { id: o.id, state: o.state, reason: o.reason, created_at: o.created_at })
    },
    'POST /v1/certificates/:id/pause': (ctx) => {
      if (!ctx.requirePermission('node.certificate.write')) return
      const c = findCert(ctx.params.id!)
      if (!c) return reply(ctx, NOT_FOUND)
      if (c.status === 'paused') return reply(ctx, err(409, 'conflict', '证书已经是暂停状态'))
      Object.assign(c, { status: 'paused', paused_reason: 'manual' })
      for (const o of c.orders) if (o.state === 'queued') Object.assign(o, { state: 'cancelled', finished_at: iso(Date.now()), error_code: 'paused' })
      ctx.send(200, certView(c))
    },
    'POST /v1/certificates/:id/resume': (ctx) => {
      if (!ctx.requirePermission('node.certificate.write')) return
      const c = findCert(ctx.params.id!)
      if (!c) return reply(ctx, NOT_FOUND)
      if (c.status !== 'paused') return reply(ctx, err(409, 'conflict', '证书没有暂停'))
      Object.assign(c, { status: c.versions.length > 0 ? 'active' : 'pending', paused_reason: null, consecutive_failures: 0, next_attempt_at: null })
      queue(c, c.versions.length > 0 ? 'manual' : 'initial')
      ctx.send(200, certView(c))
    },

    'GET /v1/dns-credentials': (ctx) => {
      if (!ctx.requirePermission('node.certificate.read')) return
      ctx.send(200, { items: [...creds].sort((a, b) => a.name.localeCompare(b.name)).map(credView) })
    },
    'POST /v1/dns-credentials': async (ctx) => {
      if (!ctx.requirePermission('node.certificate.write') || !ctx.requireReauth()) return
      await ctx.idempotent('dns_credential_create', async () => {
        const read = await readBody(ctx, ['name', 'provider', 'zone', 'secret'])
        if (!read.ok) return read.result
        const body = read.body
        const bad: Record<string, string> = {}
        const name = String(body.name ?? '').trim()
        if (!name) bad.name = '填写名称'
        if (creds.some((c) => c.name.toLowerCase() === name.toLowerCase())) bad.name = '已有同名的 DNS 凭据'
        const zone = String(body.zone ?? '').trim().toLowerCase().replace(/\.$/, '')
        if (!zone) bad.zone = '填写这个凭据管理的域名（zone），例如 example.com'
        const secret = mergeSecret(String(body.provider ?? ''), {}, body.secret)
        if (Array.isArray(secret)) Object.assign(bad, secret[0])
        if (Object.keys(bad).length > 0) return invalid(bad)
        const c: Cred = { id: randomUUID(), name, provider: String(body.provider), zone, secret: secret as Record<string, string>, verify_status: 'unverified', verified_at: null, verify_error: null, visible_zones: null, row_version: 1, created_at: iso(Date.now()), updated_at: iso(Date.now()) }
        creds.push(c)
        return { status: 201, body: verify(c) }
      })
    },
    'PATCH /v1/dns-credentials/:id': async (ctx) => {
      if (!ctx.requirePermission('node.certificate.write') || !ctx.requireReauth()) return
      const c = findCred(ctx.params.id!)
      if (!c) return reply(ctx, NOT_FOUND)
      const read = await readBody(ctx, ['name', 'zone', 'secret'])
      if (!read.ok) return reply(ctx, read.result)
      const body = read.body
      const bad: Record<string, string> = {}
      if (body.name !== undefined && !String(body.name).trim()) bad.name = '填写名称'
      const secret = body.secret !== undefined && Object.keys(body.secret as object).length > 0 ? mergeSecret(c.provider, c.secret, body.secret) : null
      if (Array.isArray(secret)) Object.assign(bad, secret[0])
      if (Object.keys(bad).length > 0) return reply(ctx, invalid(bad))
      const zone = body.zone !== undefined ? String(body.zone).trim().toLowerCase() : c.zone
      const reverify = secret !== null || zone !== c.zone
      if (body.name !== undefined) c.name = String(body.name).trim()
      c.zone = zone
      if (secret) c.secret = secret as Record<string, string>
      c.row_version++
      ctx.send(200, reverify ? verify(c) : { credential: credView(c), ok: c.verify_status === 'ok', message: '', warnings: [], resumed: 0 })
    },
    'DELETE /v1/dns-credentials/:id': (ctx) => {
      if (!ctx.requirePermission('node.certificate.write') || !ctx.requireReauth()) return
      const c = findCred(ctx.params.id!)
      if (!c) return reply(ctx, NOT_FOUND)
      const using = certs.filter((x) => x.dns_credential_id === c.id).map((x) => x.name)
      if (using.length > 0) return reply(ctx, err(409, 'conflict', `还有证书在用这个凭据（${using.slice(0, 5).join('、')}），先改用别的凭据或删掉这些证书`))
      creds.splice(creds.indexOf(c), 1)
      ctx.send(204)
    },
    'POST /v1/dns-credentials/:id/verify': (ctx) => {
      if (!ctx.requirePermission('node.certificate.write')) return
      const c = findCred(ctx.params.id!)
      if (!c) return reply(ctx, NOT_FOUND)
      ctx.send(200, verify(c))
    },

    'GET /v1/settings/acme': (ctx) => {
      if (!ctx.requirePermission('node.certificate.read')) return
      const { hmac, ...rest } = acme
      ctx.send(200, { ...rest, zerossl_eab_hmac_set: hmac !== '', directory_override: false })
    },
    'PUT /v1/settings/acme': async (ctx) => {
      if (!ctx.requirePermission('node.certificate.write') || !ctx.requireReauth()) return
      const read = await readBody(ctx, ['contact_email', 'use_staging', 'zerossl_enabled', 'zerossl_eab_kid', 'zerossl_eab_hmac'])
      if (!read.ok) return reply(ctx, read.result)
      const body = read.body
      const hmac = typeof body.zerossl_eab_hmac === 'string' ? body.zerossl_eab_hmac.trim() : acme.hmac
      const kid = String(body.zerossl_eab_kid ?? '').trim()
      const bad: Record<string, string> = {}
      const email = String(body.contact_email ?? '').trim()
      if (email && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) bad.contact_email = '邮箱格式不对'
      if (body.zerossl_enabled === true && !kid) bad.zerossl_eab_kid = '启用 ZeroSSL 要填 EAB KID'
      if (body.zerossl_enabled === true && !hmac) bad.zerossl_eab_hmac = '启用 ZeroSSL 要填 EAB HMAC'
      if (Object.keys(bad).length > 0) return reply(ctx, invalid(bad))
      acme = { contact_email: email, use_staging: body.use_staging === true, zerossl_enabled: body.zerossl_enabled === true, zerossl_eab_kid: kid, hmac }
      const { hmac: h, ...rest } = acme
      ctx.send(200, { ...rest, zerossl_eab_hmac_set: h !== '', directory_override: false })
    },
  },
}
