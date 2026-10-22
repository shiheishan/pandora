import type { IconSvgElement } from '@hugeicons/react'
import {
  BubbleChatIcon,
  CreditCardIcon,
  DashboardSquare01Icon,
  File01Icon,
  GiftIcon,
  Key01Icon,
  LinkSquare02Icon,
  Logout01Icon,
  Menu01Icon,
  Moon02Icon,
  Notification01Icon,
  PackageIcon,
  Search01Icon,
  ServerStack01Icon,
  Shield01Icon,
  Sun03Icon,
  TelegramIcon,
  UserMultipleIcon,
} from '@hugeicons-pro/core-stroke-rounded'
import type { ModuleKey } from './modules'

/** 侧栏每个模块的图标 */
export const NAV_ICONS: Readonly<Record<ModuleKey, IconSvgElement>> = {
  dash: DashboardSquare01Icon,
  tickets: BubbleChatIcon,
  users: UserMultipleIcon,
  plans: PackageIcon,
  billing: CreditCardIcon,
  marketing: GiftIcon,
  nodes: ServerStack01Icon,
  content: File01Icon,
  system: Notification01Icon,
  security: Shield01Icon,
}

/** 侧栏底部「打开用户端」图标按钮 */
export const PORTAL_ICON: IconSvgElement = LinkSquare02Icon

/** 侧栏搜索框 */
export const SEARCH_ICON = Search01Icon

/** 手机顶栏汉堡 */
export const MENU_ICON = Menu01Icon

/** 主题切换按钮：浅色时显示月亮=切到深色，深色时显示太阳 */
export const THEME_ICONS = { light: Moon02Icon, dark: Sun03Icon } as const

/** 账户菜单：修改密码 */
export const PASSWORD_ICON = Key01Icon

/** 账户菜单：绑定 Telegram */
export const TELEGRAM_ICON = TelegramIcon

/** 账户菜单：退出登录 */
export const LOGOUT_ICON = Logout01Icon

