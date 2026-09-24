/**
 * [INPUT]: 依赖 ../core/router 的 matchPath
 * [OUTPUT]: 对外提供 PageKey、PAGES、NAV_PAGES、NAV_LABELS、navLabel、MENU_PAGES、resolvePage、pagePath、navOwner、greeting
 * [POS]: portal 的页面与路由表：顶部导航、底部标签栏、头像菜单、页脚链接与页头标题共用（取自 用户门户.dc.html 的 PAGES / NAV / MENU）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { matchPath } from '../core/router'

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

/** #/orders → orders；空路径与未知路径落到概览（canonical 供外框 replace）。 */
export function resolvePage(path: string): { page: PageKey; canonical: string } {
  const params = matchPath('/:page', path)
  const page = params?.page && isPage(params.page) ? params.page : 'overview'
  return { page, canonical: pagePath(page) }
}

/** 顶部导航的高亮：结账页归在「选购套餐」下。 */
export const navOwner = (page: PageKey): PageKey => (page === 'checkout' ? 'plans' : page)

export function greeting(now = new Date()): string {
  const h = now.getHours()
  return h < 5 ? '夜深了' : h < 11 ? '早上好' : h < 13 ? '中午好' : h < 18 ? '下午好' : '晚上好'
}
