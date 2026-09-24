/**
 * [INPUT]: 无外部依赖（纯函数；base64 用浏览器与 node 都有的 btoa）
 * [OUTPUT]: 对外提供 ClientApp、importClients、protocolLabel、rateLabel、copyText
 * [POS]: portal/screens/common 的客户端导入与节点展示映射：我的订阅「一键导入」深链、「可用节点」协议名与倍率；copyText 是概览与我的订阅复制订阅地址共用的剪贴板封装
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// ---------------------------------------------------------------------------
// 一键导入：订阅地址本身按 UA 自动出格式（subscription/render.go），深链只需带原地址。
// v2rayN 没有注册 URL scheme，改为复制地址并提示手动添加（href 为 null）。
// ---------------------------------------------------------------------------
export interface ClientApp {
  name: string
  platforms: string
  /** null = 不支持唤起，点了复制地址 */
  href: string | null
}

export function importClients(url: string, profileName: string): ClientApp[] {
  const u = encodeURIComponent(url)
  const n = encodeURIComponent(profileName)
  return [
    { name: 'Clash Verge', platforms: 'Windows · macOS', href: `clash://install-config?url=${u}&name=${n}` },
    { name: 'Shadowrocket', platforms: 'iOS', href: `shadowrocket://add/sub://${btoa(url)}?remark=${n}` },
    { name: 'Hiddify', platforms: 'Android · 全平台', href: `hiddify://import/${url}#${n}` },
    { name: 'v2rayN', platforms: 'Windows', href: null },
    { name: 'Stash', platforms: 'iOS · macOS', href: `stash://install-config?url=${u}&name=${n}` },
    { name: 'sing-box', platforms: '全平台', href: `sing-box://import-remote-profile?url=${u}#${n}` },
  ]
}

// 节点 type（nodefabric 的 13 个稳定协议）→ 展示名；未知的原样显示
const PROTOCOLS: Readonly<Record<string, string>> = {
  anytls: 'AnyTLS',
  http: 'HTTP',
  hysteria2: 'Hysteria2',
  juicity: 'Juicity',
  mieru: 'Mieru',
  naive: 'NaïveProxy',
  shadowsocks: 'Shadowsocks',
  shadowtls: 'ShadowTLS',
  socks: 'SOCKS',
  trojan: 'Trojan',
  tuic: 'TUIC',
  vless: 'VLESS',
  vmess: 'VMess',
}

export const protocolLabel = (protocol: string) => PROTOCOLS[protocol.toLowerCase()] ?? protocol

/** 倍率标签：1 倍不显示，其余「×1.5」 */
export function rateLabel(rate: number): string | null {
  if (!Number.isFinite(rate) || rate === 1) return null
  return `×${Number(rate.toFixed(2))}`
}

/**
 * 复制到剪贴板。navigator.clipboard 只在安全上下文可用（面板可能跑在明文 HTTP 上），
 * 退回隐藏 textarea + execCommand('copy')；都失败返回 false，由调用方提示手动复制。
 */
export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // 落到下面的兜底
  }
  const area = document.createElement('textarea')
  area.value = text
  area.setAttribute('readonly', '')
  area.style.position = 'fixed'
  area.style.opacity = '0'
  document.body.appendChild(area)
  area.select()
  try {
    return document.execCommand('copy')
  } catch {
    return false
  } finally {
    area.remove()
  }
}
