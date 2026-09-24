/**
 * [INPUT]: 依赖 react 的 lazy，依赖 ../modules 的 ModuleKey
 * [OUTPUT]: 对外提供 AdminScreenProps、AdminScreen、SCREENS
 * [POS]: admin 的页面登记表：十个模块各一个懒加载入口，Shell 的内容区按路由取组件渲染；每个模块一个目录、一个独立块，三个前端会话各改各的目录，本文件不再改动
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
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
  content: lazy(() => import('./content')),
  system: lazy(() => import('./system')),
  security: lazy(() => import('./security')),
}
