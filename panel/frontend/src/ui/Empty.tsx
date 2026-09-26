/**
 * [INPUT]: 依赖 react 的 ReactNode，依赖 ./cx 与 ./Empty.module.css
 * [OUTPUT]: 对外提供 Empty
 * [POS]: ui 的空状态：一句现状 + 一句能做什么 + 最多一个次按钮，不放插画（规范「空状态」）；bare 用于已经在卡片或表格里的场合
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { ReactNode } from 'react'
import { cx } from './cx'
import css from './Empty.module.css'

export interface EmptyProps {
  title: ReactNode
  description?: ReactNode
  /** 最多一个次按钮 */
  action?: ReactNode
  /** 不画卡片外框 */
  bare?: boolean
  className?: string
}

export function Empty({ title, description, action, bare = false, className }: EmptyProps) {
  return (
    <div className={cx(css.empty, bare && css.bare, className)}>
      <div className={css.title}>{title}</div>
      {description != null && <div className={css.description}>{description}</div>}
      {action != null && <div className={css.action}>{action}</div>}
    </div>
  )
}
