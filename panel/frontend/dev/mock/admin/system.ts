/**
 * [INPUT]: 依赖 node:crypto 的 randomBytes / randomUUID，依赖 ../types 的 Json / MockContext / MockModule / MockResult
 * [OUTPUT]: 对外提供 system 模块的假接口 MockModule
 * [POS]: dev/mock/admin 的「通知与插件（后台-09 前半）」假接口，归后台前端二；形状、权限、reauth、幂等与文案照 api-contract.md（含 R14 R20 R21 R45 R59）与 Go 的 mail.go、telegram.go、mail_template.go、notify/template_admin.go、plugin/hooks.go：
 *        邮件设置（SMTP 六字段整体覆盖、密码空不改 / "-" 清空、from_name 空时回站点名、开启邮箱验证要 host 与发件人）与测试发送（要已保存的 host + 发件人，收件地址含 fail 时模拟 SMTP 报错）；Telegram（Token 只进不出、admin_chat_id 缺省不改 / null 清空、测试缺 chat 回 fields.chat_id）；
 *        12 个模板（种子与 Go 一致：code / 渠道 / 变量白名单 / 有无内置默认），保存校验长度与变量白名单、恢复默认、草稿预览（示例值与 RenderPreview 同表）、测试信（reauth，只限邮件，可发草稿）；钩子按 code upsert（省略即零值、新建无密钥时生成 whsec_ 一次性回传、https 与内网地址校验、超时 / 次数越界模拟 DB CHECK 的 500）、删除、投递记录（无记录 null）、测试投递（地址含 fail 时对方回 503）。按 DisallowUnknownFields 拒绝未知字段
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomBytes, randomUUID } from 'node:crypto'
import type { Json, MockContext, MockModule, MockResult } from '../types.ts'

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------
const err = (status: number, code: string, message: string, fields?: Record<string, string>): MockResult => ({ status, body: { error: { code, message, ...(fields ? { fields } : {}) } } })
const invalid = (fields: Record<string, string>) => err(422, 'validation_failed', '请求参数校验未通过', fields)
const failed = (message: string) => err(422, 'validation_failed', message)
const reply = (ctx: MockContext, r: MockResult) => ctx.send(r.status, r.body)
const str = (v: unknown) => (typeof v === 'string' ? v : '')

type Shape = Record<string, 'string' | 'boolean' | 'number' | 'strings' | 'any'>
type Decoded = { ok: true; body: Json } | { ok: false; result: MockResult }
/** httpx.DecodeJSON：非法 JSON、未知字段、类型不符都回 400 */
async function decode(ctx: MockContext, shape: Shape): Promise<Decoded> {
  const bad = (message: string): Decoded => ({ ok: false, result: err(400, 'bad_request', message) })
  const body = await ctx.body()
  if (!body) return bad('请求体不是合法的 JSON')
  for (const [k, v] of Object.entries(body)) {
    const want = shape[k]
    if (!want) return bad(`请求体包含未知字段 "${k}"`)
    if (want === 'any') continue
    const ok = want === 'strings' ? v === null || (Array.isArray(v) && v.every((x) => typeof x === 'string')) : v === null || typeof v === want
    if (!ok) return bad(`字段 "${k}" 类型不正确`)
  }
  return { ok: true, body }
}
const stamp = (offsetMin = 0, seconds = false) => {
  const d = new Date(Date.now() + offsetMin * 60_000)
  const p = (n: number) => String(n).padStart(2, '0')
  const s = `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
  return seconds ? `${s}:${p(d.getSeconds())}` : s
}

// ---------------------------------------------------------------------------
// 邮件与注册（mail.go）
// ---------------------------------------------------------------------------
const SITE_NAME = 'Pandora'
const mail = {
  smtp_host: 'smtp.exmail.qq.com',
  smtp_port: 465,
  encryption: 'ssl',
  smtp_username: 'noreply@pandora.run',
  password: 'secret',
  from_address: 'noreply@pandora.run',
  from_name: '',
  email_verification: false,
  registration_mode: 'invite_only',
}
const mailJson = () => ({
  smtp_host: mail.smtp_host,
  smtp_port: mail.smtp_port,
  encryption: mail.encryption,
  smtp_username: mail.smtp_username,
  has_password: mail.password !== '',
  from_address: mail.from_address,
  from_name: mail.from_name.trim() || SITE_NAME,
  email_verification: mail.email_verification,
  registration_mode: mail.registration_mode,
})

async function saveMail(ctx: MockContext): Promise<MockResult> {
  const d = await decode(ctx, { smtp_host: 'string', smtp_port: 'number', encryption: 'string', smtp_username: 'string', smtp_password: 'string', from_address: 'string', from_name: 'string', email_verification: 'boolean', registration_mode: 'string' })
  if (!d.ok) return d.result
  const b = d.body
  const port = typeof b.smtp_port === 'number' ? b.smtp_port : 0
  const fields: Record<string, string> = {}
  if (!Number.isInteger(port) || port < 1 || port > 65535) fields.smtp_port = '端口需在 1 到 65535 之间'
  if (!['ssl', 'tls', 'none'].includes(str(b.encryption))) fields.encryption = '加密方式只能是 ssl / tls / none'
  if (b.registration_mode !== undefined && b.registration_mode !== null && !['closed', 'invite_only', 'open'].includes(str(b.registration_mode))) fields.registration_mode = '注册模式只能是 closed / invite_only / open'
  if (b.email_verification === true && (!str(b.smtp_host) || !str(b.from_address))) fields.email_verification = '开启邮箱验证前请先填好 SMTP 服务器与发件人地址'
  if (Object.keys(fields).length > 0) return invalid(fields)
  Object.assign(mail, { smtp_host: str(b.smtp_host), smtp_port: port, encryption: str(b.encryption), smtp_username: str(b.smtp_username), from_address: str(b.from_address), from_name: str(b.from_name) })
  if (typeof b.registration_mode === 'string') mail.registration_mode = b.registration_mode
  if (typeof b.email_verification === 'boolean') mail.email_verification = b.email_verification
  const pw = str(b.smtp_password)
  if (pw) mail.password = pw === '-' ? '' : pw
  return { status: 200, body: { ok: true } }
}

// ---------------------------------------------------------------------------
// Telegram（telegram.go）
// ---------------------------------------------------------------------------
const telegram = { enabled: true, bot_username: 'pandora_notify_bot', token: '7012345678:AAH-mock', admin_chat_id: -1002231180042 as number | null }

async function saveTelegram(ctx: MockContext): Promise<MockResult> {
  const d = await decode(ctx, { enabled: 'boolean', bot_username: 'string', bot_token: 'string', admin_chat_id: 'any' })
  if (!d.ok) return d.result
  const b = d.body
  const username = str(b.bot_username).trim().replace(/^@/, '')
  if (b.enabled === true && !username) return invalid({ bot_username: '启用前要填 Bot 用户名，用户要靠它找到你的 bot' })
  let chat: number | null | undefined
  if ('admin_chat_id' in b) {
    if (b.admin_chat_id === null) chat = null
    else if (typeof b.admin_chat_id === 'number' && Number.isInteger(b.admin_chat_id) && b.admin_chat_id !== 0) chat = b.admin_chat_id
    else return invalid({ admin_chat_id: '管理员群组 chat id 必须是非 0 整数' })
  }
  telegram.enabled = b.enabled === true
  telegram.bot_username = username
  if (str(b.bot_token).trim()) telegram.token = str(b.bot_token).trim()
  if (chat !== undefined) telegram.admin_chat_id = chat
  return { status: 200, body: { ok: true, enabled: telegram.enabled } }
}

// ---------------------------------------------------------------------------
// 通知模板（种子与 Go 的迁移 / defaultTemplates 一致）
// ---------------------------------------------------------------------------
interface Tpl {
  code: string
  channel: string
  category: string
  version: number
  subject: string
  body: string
  vars: string[]
  updated_at: string
}
const DEFAULTS: Record<string, { subject: string; body: string }> = {
  'subscription.expiring|inapp': { subject: '套餐即将到期', body: '你的「{{plan}}」将在 {{days}} 天后（{{expires_at}}）到期。到期后节点会停止服务，记得及时续费。' },
  'subscription.expiring|email': { subject: '【{{site}}】你的套餐 {{days}} 天后到期', body: '你好，\n\n你的「{{plan}}」将在 {{days}} 天后到期（{{expires_at}}）。\n到期后节点将停止服务，请及时续费以免影响使用。\n\n{{site}}' },
  'quota.warning|inapp': { subject: '流量即将用尽', body: '你的「{{plan}}」已使用 {{percent}}% 流量（剩余 {{remaining}}）。用尽后将无法连接节点。' },
  'quota.warning|email': { subject: '【{{site}}】流量已使用 {{percent}}%', body: '你好，\n\n你的「{{plan}}」已使用 {{percent}}% 流量，剩余 {{remaining}}。\n流量用尽后将无法连接节点，可在面板购买流量包或升级套餐。\n\n{{site}}' },
  'order.paid|inapp': { subject: '支付成功', body: '订单 {{order_no}} 已支付成功，「{{plan}}」已开通，有效期至 {{expires_at}}。' },
  'ticket.replied|inapp': { subject: '工单有新回复', body: '你的工单「{{subject}}」有新回复，点击查看。' },
  'auth.email_verify|email': { subject: '【{{site}}】注册验证码 {{code}}', body: '你好，\n\n你正在注册 {{site}}，验证码是：\n\n{{code}}\n\n验证码 {{minutes}} 分钟内有效。如果这不是你本人的操作，忽略这封邮件即可。\n\n{{site}}' },
}
const DESCRIPTION: Record<string, string> = {
  'subscription.expiring': '套餐到期前提醒（由定时扫描触发，每个订阅每个提醒窗口只发一次）',
  'quota.warning': '流量用量预警（用量越过阈值时触发）',
  'order.paid': '订单支付成功后发给下单用户',
  'ticket.replied': '工单被管理员回复后通知提单人',
  'auth.email_verify': '注册第 1 步发给注册邮箱的验证码（开启邮箱验证时；邮箱已注册则不发）',
}
const SAMPLE: Record<string, string> = { site: '潘多拉面板', plan: '旗舰套餐', days: '3', expires_at: '2026-08-31 23:59', percent: '85', remaining: '3.2 GB', order_no: 'AO20260804-XXXXXX', subject: '无法连接节点' }
const tpl = (code: string, channel: string, category: string, vars: string[], override?: { subject: string; body: string }): Tpl => {
  const d = override ?? DEFAULTS[`${code}|${channel}`]!
  return { code, channel, category, version: override ? 3 : 1, subject: d.subject, body: d.body, vars, updated_at: new Date(Date.now() - 86_400_000 * 20).toISOString() }
}
const templates: Tpl[] = [
  tpl('admin.broadcast', 'email', 'marketing', ['subject', 'body'], { subject: '{{subject}}', body: '{{body}}' }),
  tpl('auth.email_verify', 'email', 'transactional', ['site', 'code', 'minutes']),
  tpl('order.paid', 'inapp', 'transactional', ['order_no', 'plan', 'expires_at'], { subject: '支付成功 🎉', body: '订单 {{order_no}} 已支付，「{{plan}}」已开通，有效期至 {{expires_at}}。感谢支持！' }),
  tpl('order.paid', 'telegram', 'transactional', ['order_no', 'plan', 'expires_at'], { subject: '支付成功', body: '订单 {{order_no}} 已支付，「{{plan}}」已开通，有效期至 {{expires_at}}。' }),
  tpl('quota.warning', 'email', 'service', ['site', 'plan', 'percent', 'remaining']),
  tpl('quota.warning', 'inapp', 'service', ['plan', 'percent', 'remaining']),
  tpl('quota.warning', 'telegram', 'service', ['plan', 'percent', 'remaining'], { subject: '流量预警', body: '你的「{{plan}}」已使用 {{percent}}% 流量，剩余 {{remaining}}。' }),
  tpl('subscription.expiring', 'email', 'service', ['site', 'plan', 'days', 'expires_at']),
  tpl('subscription.expiring', 'inapp', 'service', ['plan', 'days', 'expires_at']),
  tpl('subscription.expiring', 'telegram', 'service', ['plan', 'days', 'expires_at'], { subject: '套餐即将到期', body: '你的「{{plan}}」将在 {{days}} 天后（{{expires_at}}）到期，记得续费。' }),
  tpl('ticket.replied', 'inapp', 'service', ['subject']),
  tpl('ticket.replied', 'telegram', 'service', ['subject'], { subject: '工单有新回复', body: '你的工单「{{subject}}」有新回复。' }),
]

const VAR = /\{\{\s*([a-z_][a-z0-9_]*)\s*\}\}/g
const render = (text: string) => text.replace(VAR, (m, name: string) => SAMPLE[name] ?? m)
const unknownVars = (text: string, allowed: readonly string[]) => [...new Set([...text.matchAll(VAR)].map((m) => m[1]!).filter((n) => !allowed.includes(n)))].sort()
const isDefault = (t: Tpl) => {
  const d = DEFAULTS[`${t.code}|${t.channel}`]
  return !!d && d.subject === t.subject && d.body === t.body
}
const rowJson = (t: Tpl) => ({ code: t.code, channel: t.channel, locale: 'zh-CN', category: t.category, status: 'active', version: t.version, subject: t.subject, body: t.body, allowed_variables: t.vars, is_default: isDefault(t), updated_at: t.updated_at })
const find = (code: string, channel: string) => templates.find((t) => t.code === code && t.channel === channel)

/** SaveTemplate / RenderDraftForTest 同一套校验；返回错误或去空白后的草稿 */
function checkDraft(t: Tpl, subject: string, body: string): MockResult | { subject: string; body: string } {
  const s = subject.trim()
  const b = body.trim()
  if (!s || [...s].length > 200) return invalid({ subject: '主题必填，且不超过 200 字' })
  if (!b || [...b].length > 20000) return invalid({ body: '正文必填，且不超过 20000 字' })
  const bad = unknownVars(`${s}\n${b}`, t.vars)
  if (bad.length > 0) return invalid({ body: `用到了这个模板不提供的变量：${bad.join('、')}。可用变量：${t.vars.join('、')}` })
  return { subject: s, body: b }
}
const isResult = (v: object): v is MockResult => 'status' in v

// ---------------------------------------------------------------------------
// Webhook 钩子（plugin/hooks.go）
// ---------------------------------------------------------------------------
const EVENTS = [
  { name: 'user.registered', desc: '用户完成注册' },
  { name: 'order.created', desc: '订单创建' },
  { name: 'order.paid', desc: '订单支付成功' },
  { name: 'order.cancelled', desc: '订单取消' },
  { name: 'subscription.provisioned', desc: '订阅开通或续期' },
  { name: 'subscription.expiring', desc: '订阅即将到期' },
  { name: 'subscription.expired', desc: '订阅已过期' },
  { name: 'traffic.exhausted', desc: '流量用尽' },
  { name: 'ticket.created', desc: '用户提交工单' },
  { name: 'giftcard.redeemed', desc: '礼品卡兑换' },
]
interface MockDelivery {
  event: string
  status: 'queued' | 'sent' | 'failed'
  attempts: number
  response_code: number
  error_message: string
  created_at: string
  sent_at: string
  duration_ms: number | null
}
interface MockHook {
  id: string
  code: string
  name: string
  description: string
  enabled: boolean
  events: string[]
  endpoint_url: string
  secret: string
  timeout_ms: number
  max_attempts: number
  deliveries: MockDelivery[]
}
const sent = (event: string, min: number, ms: number, attempts = 1): MockDelivery => ({ event, status: 'sent', attempts, response_code: 200, error_message: '', created_at: stamp(-min, true), sent_at: stamp(-min, true), duration_ms: ms })
const lost = (event: string, min: number, code: number, attempts: number): MockDelivery => ({ event, status: 'failed', attempts, response_code: code, error_message: code ? `对方返回 HTTP ${code}` : 'dial tcp: i/o timeout', created_at: stamp(-min, true), sent_at: '', duration_ms: code ? 10000 : null })
const hooks: MockHook[] = [
  { id: randomUUID(), code: 'hook-crm001', name: 'crm.example.com', description: 'CRM 同步新用户', enabled: true, events: ['user.registered'], endpoint_url: 'https://crm.example.com/api/pandora', secret: 'whsec_crm', timeout_ms: 10000, max_attempts: 5, deliveries: [lost('user.registered', 14, 503, 3), sent('user.registered', 40, 240, 3), lost('user.registered', 90, 0, 5), sent('user.registered', 200, 180)] },
  { id: randomUUID(), code: 'hook-ops001', name: 'ops.pandora.run', description: '', enabled: true, events: ['order.paid', 'order.cancelled'], endpoint_url: 'https://ops.pandora.run/hooks/orders', secret: 'whsec_ops', timeout_ms: 5000, max_attempts: 5, deliveries: [sent('order.paid', 4, 84), sent('order.paid', 32, 91), sent('order.cancelled', 400, 77), { event: 'order.paid', status: 'queued', attempts: 0, response_code: 0, error_message: '', created_at: stamp(0, true), sent_at: '', duration_ms: null }] },
  { id: randomUUID(), code: 'hook-slack01', name: 'hooks.slack.com', description: '工单提醒', enabled: false, events: ['ticket.created'], endpoint_url: 'https://hooks.slack.com/services/T0/B0/mock', secret: '', timeout_ms: 5000, max_attempts: 3, deliveries: [] },
]
const recent = (d: MockDelivery) => Date.now() - new Date(d.created_at.replace(' ', 'T')).getTime() < 7 * 86_400_000
const hookJson = (h: MockHook) => {
  const last = h.deliveries.filter((d) => d.status === 'sent').map((d) => d.sent_at).sort().pop()
  return {
    id: h.id,
    code: h.code,
    name: h.name,
    description: h.description,
    enabled: h.enabled,
    events: h.events,
    endpoint_url: h.endpoint_url,
    has_secret: h.secret !== '',
    timeout_ms: h.timeout_ms,
    max_attempts: h.max_attempts,
    queued_count: h.deliveries.filter((d) => d.status === 'queued').length,
    failed_count: h.deliveries.filter((d) => d.status === 'failed' && recent(d)).length,
    sent_count_7d: h.deliveries.filter((d) => d.status === 'sent' && recent(d)).length,
    ...(last ? { last_sent_at: last.slice(0, 16) } : {}),
  }
}

const PRIVATE = /^(localhost|127\.|10\.|192\.168\.|169\.254\.|172\.(1[6-9]|2\d|3[01])\.|100\.(6[4-9]|[7-9]\d|1[01]\d|12[0-7])\.|0\.0\.0\.0|\[?::1\]?)/i
function endpointError(raw: string): string | null {
  if (!raw) return '回调地址必填'
  let u: URL
  try {
    u = new URL(raw)
  } catch {
    return '不是合法的地址'
  }
  if (u.protocol !== 'https:') return '必须使用 https'
  if (!u.hostname) return '地址里没有主机名'
  if (PRIVATE.test(u.hostname)) return '不允许指向内网或本机地址'
  return null
}

async function saveHook(ctx: MockContext): Promise<MockResult> {
  const d = await decode(ctx, { code: 'string', name: 'string', description: 'string', enabled: 'boolean', events: 'strings', endpoint_url: 'string', secret: 'string', timeout_ms: 'number', max_attempts: 'number' })
  if (!d.ok) return d.result
  const b = d.body
  const code = str(b.code).trim().toLowerCase()
  const name = str(b.name).trim()
  const url = str(b.endpoint_url).trim()
  const events = Array.isArray(b.events) ? (b.events as string[]) : []
  const enabled = b.enabled === true
  const fields: Record<string, string> = {}
  if (!code) fields.code = '插件标识必填，小写字母开头'
  if (!name) fields.name = '插件名称必填'
  const bad = endpointError(url)
  if (bad) fields.endpoint_url = bad
  for (const e of events) if (!EVENTS.some((x) => x.name === e)) fields.events = `未知事件：${e}`
  if (enabled && events.length === 0) fields.events = '启用前至少要订阅一个事件，否则这个钩子永远不会触发'
  if (Object.keys(fields).length > 0) return invalid(fields)
  const timeout = typeof b.timeout_ms === 'number' && b.timeout_ms !== 0 ? b.timeout_ms : 5000
  const attempts = typeof b.max_attempts === 'number' && b.max_attempts !== 0 ? b.max_attempts : 5
  // Go 不校验范围，靠 DB CHECK：越界就是 500
  if (timeout < 500 || timeout > 30000 || attempts < 1 || attempts > 10) return err(500, 'internal_error', '服务器内部错误')
  const existing = hooks.find((h) => h.code === code)
  let secret = str(b.secret)
  let generated = ''
  if (!secret && !existing) secret = generated = `whsec_${randomBytes(32).toString('base64url')}`
  const patch = { name, description: str(b.description), enabled, events, endpoint_url: url, timeout_ms: timeout, max_attempts: attempts }
  if (existing) Object.assign(existing, patch, secret ? { secret } : {})
  else hooks.push({ id: randomUUID(), code, secret, deliveries: [], ...patch })
  hooks.sort((x, y) => x.code.localeCompare(y.code))
  return { status: 200, body: { saved: true, ...(generated ? { secret: generated, secret_hint: '签名密钥只显示这一次，请立刻填进插件那边的配置' } : {}) } }
}

// ---------------------------------------------------------------------------
// 路由
// ---------------------------------------------------------------------------
export const system: MockModule = {
  routes: {
    'GET /v1/settings/mail': (ctx) => {
      if (!ctx.requirePermission('security.audit.read')) return
      ctx.send(200, mailJson())
    },
    'POST /v1/settings/mail/test': async (ctx) => {
      if (!ctx.requirePermission('ops.notification.write')) return
      const d = await decode(ctx, { to: 'string' })
      if (!d.ok) return reply(ctx, d.result)
      const to = str(d.body.to)
      if (!to) return reply(ctx, invalid({ to: '请填写收件地址' }))
      if (!mail.smtp_host || !mail.from_address) return reply(ctx, failed('SMTP 还没配置好：服务器地址、端口、发件人地址都要填'))
      if (to.includes('fail')) return reply(ctx, failed('发送失败：550 Mailbox not found'))
      ctx.send(200, { ok: true, to })
    },
    'POST /v1/settings/mail': async (ctx) => {
      if (!ctx.requirePermission('platform.settings.write')) return
      reply(ctx, await saveMail(ctx))
    },

    'GET /v1/settings/telegram': (ctx) => {
      if (!ctx.requirePermission('security.audit.read')) return
      ctx.send(200, { enabled: telegram.enabled, bot_username: telegram.bot_username, has_token: telegram.token !== '', admin_chat_id: telegram.admin_chat_id })
    },
    'POST /v1/settings/telegram/test': async (ctx) => {
      if (!ctx.requirePermission('ops.notification.write')) return
      const d = await decode(ctx, { chat_id: 'number' })
      if (!d.ok) return reply(ctx, d.result)
      const chat = typeof d.body.chat_id === 'number' && d.body.chat_id !== 0 ? d.body.chat_id : telegram.admin_chat_id
      if (!chat) return reply(ctx, invalid({ chat_id: '请填写要接收测试消息的 chat id，或先保存管理员群组 chat id' }))
      if (!telegram.enabled || !telegram.bot_username || !telegram.token) return reply(ctx, failed('Telegram 还没配置好：启用开关、Bot 用户名、Bot Token 都要有'))
      ctx.send(200, { sent: true })
    },
    'POST /v1/settings/telegram': async (ctx) => {
      if (!ctx.requirePermission('platform.settings.write')) return
      reply(ctx, await saveTelegram(ctx))
    },

    'GET /v1/mail/templates': (ctx) => {
      if (!ctx.requirePermission('ops.notification.read')) return
      const list = [...templates].sort((a, b) => a.code.localeCompare(b.code) || a.channel.localeCompare(b.channel))
      ctx.send(200, {
        templates: list.map((t) => ({ ...rowJson(t), has_default: `${t.code}|${t.channel}` in DEFAULTS, description: DESCRIPTION[t.code] ?? '', preview_subject: render(t.subject), preview_body: render(t.body) })),
      })
    },
    'POST /v1/mail/templates/preview': async (ctx) => {
      if (!ctx.requirePermission('ops.notification.read')) return
      const d = await decode(ctx, { code: 'string', channel: 'string', subject: 'string', body: 'string' })
      if (!d.ok) return reply(ctx, d.result)
      const b = d.body
      if (!str(b.code) || !str(b.channel)) return reply(ctx, err(400, 'bad_request', 'code 与 channel 必填'))
      const t = find(str(b.code), str(b.channel))
      if (!t) return reply(ctx, err(404, 'not_found', '资源不存在或无权访问'))
      ctx.send(200, { preview_subject: render(str(b.subject)), preview_body: render(str(b.body)), unknown_variables: unknownVars(`${str(b.subject)}\n${str(b.body)}`, t.vars) })
    },
    'POST /v1/mail/templates/reset': async (ctx) => {
      if (!ctx.requirePermission('platform.settings.write')) return
      const d = await decode(ctx, { code: 'string', channel: 'string' })
      if (!d.ok) return reply(ctx, d.result)
      const def = DEFAULTS[`${str(d.body.code)}|${str(d.body.channel)}`]
      if (!def) return reply(ctx, err(404, 'not_found', '这个模板没有内置默认内容'))
      const t = find(str(d.body.code), str(d.body.channel))!
      Object.assign(t, { subject: def.subject, body: def.body, version: t.version + 1, updated_at: new Date().toISOString() })
      ctx.send(200, { template: rowJson(t) })
    },
    'POST /v1/mail/templates/test': async (ctx) => {
      if (!ctx.requirePermission('ops.notification.write') || !ctx.requireReauth()) return
      const d = await decode(ctx, { code: 'string', channel: 'string', to: 'string', subject: 'string', body: 'string' })
      if (!d.ok) return reply(ctx, d.result)
      const b = d.body
      if (!str(b.to)) return reply(ctx, invalid({ to: '请填写收件地址' }))
      if (str(b.channel) !== 'email') return reply(ctx, failed('只有邮件模板可以测试发送，站内信请到用户端查看'))
      const t = find(str(b.code), str(b.channel))
      if (!t) return reply(ctx, err(404, 'not_found', '资源不存在或无权访问'))
      let subject = render(t.subject)
      if (b.subject !== undefined || b.body !== undefined) {
        const draft = checkDraft(t, str(b.subject), str(b.body))
        if (isResult(draft)) return reply(ctx, draft)
        subject = render(draft.subject)
      }
      if (!mail.smtp_host || !mail.from_address) return reply(ctx, failed('SMTP 还没配置好：服务器地址、端口、发件人地址都要填'))
      if (str(b.to).includes('fail')) return reply(ctx, failed('发送失败：550 Mailbox not found'))
      ctx.send(200, { sent: true, subject })
    },
    'POST /v1/mail/templates': async (ctx) => {
      if (!ctx.requirePermission('platform.settings.write')) return
      const d = await decode(ctx, { code: 'string', channel: 'string', subject: 'string', body: 'string' })
      if (!d.ok) return reply(ctx, d.result)
      const b = d.body
      if (!str(b.code) || !str(b.channel)) return reply(ctx, err(400, 'bad_request', 'tenant, code and channel are required'))
      const t = find(str(b.code), str(b.channel))
      const s = str(b.subject).trim()
      const body = str(b.body).trim()
      if (!s || [...s].length > 200) return reply(ctx, invalid({ subject: '主题必填，且不超过 200 字' }))
      if (!body || [...body].length > 20000) return reply(ctx, invalid({ body: '正文必填，且不超过 20000 字' }))
      if (!t) return reply(ctx, err(404, 'not_found', '资源不存在或无权访问'))
      const draft = checkDraft(t, s, body)
      if (isResult(draft)) return reply(ctx, draft)
      Object.assign(t, { subject: draft.subject, body: draft.body, version: t.version + 1, updated_at: new Date().toISOString() })
      ctx.send(200, { template: rowJson(t), preview_subject: render(t.subject), preview_body: render(t.body) })
    },

    'GET /v1/plugin-hooks': (ctx) => {
      if (!ctx.requirePermission('platform.plugin.read')) return
      ctx.send(200, { hooks: hooks.map(hookJson), events: EVENTS })
    },
    'POST /v1/plugin-hooks': async (ctx) => {
      if (!ctx.requirePermission('platform.plugin.write') || !ctx.requireReauth()) return
      await ctx.idempotent('plugin_hook_save', () => saveHook(ctx))
    },
    'DELETE /v1/plugin-hooks/:code': (ctx) => {
      if (!ctx.requirePermission('platform.plugin.write') || !ctx.requireReauth()) return
      const i = hooks.findIndex((h) => h.code === ctx.params.code)
      if (i < 0) return reply(ctx, err(404, 'not_found', '插件不存在'))
      hooks.splice(i, 1)
      ctx.send(200, { deleted: true })
    },
    'GET /v1/plugin-hooks/:code/deliveries': (ctx) => {
      if (!ctx.requirePermission('platform.plugin.read')) return
      const rows = hooks.find((h) => h.code === ctx.params.code)?.deliveries ?? []
      const sorted = [...rows].sort((a, b) => b.created_at.localeCompare(a.created_at)).slice(0, 50)
      ctx.send(200, { deliveries: sorted.length ? sorted : null })
    },
    'POST /v1/plugin-hooks/:code/test': (ctx) => {
      if (!ctx.requirePermission('platform.plugin.write') || !ctx.requireReauth()) return
      const h = hooks.find((x) => x.code === ctx.params.code)
      if (!h) return reply(ctx, failed('发送失败：插件不存在'))
      if (h.endpoint_url.includes('fail') || h.endpoint_url.includes('crm.')) return reply(ctx, failed('对方返回 HTTP 503，不是 2xx'))
      ctx.send(200, { sent: true, response_code: 200, duration_ms: 60 + Math.floor(Math.random() * 60) })
    },
  },
}
