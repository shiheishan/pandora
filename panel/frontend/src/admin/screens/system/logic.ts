import type { Channel, Delivery, Encryption, Hook, HookEvent, MailSettings, RegistrationMode, TelegramExtras, TelegramSettings, Template } from './schemas'

export type Tone = 'ok' | 'warn' | 'muted'

// ---------------------------------------------------------------------------
// 邮件 · SMTP
// ---------------------------------------------------------------------------
/** 设计是一个「发件人」框：name <address> 拼接显示，提交时拆回两个字段；解析不了整串当地址 */
export function formatSender(name: string, address: string): string {
  if (!address) return name
  return name ? `${name} <${address}>` : address
}

export function parseSender(raw: string): { from_name: string; from_address: string } {
  const text = raw.trim()
  const m = /^(.*?)\s*<([^<>\s]+)>$/.exec(text)
  if (m) return { from_name: m[1]!.replace(/^"|"$/g, '').trim(), from_address: m[2]! }
  return { from_name: '', from_address: text }
}

export interface SmtpForm {
  host: string
  port: string
  encryption: Encryption
  username: string
  /** 空 = 不改 */
  password: string
  /** 勾选后提交 "-" 清空已存密码 */
  clearPassword: boolean
  sender: string
}

export function smtpFormFrom(m: MailSettings): SmtpForm {
  return { host: m.smtp_host, port: String(m.smtp_port), encryption: m.encryption, username: m.smtp_username, password: '', clearPassword: false, sender: formatSender(m.from_name, m.from_address) }
}

export function validateSmtp(f: SmtpForm): Record<string, string> {
  const errors: Record<string, string> = {}
  const port = Number(f.port)
  if (!Number.isInteger(port) || port < 1 || port > 65535) errors.smtp_port = '端口需在 1 到 65535 之间'
  const { from_address } = parseSender(f.sender)
  if (from_address && !/^[^@\s]+@[^@\s]+$/.test(from_address)) errors.from_address = '发件人地址格式不对，写成「名称 <地址>」或只写地址'
  return errors
}

interface SmtpFields {
  smtp_host: string
  smtp_port: number
  encryption: Encryption
  smtp_username: string
  from_address: string
  from_name: string
}

/** SMTP 卡保存：六个字段整体覆盖，密码空 = 不改、"-" = 清空；不带注册两项（省略即不动） */
export function smtpBody(f: SmtpForm): SmtpFields & { smtp_password: string } {
  return {
    smtp_host: f.host.trim(),
    smtp_port: Number(f.port),
    encryption: f.encryption,
    smtp_username: f.username.trim(),
    smtp_password: f.clearPassword ? '-' : f.password,
    ...parseSender(f.sender),
  }
}

/** 注册卡保存：同一接口，必须带上「已保存」的 SMTP 六个字段（不是 SMTP 卡里没保存的输入），密码不带 */
export function registrationBody(saved: MailSettings, mode: RegistrationMode, emailVerification: boolean): SmtpFields & { registration_mode: RegistrationMode; email_verification: boolean } {
  return {
    smtp_host: saved.smtp_host,
    smtp_port: saved.smtp_port,
    encryption: saved.encryption,
    smtp_username: saved.smtp_username,
    from_address: saved.from_address,
    from_name: saved.from_name,
    registration_mode: mode,
    email_verification: emailVerification,
  }
}

export function smtpStatus(m: MailSettings): { label: string; tone: Tone } {
  return m.smtp_host && m.from_address ? { label: `已配置 · ${m.smtp_host}`, tone: 'ok' } : { label: '未配置', tone: 'muted' }
}

export const REGISTRATION_LABEL: Record<RegistrationMode, string> = { closed: '关闭', invite_only: '仅邀请', open: '开放' }

// ---------------------------------------------------------------------------
// Telegram
// ---------------------------------------------------------------------------
export function telegramStatus(t: TelegramSettings): { label: string; tone: Tone } {
  if (!t.has_token) return { label: '未配置 Token', tone: 'muted' }
  if (!t.enabled) return { label: '已停用', tone: 'warn' }
  return { label: t.bot_username ? `已启用 @${t.bot_username}` : '已启用', tone: 'ok' }
}

/** chat id：空串 = 没填（null），否则必须是非 0 的安全整数（群组是负数） */
export function parseChatId(raw: string): { ok: true; value: number | null } | { ok: false } {
  const text = raw.trim()
  if (!text) return { ok: true, value: null }
  if (!/^-?\d+$/.test(text)) return { ok: false }
  const n = Number(text)
  return Number.isSafeInteger(n) && n !== 0 ? { ok: true, value: n } : { ok: false }
}

/** 群组 chat id：空串 = 没填（null），否则必须是负数（群组与超级群的 chat id 都以 - 开头） */
export function parseGroupChatId(raw: string): { ok: true; value: number | null } | { ok: false } {
  const chat = parseChatId(raw)
  return chat.ok && (chat.value === null || chat.value < 0) ? chat : { ok: false }
}

export interface TelegramForm {
  enabled: boolean
  username: string
  /** 空 = 不改 */
  token: string
  chatId: string
}

