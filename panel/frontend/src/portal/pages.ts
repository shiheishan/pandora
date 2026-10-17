export type PageKey = 'subs' | 'plans' | 'checkout' | 'orders' | 'wallet' | 'referral' | 'tickets' | 'messages' | 'help' | 'account' | 'me'

// 页标题；页头只有标题，没有副标题。叫法：订阅叫「套餐」「一份」，订阅地址叫「链接」，客户端叫「App」
export const PAGES: Readonly<Record<PageKey, string>> = {
  subs: '我的套餐',
  plans: '选购',
  // 确认页的标题随意图变（续费 / 换成进阶版 / 再买一份 / 重置流量），由页面经 usePageHead 覆盖
  checkout: '确认',
  orders: '订单',
  wallet: '钱包',
  referral: '邀请返利',
  tickets: '工单支持',
  messages: '消息',
  help: '帮助中心',
  account: '账号安全',
  // 只有手机有：桌面打开 #/me 落回我的套餐
  me: '我的',
}

/** 桌面左侧导航（样稿 v5 .side）：三组，组间留空 */
export const SIDE_GROUPS: readonly (readonly PageKey[])[] = [
  ['subs', 'plans', 'wallet'],
  ['orders', 'referral'],
  ['messages', 'tickets', 'help', 'account'],
]

/** 手机底部标签栏四项（样稿 v5 .tabbar） */
export const TAB_PAGES: readonly PageKey[] = ['subs', 'plans', 'wallet', 'me']
/** 标签栏的字比页标题短 */
const TAB_LABELS: Readonly<Partial<Record<PageKey, string>>> = { subs: '套餐' }
export const tabLabel = (page: PageKey) => TAB_LABELS[page] ?? PAGES[page]

/** 内容列 1040 宽的页面（样稿 .col.wide）；其余 720 */
export const WIDE_PAGES: ReadonlySet<PageKey> = new Set<PageKey>(['plans', 'checkout', 'tickets', 'help'])
/** 页标题只给读屏、不显示的页面：手机「我的」页顶上直接是资料卡 */
export const HIDDEN_TITLE_PAGES: ReadonlySet<PageKey> = new Set<PageKey>(['me'])

/** 空路径、未知页面（含已删的 #/overview）与登录后落到这里 */
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
 * 桌面打开 #/me 与未知页面同样处理，落回我的套餐（样稿：桌面没有「我的」页）。
 * 规范化只作用于页面这一段，合法页面后的 rest 原样保留（去空段、重新编码）；查询串不在 path 里，不受影响。
 */
export function resolvePage(path: string, options: { desktop?: boolean } = {}): PortalRoute {
  const [, head = '', ...tail] = path.split('/')
  if (!isPage(head) || (head === 'me' && options.desktop)) {
    return { page: HOME_PAGE, rest: [], canonical: pagePath(HOME_PAGE) }
  }
  let rest: string[]
  try {
    rest = tail.filter((s) => s !== '').map((s) => decodeURIComponent(s))
  } catch {
    rest = []
  }
  return { page: head, rest, canonical: [pagePath(head), ...rest.map((s) => encodeURIComponent(s))].join('/') }
}

/** 侧栏高亮：确认页从哪儿都能进，不亮任何一项；「我的」桌面不存在，返回 null；其余返回自己。 */
export const sideOwner = (page: PageKey): PageKey | null => (page === 'checkout' || page === 'me' ? null : page)

/** 底部标签栏高亮：确认页不高亮；subs / plans / wallet / me 高亮自己；其余页面手机上收在「我的」里，高亮 me。 */
export function tabOwner(page: PageKey): PageKey | null {
  if (page === 'checkout') return null
  if (page === 'subs' || page === 'plans' || page === 'wallet' || page === 'me') return page
  return 'me'
}

