/**
 * [INPUT]: 依赖 react 的 lazy，依赖 ../pages 的 PageKey
 * [OUTPUT]: 对外提供 PortalScreenProps、PortalScreen、SCREENS
 * [POS]: portal 的页面登记表：十一个页面（含结账）各一个懒加载入口，Shell 的内容区按路由取组件渲染；每个页面一个目录、一个独立块，门户前端会话只改各页面目录，本文件不再改动
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
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
