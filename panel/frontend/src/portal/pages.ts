/**
 * [INPUT]: 无外部依赖（纯数据与纯函数）
 * [OUTPUT]: 对外提供 PageKey、PAGES、NAV_PAGES、NAV_LABELS、navLabel、MENU_PAGES、PortalRoute、resolvePage、pagePath、navOwner、greeting
 * [POS]: portal 的页面与路由表：顶部导航、底部标签栏、头像菜单、页脚链接与页头标题共用（取自 用户门户.dc.html 的 PAGES / NAV / MENU）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
export type PageKey = 'overview' | 'subs' | 'plans' | 'checkout' | 'orders' | 'wallet' | 'referral' | 'tickets' | 'messages' | 'help' | 'account'

// [标题, 副标题]；设计稿「按时间订阅，或按流量买流量包」等副标题原样保留
export const PAGES: Readonly<Record<PageKey, readonly [string, string]>> = {
  overview: ['概览', ''],
  subs: ['我的订阅', '订阅地址、客户端导入与可用节点'],
  plans: ['选购套餐', '按时间订阅，或按流量买流量包'],
  checkout: ['确认订单', ''],
  orders: ['我的订单', ''],
  wallet: ['钱包', '余额、充值与礼品卡'],
  referral: ['邀请返利', '好友通过您的链接购买，您获得佣金'],
  tickets: ['工单支持', '工作日 10 分钟内首次响应'],
  messages: ['消息', ''],
  help: ['帮助中心', ''],
  account: ['账号安全', ''],
}

export const NAV_PAGES: readonly PageKey[] = ['overview', 'subs', 'plans', 'orders']
/** 顶部导航与底部标签栏的文字：「订单」比页标题「我的订单」短 */
export const NAV_LABELS: Readonly<Partial<Record<PageKey, string>>> = { orders: '订单' }
export const navLabel = (page: PageKey) => NAV_LABELS[page] ?? PAGES[page][0]
export const MENU_PAGES: readonly PageKey[] = ['wallet', 'referral', 'tickets', 'help', 'account']

const isPage = (value: string): value is PageKey => Object.hasOwn(PAGES, value)

export const pagePath = (page: PageKey) => `/${page}`

export interface PortalRoute {
  page: PageKey
  /** 页面之后剩下的路径段，已 URI 解码，交给页面自己解释（如 #/orders/<订单号> → [订单号]） */
  rest: string[]
  /** 地址不规范（未知页面、空段）时外框 replace 成它 */
  canonical: string
}

/**
 * #/orders/abc → { page: 'orders', rest: ['abc'] }；空路径与未知页面落到概览并丢掉后面的段。
 * 规范化只作用于页面这一段，合法页面后的 rest 原样保留（去空段、重新编码）；查询串不在 path 里，不受影响。
 */
export function resolvePage(path: string): PortalRoute {
  const [, head = '', ...tail] = path.split('/')
  if (!isPage(head)) return { page: 'overview', rest: [], canonical: pagePath('overview') }
  let rest: string[]
  try {
    rest = tail.filter((s) => s !== '').map((s) => decodeURIComponent(s))
  } catch {
    rest = []
  }
  return { page: head, rest, canonical: [pagePath(head), ...rest.map((s) => encodeURIComponent(s))].join('/') }
}

/** 顶部导航的高亮：结账页归在「选购套餐」下。 */
export const navOwner = (page: PageKey): PageKey => (page === 'checkout' ? 'plans' : page)

export function greeting(now = new Date()): string {
  const h = now.getHours()
  return h < 5 ? '夜深了' : h < 11 ? '早上好' : h < 13 ? '中午好' : h < 18 ? '下午好' : '晚上好'
}
