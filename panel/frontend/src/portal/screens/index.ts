import { lazy, type ComponentType, type LazyExoticComponent } from 'react'
import type { PageKey } from '../pages'

/** 页面入参：页面之后剩下的路径段（已解码），如 #/orders/<订单号> → rest = [订单号]；查询串用 core/router 的 useHashLocation 读 */
export interface PortalScreenProps {
  rest: string[]
}

export type PortalScreen = LazyExoticComponent<ComponentType<PortalScreenProps>>

export const SCREENS: Readonly<Record<PageKey, PortalScreen>> = {
  overview: lazy(() => import('./overview')),
  subs: lazy(() => import('./subs')),
  plans: lazy(() => import('./plans')),
  checkout: lazy(() => import('./checkout')),
  orders: lazy(() => import('./orders')),
  wallet: lazy(() => import('./wallet')),
  referral: lazy(() => import('./referral')),
  tickets: lazy(() => import('./tickets')),
  messages: lazy(() => import('./messages')),
  help: lazy(() => import('./help')),
  account: lazy(() => import('./account')),
}