export function telegramFormFrom(t: TelegramSettings): TelegramForm {
  return { enabled: t.enabled, username: t.bot_username, token: '', chatId: t.admin_chat_id === null ? '' : String(t.admin_chat_id) }
}

/** admin_chat_id：没变就不带（后端缺省 = 不改），清空发 null，填了发数字 */
export function telegramBody(f: TelegramForm, saved: TelegramSettings): { enabled: boolean; bot_username: string; bot_token: string; admin_chat_id?: number | null } | { error: string } {
  const chat = parseGroupChatId(f.chatId)
  if (!chat.ok) return { error: '管理员群组 chat id 必须是负数（群组的 chat id 以 - 开头）' }
  const body = { enabled: f.enabled, bot_username: f.username.trim().replace(/^@/, ''), bot_token: f.token.trim() }
  return chat.value === saved.admin_chat_id ? body : { ...body, admin_chat_id: chat.value }
}

/** Telegram 弹窗「机器人与群组」一节的草稿；两个管理员提醒开关不在这里，放在页面「管理员提醒」里即时保存 */
export interface ExtrasForm {
  subCommand: boolean
  startMessage: string
  joinReview: boolean
  userGroupChatId: string
  discussLink: string
}

export function extrasFormFrom(e: TelegramExtras): ExtrasForm {
  return {
    subCommand: e.sub_command_enabled,
    startMessage: e.start_message,
    joinReview: e.join_review_enabled,
    userGroupChatId: e.user_group_chat_id === null ? '' : String(e.user_group_chat_id),
    discussLink: e.discuss_link,
  }
}

/** 整份 7 个键：表单里的 5 个 + 已保存的两个管理员提醒开关；用户群 chat id 只收负数 */
export function extrasBody(f: ExtrasForm, saved: TelegramExtras): TelegramExtras | { error: Record<string, string> } {
  const chat = parseGroupChatId(f.userGroupChatId)
  if (!chat.ok) return { error: { user_group_chat_id: '用户群 chat id 必须是负数（群组的 chat id 以 - 开头）' } }
  return {
    sub_command_enabled: f.subCommand,
    start_message: f.startMessage,
    admin_paid_notify: saved.admin_paid_notify,
    admin_ticket_notify: saved.admin_ticket_notify,
    join_review_enabled: f.joinReview,
    user_group_chat_id: chat.value,
    discuss_link: f.discussLink,
  }
}

export type AdminAlert = 'admin_paid_notify' | 'admin_ticket_notify'

/** 管理员提醒两个开关（10-16 保持两个，不拆三项）：即时保存，其余 5 个键照已保存的原样带上 */
export function adminAlertBody(saved: TelegramExtras, key: AdminAlert, on: boolean): TelegramExtras {
  return { ...saved, [key]: on }
}

// ---------------------------------------------------------------------------
// 通知模板
// ---------------------------------------------------------------------------
export const CHANNEL_LABEL: Record<Channel, string> = { email: '邮件', inapp: '站内信', telegram: 'Telegram' }

const TEMPLATE_NAME: Readonly<Record<string, string>> = {
  'subscription.expiring': '订阅即将到期',
  'quota.warning': '流量预警',
  'order.paid': '订单支付成功',
  'ticket.replied': '工单有新回复',
  'admin.broadcast': '群发邮件',
  'auth.email_verify': '注册验证码',
}

export function templateName(code: string): string {
  return TEMPLATE_NAME[code] ?? code
}

export const templateKey = (t: Pick<Template, 'code' | 'channel'>) => `${t.code}|${t.channel}`

export interface Draft {
  subject: string
  body: string
}

/** 列表里的小字：有未保存的修改 > 已自定义 > 默认；没有内置默认内容的模板写「无内置默认」 */
export function templateMeta(t: Template, draft: Draft | undefined): { label: string; tone: Tone } {
  if (draft && (draft.subject !== t.subject || draft.body !== t.body)) return { label: '未保存的修改', tone: 'warn' }
  if (!t.has_default) return { label: '无内置默认', tone: 'muted' }
  return t.is_default ? { label: '默认', tone: 'muted' } : { label: '已自定义', tone: 'muted' }
}

/** 在光标处插入 {{name}}，返回新文本与插入后的光标位置 */
export function insertVariable(text: string, start: number, end: number, name: string): { text: string; cursor: number } {
  const token = `{{${name}}}`
  const a = Math.max(0, Math.min(start, text.length))
  const b = Math.max(a, Math.min(end, text.length))
  return { text: text.slice(0, a) + token + text.slice(b), cursor: a + token.length }
}

/** 与 Go 的 SaveTemplate 同样的长度规则（去首尾空白后按字计） */
export function validateTemplate(d: Draft): Record<string, string> {
  const errors: Record<string, string> = {}
  const s = [...d.subject.trim()].length
  const b = [...d.body.trim()].length
  if (s === 0 || s > 200) errors.subject = '主题必填，且不超过 200 字'
  if (b === 0 || b > 20000) errors.body = '正文必填，且不超过 20000 字'
  return errors
}

