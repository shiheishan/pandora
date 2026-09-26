/**
 * [INPUT]: 依赖 zod
 * [OUTPUT]: 对外提供通知与插件页的 zod schema 与类型：邮件与注册设置、Telegram 设置、三个测试发送响应、通知模板（列表行、保存 / 恢复 / 草稿预览响应）、Webhook 钩子（列表与事件目录、保存、投递记录、测试投递）
 * [POS]: admin/screens/system 的数据边界：形状照 api-contract.md 后台-09 · 通知与插件（含 R14 R20 R21 R45 R59）并按 Go 的 mail.go、telegram.go、mail_template.go、notify/template_admin.go、plugin/hooks.go 核对——admin_chat_id 是无 omitempty 的指针（缺值 null），投递记录无数据时是 null，last_sent_at 为 omitempty
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { z } from 'zod'

// ---------------------------------------------------------------------------
// 邮件与注册（mail.go）
// ---------------------------------------------------------------------------
export const ENCRYPTIONS = ['ssl', 'tls', 'none'] as const
export type Encryption = (typeof ENCRYPTIONS)[number]
export const REGISTRATION_MODES = ['closed', 'invite_only', 'open'] as const
export type RegistrationMode = (typeof REGISTRATION_MODES)[number]

export const mailSettingsSchema = z.object({
  smtp_host: z.string(),
  smtp_port: z.number().int(),
  encryption: z.enum(ENCRYPTIONS),
  smtp_username: z.string(),
  has_password: z.boolean(),
  from_address: z.string(),
  from_name: z.string(),
  email_verification: z.boolean(),
  registration_mode: z.enum(REGISTRATION_MODES),
})
export type MailSettings = z.output<typeof mailSettingsSchema>

// ---------------------------------------------------------------------------
// Telegram（telegram.go）
// ---------------------------------------------------------------------------
export const telegramSettingsSchema = z.object({
  enabled: z.boolean(),
  bot_username: z.string(),
  has_token: z.boolean(),
  admin_chat_id: z.number().int().nullable(),
})
export type TelegramSettings = z.output<typeof telegramSettingsSchema>

export const okResponse = z.object({ ok: z.literal(true) })
export const telegramSaved = z.object({ ok: z.literal(true), enabled: z.boolean() })
export const telegramTested = z.object({ sent: z.literal(true) })
export const mailTested = z.object({ ok: z.literal(true), to: z.string() })

// ---------------------------------------------------------------------------
// 通知模板（notify.TemplateRow；列表另带 has_default / description / preview_*）
// ---------------------------------------------------------------------------
export const CHANNELS = ['email', 'inapp', 'telegram'] as const
export type Channel = (typeof CHANNELS)[number]

const templateRow = z.object({
  code: z.string(),
  channel: z.enum(CHANNELS),
  locale: z.string(),
  category: z.string(),
  status: z.string(),
  version: z.number().int(),
  subject: z.string(),
  body: z.string(),
  allowed_variables: z.array(z.string()),
  is_default: z.boolean(),
  updated_at: z.string(),
})
export const templateSchema = templateRow.extend({
  has_default: z.boolean(),
  description: z.string(),
  preview_subject: z.string(),
  preview_body: z.string(),
})
export type Template = z.output<typeof templateSchema>
export type TemplateRow = z.output<typeof templateRow>

export const templatesResponse = z.object({ templates: z.array(templateSchema) })
export const templateSaved = z.object({ template: templateRow, preview_subject: z.string(), preview_body: z.string() })
export const templateReset = z.object({ template: templateRow })
export const templatePreview = z.object({ preview_subject: z.string(), preview_body: z.string(), unknown_variables: z.array(z.string()) })
export type TemplatePreview = z.output<typeof templatePreview>
export const templateTested = z.object({ sent: z.literal(true), subject: z.string() })

// ---------------------------------------------------------------------------
// Webhook 钩子（plugin.Hook；R59 事件目录小写键、hooks 恒为数组）
// ---------------------------------------------------------------------------
export const hookSchema = z.object({
  id: z.string().uuid(),
  code: z.string(),
  name: z.string(),
  description: z.string(),
  enabled: z.boolean(),
  events: z.array(z.string()),
  endpoint_url: z.string(),
  has_secret: z.boolean(),
  timeout_ms: z.number().int(),
  max_attempts: z.number().int(),
  queued_count: z.number().int(),
  /** 近 7 天 */
  failed_count: z.number().int(),
  sent_count_7d: z.number().int(),
  /** 数据库会话时区的 YYYY-MM-DD HH:MM，从没送达过时没有 */
  last_sent_at: z.string().optional(),
})
export type Hook = z.output<typeof hookSchema>

export const hookEventSchema = z.object({ name: z.string(), desc: z.string() })
export type HookEvent = z.output<typeof hookEventSchema>
export const hooksResponse = z.object({ hooks: z.array(hookSchema), events: z.array(hookEventSchema) })

export const hookSaved = z.object({ saved: z.literal(true), secret: z.string().optional(), secret_hint: z.string().optional() })
export const hookDeleted = z.object({ deleted: z.literal(true) })

export const DELIVERY_STATUSES = ['queued', 'sent', 'failed'] as const
export const deliverySchema = z.object({
  event: z.string(),
  status: z.enum(DELIVERY_STATUSES),
  attempts: z.number().int(),
  /** 0 = 没有响应 */
  response_code: z.number().int(),
  error_message: z.string(),
  /** 数据库会话时区的 YYYY-MM-DD HH:MM:SS（非 RFC3339） */
  created_at: z.string(),
  /** '' = 未送达 */
  sent_at: z.string(),
  /** 请求没发出去时为 null（R45） */
  duration_ms: z.number().int().nullable(),
})
export type Delivery = z.output<typeof deliverySchema>
/** 没有记录（或 code 不存在）时是 null */
export const deliveriesResponse = z.object({ deliveries: z.array(deliverySchema).nullable() })

export const hookTested = z.object({ sent: z.literal(true), response_code: z.number().int(), duration_ms: z.number().int().nullable() })
