import type { HTMLAttributes, ReactNode } from 'react'
import { cx } from './cx'
import css from './Card.module.css'

export interface CardProps extends Omit<HTMLAttributes<HTMLElement>, 'title'> {
  title?: ReactNode
  /** 标题后的灰色小字（样稿 .bh .sub） */
  subtitle?: ReactNode
  /** 标题行右侧：操作按钮、分段、标签 */
  extra?: ReactNode
  /** 底栏（样稿 .sbar）：说明文字 + 保存按钮等，内容自己排，组件给 flex 容器 */
  footer?: ReactNode
  /** 浅底（--brand-tint 底、--brand-soft 描边） */
  tint?: boolean
  /** 内容贴边（表格、行列表），不加内边距 */
  flush?: boolean
}

export function Card({
  title,
  subtitle,
  extra,
  footer,
  tint = false,
  flush = false,
  className,
  children,
  ...rest
}: CardProps) {
  return (
    <section className={cx(css.card, tint && css.tint, flush && css.flush, className)} {...rest}>
      {(title != null || extra != null) && (
        <header className={css.head}>
          {title != null && <h3 className={css.title}>{title}</h3>}
          {subtitle != null && <span className={css.subtitle}>{subtitle}</span>}
          <span className={css.spacer} />
          {extra != null && <div className={css.extra}>{extra}</div>}
        </header>
      )}
      <div className={css.body}>{children}</div>
      {footer != null && <footer className={css.foot}>{footer}</footer>}
    </section>
  )
}