// ---------------------------------------------------------------------------
// Webhook 钩子
// ---------------------------------------------------------------------------
/** 钩子的健康字样（设计稿行内小字）：停用 > 近 7 天失败次数 > 正常 */
export function hookHealth(h: Pick<Hook, 'enabled' | 'failed_count'>): string {
  if (!h.enabled) return '已停用'
  return h.failed_count > 0 ? `近 7 天有 ${h.failed_count} 次失败` : '正常'
}

export function hookTone(h: Pick<Hook, 'enabled' | 'failed_count'>): Tone {
  if (!h.enabled) return 'muted'
  return h.failed_count > 0 ? 'warn' : 'ok'
}

/** 新建钩子的 code：按 code upsert，撞上已有的就会静默覆盖，所以避开现有 code */
export function newHookCode(existing: readonly string[], random: () => number = Math.random): string {
  const alphabet = 'abcdefghijklmnopqrstuvwxyz0123456789'
  for (;;) {
    let tail = ''
    for (let i = 0; i < 6; i++) tail += alphabet[Math.floor(random() * alphabet.length)]
    const code = `hook-${tail}`
    if (!existing.includes(code)) return code
  }
}

/** 名称默认取 URL 主机名 */
export function hostOf(url: string): string {
  try {
    return new URL(url.trim()).hostname
  } catch {
    return ''
  }
}

export interface HookForm {
  name: string
  description: string
  endpoint: string
  events: string[]
  timeoutMs: string
  maxAttempts: string
  enabled: boolean
  /** 空 = 不改 */
  secret: string
}

export function hookFormFrom(h: Hook): HookForm {
  return { name: h.name, description: h.description, endpoint: h.endpoint_url, events: [...h.events], timeoutMs: String(h.timeout_ms), maxAttempts: String(h.max_attempts), enabled: h.enabled, secret: '' }
}

/** 超时与重试次数后端只有 DB CHECK（500–30000 毫秒、1–10 次），越界会变 500，所以前端先拦 */
export function validateHook(f: HookForm): Record<string, string> {
  const errors: Record<string, string> = {}
  if (!f.name.trim()) errors.name = '插件名称必填'
  const url = f.endpoint.trim()
  if (!url) errors.endpoint_url = '回调地址必填'
  else if (!/^https:\/\//i.test(url)) errors.endpoint_url = '必须使用 https'
  if (f.enabled && f.events.length === 0) errors.events = '启用前至少要订阅一个事件，否则这个钩子永远不会触发'
  const t = Number(f.timeoutMs)
  if (!Number.isInteger(t) || t < 500 || t > 30000) errors.timeout_ms = '超时需在 500 到 30000 毫秒之间'
  const m = Number(f.maxAttempts)
  if (!Number.isInteger(m) || m < 1 || m > 10) errors.max_attempts = '最多尝试 1 到 10 次'
  return errors
}

export interface HookBody {
  code: string
  name: string
  description: string
  enabled: boolean
  events: string[]
  endpoint_url: string
  secret: string
  timeout_ms: number
  max_attempts: number
}

/** upsert 全字段：省略的会被写成零值，所以编辑、启停都从现有行回填 */
export function hookBody(code: string, f: HookForm): HookBody {
  return {
    code,
    name: f.name.trim(),
    description: f.description.trim(),
    enabled: f.enabled,
    events: [...f.events],
    endpoint_url: f.endpoint.trim(),
    secret: f.secret.trim(),
    timeout_ms: Number(f.timeoutMs),
    max_attempts: Number(f.maxAttempts),
  }
}

/** 订阅事件的显示：空 = 未订阅，订满目录 = 全部事件，其余按目录里的中文说明（目录没有的原样）用顿号连起来 */
export function eventsLabel(events: readonly string[], catalog: readonly HookEvent[]): string {
  if (events.length === 0) return '未订阅事件'
  if (catalog.length > 0 && events.length >= catalog.length) return '全部事件'
  return events.map((name) => catalog.find((e) => e.name === name)?.desc || name).join('、')
}

export function durationLabel(ms: number | null): string {
  if (ms === null) return '—'
  return ms >= 1000 ? `${(ms / 1000).toFixed(1)} s` : `${ms} ms`
}

/** 投递记录一行（表头：时间 / 事件 / 响应 / 耗时 / 次数）：状态码 0 显示「—」，排队中显示「排队」；次数 0 显示「—」 */
export function deliveryRow(d: Delivery): { at: string; event: string; code: string; tone: Tone; duration: string; attempts: string } {
  const tone: Tone = d.status === 'queued' ? 'muted' : d.response_code >= 200 && d.response_code < 300 ? 'ok' : 'warn'
  return {
    at: (d.sent_at || d.created_at).slice(5, 16),
    event: d.event,
    code: d.status === 'queued' && d.response_code === 0 ? '排队' : d.response_code === 0 ? '—' : String(d.response_code),
    tone,
    duration: durationLabel(d.duration_ms),
    attempts: d.attempts > 0 ? String(d.attempts) : '—',
  }
}
