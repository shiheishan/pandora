// ---------------------------------------------------------------------------
// 添加到 App（原型第 6 节、CLIENTS）：链接本身按 UA 自动出格式（subscription/render.go），深链只带原链接
// 与配置名（服务端 client_name：「站点名 · 备注名」）。没有可靠 scheme 的 App 点了复制链接、手动粘贴。
// ---------------------------------------------------------------------------
export type Device = 'ios' | 'android' | 'mac' | 'windows'
export const DEVICES: readonly Device[] = ['ios', 'android', 'mac', 'windows']

/** 「这台是…」「对方用的是…」的说法 */
export const DEVICE_SAY: Readonly<Record<Device, string>> = { ios: 'iPhone / iPad', android: '安卓手机', mac: 'Mac 电脑', windows: 'Windows 电脑' }
/** 转发说明里的设备：「在 iPhone 或 iPad 上装…」 */
const OTHER_SAY: Readonly<Record<Device, string>> = { ios: ' iPhone 或 iPad ', android: '安卓手机', mac: ' Mac ', windows: ' Windows 电脑' }
const STORE: Readonly<Record<Device, string>> = { ios: 'App Store 搜索', android: '应用商店或官网下载', mac: '官网下载', windows: '官网下载' }

type LinkMaker = (url: string, name: string) => string
const enc = encodeURIComponent
const LINKS: Readonly<Record<string, LinkMaker>> = {
  'Clash Verge': (u, n) => `clash://install-config?url=${enc(u)}&name=${enc(n)}`,
  'Clash Meta for Android': (u, n) => `clash://install-config?url=${enc(u)}&name=${enc(n)}`,
  Shadowrocket: (u, n) => `shadowrocket://add/sub://${btoa(u)}?remark=${enc(n)}`,
  Stash: (u, n) => `stash://install-config?url=${enc(u)}&name=${enc(n)}`,
  Hiddify: (u, n) => `hiddify://import/${u}#${enc(n)}`,
  'sing-box': (u, n) => `sing-box://import-remote-profile?url=${enc(u)}#${enc(n)}`,
}

export interface ClientApp {
  name: string
  note: string
}

/** 按设备推荐：前两个直接列出，其余收在「其他 App」里 */
export const CLIENTS: Readonly<Record<Device, { top: readonly ClientApp[]; rest: readonly ClientApp[] }>> = {
  ios: {
    top: [
      { name: 'Shadowrocket', note: '小火箭，最常用' },
      { name: 'Stash', note: '界面简单' },
    ],
    rest: [
      { name: 'Quantumult X', note: '复制链接后手动粘贴' },
      { name: 'Loon', note: '复制链接后手动粘贴' },
      { name: 'sing-box', note: '' },
    ],
  },
  android: {
    top: [
      { name: 'Clash Meta for Android', note: '最常用' },
      { name: 'Hiddify', note: '一键连接' },
    ],
    rest: [
      { name: 'v2rayNG', note: '复制链接后手动粘贴' },
      { name: 'sing-box', note: '' },
    ],
  },
  mac: {
    top: [{ name: 'Clash Verge', note: '最常用' }],
    rest: [
      { name: 'Hiddify', note: '' },
      { name: 'Stash', note: '' },
      { name: 'sing-box', note: '' },
    ],
  },
  windows: {
    top: [{ name: 'Clash Verge', note: '最常用' }],
    rest: [
      { name: 'Hiddify', note: '' },
      { name: 'v2rayN', note: '复制链接后手动粘贴' },
      { name: 'sing-box', note: '' },
    ],
  },
}

/** 深链；null = 这个 App 不支持唤起，点了复制链接 */
export const appLink = (app: string, url: string, profileName: string): string | null => LINKS[app]?.(url, profileName) ?? null

/** 按 UA 猜这台设备（iPadOS 的桌面 UA 靠触点数认出来） */
export function detectDevice(ua: string, touchPoints = 0): Device {
  if (/iPhone|iPad|iPod/.test(ua) || (/Macintosh/.test(ua) && touchPoints > 1)) return 'ios'
  if (/Android/.test(ua)) return 'android'
  if (/Windows/.test(ua)) return 'windows'
  return 'mac'
}

/** 给别人添加时按备注名猜对方的设备：「妈妈的 iPad」→ iPhone / iPad；猜不出按最常见的 iPhone */
export function guessDeviceFromName(label: string | null): Device {
  const name = label ?? ''
  if (/iPad|iPhone|苹果/i.test(name)) return 'ios'
  if (/安卓|华为|小米|OPPO|vivo|荣耀|三星/i.test(name)) return 'android'
  if (/Mac/i.test(name)) return 'mac'
  if (/电脑|Windows|PC/i.test(name)) return 'windows'
  return 'ios'
}

/** 发给对方的说明：按对方的设备给 App，最后是链接 */
export function shareMessage(device: Device, url: string): string {
  const app = CLIENTS[device].top[0]!.name
  return `在${OTHER_SAY[device]}上装 ${app}（${STORE[device]}）→ 打开下面这条链接 → 点「添加」。\n${url}`
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
