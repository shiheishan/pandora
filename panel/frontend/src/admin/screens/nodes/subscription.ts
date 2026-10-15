import { z } from 'zod'

/** 有可编辑模板的订阅格式，顺序即列表顺序（与后端 render.TemplateFormats 一致） */
export const TEMPLATE_FORMATS = ['clash', 'clash-premium', 'stash', 'singbox', 'surfboard', 'loon'] as const
export type TemplateFormat = (typeof TEMPLATE_FORMATS)[number]

export const FORMAT_LABEL: Readonly<Record<TemplateFormat, string>> = {
  clash: 'Clash Meta（mihomo）',
  'clash-premium': 'Clash Premium',
  stash: 'Stash',
  singbox: 'sing-box',
  surfboard: 'Surfboard',
  loon: 'Loon',
}

export const subscriptionExtrasSchema = z.object({
  info_nodes: z.boolean(),
  protocol_prefix: z.boolean(),
})
export type SubscriptionExtras = z.output<typeof subscriptionExtrasSchema>

export const templateListSchema = z.object({
  templates: z.array(z.object({ format: z.string(), customized: z.boolean(), bytes: z.number().int(), updated_at: z.string().nullable() })),
})
export type TemplateSummary = z.output<typeof templateListSchema>['templates'][number]

export const templateDetailSchema = z.object({
  format: z.string(),
  customized: z.boolean(),
  content: z.string(),
  default: z.string(),
  updated_at: z.string().nullable(),
  placeholders: z.array(z.object({ token: z.string(), required: z.boolean(), desc: z.string() })),
  max_bytes: z.number().int(),
})
export type TemplateDetail = z.output<typeof templateDetailSchema>

export const templatePreviewSchema = z.object({ body: z.string(), content_type: z.string(), node_count: z.number().int() })
export type TemplatePreview = z.output<typeof templatePreviewSchema>

export function formatLabel(format: string): string {
  return (FORMAT_LABEL as Readonly<Record<string, string>>)[format] ?? format
}

export interface ExtrasForm {
  infoNodes: boolean
  protocolPrefix: boolean
}

export function extrasFormFrom(s: SubscriptionExtras): ExtrasForm {
  return { infoNodes: s.info_nodes, protocolPrefix: s.protocol_prefix }
}

export function extrasBody(form: ExtrasForm, saved: SubscriptionExtras): { body: Partial<SubscriptionExtras> } {
  const body: Partial<SubscriptionExtras> = {}
  if (form.infoNodes !== saved.info_nodes) body.info_nodes = form.infoNodes
  if (form.protocolPrefix !== saved.protocol_prefix) body.protocol_prefix = form.protocolPrefix
  return { body }
}

export function templateBytes(content: string): number {
  return new TextEncoder().encode(content).length
}

export function missingPlaceholders(content: string, placeholders: TemplateDetail['placeholders']): string[] {
  return placeholders.filter((p) => p.required && !content.includes(p.token)).map((p) => p.token)
}

export function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  return `${(bytes / 1024).toFixed(1)} KB`
}
