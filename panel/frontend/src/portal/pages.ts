export type PageKey = 'overview' | 'subs' | 'plans' | 'checkout' | 'orders' | 'wallet' | 'referral' | 'tickets' | 'messages' | 'help' | 'account'

// [标题, 副标题]；设计稿「按时间订阅，或按流量买流量包」等副标题原样保留
// 叫法（购买模型 10-07，原型第 7 节）：订阅叫「套餐」「一份」，订阅地址叫「链接」，客户端叫「App」
export const PAGES: Readonly<Record<PageKey, readonly [string, string]>> = {
  overview: ['概览', ''],
  subs: ['我的套餐', ''],
  plans: ['选购', ''],
  // 确认页的标题随意图变（续费 / 换成进阶版 / 再买一份 / 加流量），由页面经 usePageHead 覆盖
  checkout: ['确认', ''],
  orders: ['我的订单', ''],
  wallet: ['钱包', ''],
  referral: ['邀请返利', '好友通过您的链接购买，您获得佣金'],
  // 设计稿写「工作日 10 分钟内首次响应」，后端 SLA 是普通 12 小时、紧急 1 小时（support.slaHours），不做时效承诺
  tickets: ['工单支持', '提交问题后，客服会在这里回复你'],
  messages: ['消息', ''],
  help: ['帮助中心', ''],
  account: ['账号安全', ''],
}

/** 用户 10-07 拍板：导航只有「我的套餐 / 选购 / 钱包」三项，概览、订单、工单等收进菜单 */
export const NAV_PAGES: readonly PageKey[] = ['subs', 'plans', 'wallet']
/** 顶部导航与底部标签栏的文字：「订单」比页标题「我的订单」短 */
export const NAV_LABELS: Readonly<Partial<Record<PageKey, string>>> = { orders: '订单' }
export const navLabel = (page: PageKey) => NAV_LABELS[page] ?? PAGES[page][0]
export const MENU_PAGES: readonly PageKey[] = ['overview', 'orders', 'referral', 'tickets', 'help', 'account']
/** 空路径、未知页面与登录后落到这里 */
export const HOME_PAGE: PageKey = 'subs'

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
 * #/orders/abc → { page: 'orders', rest: ['abc'] }；空路径与未知页面落到我的套餐并丢掉后面的段。
 * 规范化只作用于页面这一段，合法页面后的 rest 原样保留（去空段、重新编码）；查询串不在 path 里，不受影响。
 */
export function resolvePage(path: string): PortalRoute {
  const [, head = '', ...tail] = path.split('/')
  if (!isPage(head)) return { page: HOME_PAGE, rest: [], canonical: pagePath(HOME_PAGE) }
  let rest: string[]
  try {
    rest = tail.filter((s) => s !== '').map((s) => decodeURIComponent(s))
  } catch {
    rest = []
  }
  return { page: head, rest, canonical: [pagePath(head), ...rest.map((s) => encodeURIComponent(s))].join('/') }
}

/** 顶部导航的高亮：确认页从哪儿都能进（卡片续费、选购、加流量），不高亮任何一项。 */
export const navOwner = (page: PageKey): PageKey | null => (page === 'checkout' ? null : page)

export function greeting(now = new Date()): string {
  const h = now.getHours()
  return h < 5 ? '夜深了' : h < 11 ? '早上好' : h < 13 ? '中午好' : h < 18 ? '下午好' : '晚上好'
}
