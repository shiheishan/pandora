import { z } from 'zod'

export const subscriptionExtrasSchema = z.object({
  info_nodes: z.boolean(),
  protocol_prefix: z.boolean(),
  fetch_limit_per_hour: z.number().int(),
})
export type SubscriptionExtras = z.output<typeof subscriptionExtrasSchema>

export interface ExtrasForm {
  infoNodes: boolean
  protocolPrefix: boolean
  /** 输入框原文，编辑中途可以为空 */
  fetchLimit: string
}

export function extrasFormFrom(s: SubscriptionExtras): ExtrasForm {
  return {
    infoNodes: s.info_nodes,
    protocolPrefix: s.protocol_prefix,
    fetchLimit: String(s.fetch_limit_per_hour),
  }
}

/** 每小时拉取上限：去首尾空白后必须是 1–10000 的正整数，合法回 null */
export function fetchLimitError(text: string): string | null {
  const trimmed = text.trim()
  if (!/^\d+$/.test(trimmed)) return '每小时拉取上限要在 1 到 10000 之间'
  const n = Number(trimmed)
  if (n < 1 || n > 10000) return '每小时拉取上限要在 1 到 10000 之间'
  return null
}

export function extrasBody(form: ExtrasForm, saved: SubscriptionExtras): { body: Partial<SubscriptionExtras> } {
  const body: Partial<SubscriptionExtras> = {}
  if (form.infoNodes !== saved.info_nodes) body.info_nodes = form.infoNodes
  if (form.protocolPrefix !== saved.protocol_prefix) body.protocol_prefix = form.protocolPrefix
  // 不合法的不放进 body：调用方提交前先用 fetchLimitError 校验
  const limit = Number(form.fetchLimit.trim())
  if (fetchLimitError(form.fetchLimit) === null && limit !== saved.fetch_limit_per_hour) {
    body.fetch_limit_per_hour = limit
  }
  return { body }
}

/** 与后端 render.DetectFormat 同口径，后端改了这里跟着改 */
export interface ClientFormat {
  /** 客户端，界面主列 */
  clients: string
  /** UA 里认的关键词，界面等宽灰字 */
  ua: string
  /** 给的格式 */
  format: string
  /** 认不出的客户端都给它 */
  fallback?: boolean
}

export const CLIENT_FORMATS: readonly ClientFormat[] = [
  {
    clients: 'sing-box 官方客户端、Hiddify、Karing',
    ua: 'sing-box|sfi|sfa|sfm|sft|hiddify|karing',
    format: 'sing-box',
  },
  {
    clients: 'Shadowrocket',
    ua: 'shadowrocket',
    format: 'URI 列表 + 流量信息行',
  },
  {
    clients: 'Loon',
    ua: 'loon',
    format: 'Loon',
  },
  {
    clients: 'Surfboard',
    ua: 'surfboard',
    format: 'Surfboard',
  },
  {
    clients: 'Stash',
    ua: 'stash',
    format: 'Stash',
  },
  {
    clients: 'Shadowsocks 官方客户端',
    ua: 'shadowsocks（不含 ShadowsocksX / SSR）',
    format: 'SIP008',
  },
  {
    clients: 'Clash Verge、Mihomo、FlClash、Clash Nyanpasu、OpenClash',
    ua: 'meta|mihomo|verge|flclash|nyanpasu|openclash|koala|clashmi',
    format: 'Clash Meta（mihomo）',
  },
  {
    clients: 'ClashX、Clash for Windows 等旧版 Clash',
    ua: 'clash',
    format: 'Clash Premium',
  },
  {
    clients: '其他客户端（v2rayN、v2rayNG、NekoBox、Quantumult X 等）',
    ua: '其余',
    format: '通用 URI（base64）',
    fallback: true,
  },
]
