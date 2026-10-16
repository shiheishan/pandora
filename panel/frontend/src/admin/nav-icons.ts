import type { IconSvgElement } from '@hugeicons/react'
import {
  BubbleChatIcon,
  CreditCardIcon,
  DashboardSquare01Icon,
  File01Icon,
  GiftIcon,
  LinkSquare02Icon,
  Notification01Icon,
  PackageIcon,
  ServerStack01Icon,
  Shield01Icon,
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

/** 侧栏底部「打开用户端 ↗」 */
export const PORTAL_ICON: IconSvgElement = LinkSquare02Icon
