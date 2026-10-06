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
