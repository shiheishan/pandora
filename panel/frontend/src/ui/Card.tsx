/**
 * [INPUT]: 依赖 react 的 HTMLAttributes 与 ReactNode，依赖 ./cx 与 ./Card.module.css
 * [OUTPUT]: 对外提供 Card
 * [POS]: ui 的卡片：1px --border 描边、不加阴影；圆角与内边距随入口（门户 14/20、后台 12/16）；tint 是套餐卡的朱砂极浅底
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { HTMLAttributes, ReactNode } from 'react'
import { cx } from './cx'
import css from './Card.module.css'

export interface CardProps extends Omit<HTMLAttributes<HTMLElement>, 'title'> {
  title?: ReactNode
  /** 标题行右侧：操作按钮、标签 */
  extra?: ReactNode
  /** 套餐卡：--brand-tint 底、--brand-soft 描边 */
  tint?: boolean
  /** 内容自己管边距（表格、列表贴边） */
  flush?: boolean
}

export function Card({ title, extra, tint = false, flush = false, className, children, ...rest }: CardProps) {
  return (
    <section className={cx(css.card, tint && css.tint, flush && css.flush, className)} {...rest}>
      {(title != null || extra != null) && (
        <header className={css.head}>
          {title != null && <h3 className={css.title}>{title}</h3>}
          {extra != null && <div className={css.extra}>{extra}</div>}
        </header>
      )}
      {children}
    </section>
  )
}
