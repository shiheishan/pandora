import { lazy, type ComponentType, type LazyExoticComponent } from 'react'
import type { ModuleKey } from '../modules'

/** 页面入参：当前标签（无标签模块为 null）与标签之后剩下的路径段（已解码），如 #/users/list/<id> → rest = [id] */
export interface AdminScreenProps {
  tab: string | null
  rest: string[]
}

export type AdminScreen = LazyExoticComponent<ComponentType<AdminScreenProps>>

export const SCREENS: Readonly<Record<ModuleKey, AdminScreen>> = {
  dash: lazy(() => import('./dash')),
  tickets: lazy(() => import('./tickets')),
  users: lazy(() => import('./users')),
  plans: lazy(() => import('./plans')),
  billing: lazy(() => import('./billing')),
  marketing: lazy(() => import('./marketing')),
  nodes: lazy(() => import('./nodes')),
  certs: lazy(() => import('./certs')),
  content: lazy(() => import('./content')),
  system: lazy(() => import('./system')),
  security: lazy(() => import('./security')),
}
