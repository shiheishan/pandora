import { Button } from './Button'
import { cx } from './cx'
import css from './Pager.module.css'

export interface PagerProps {
  total: number
  limit: number
  offset: number
  onChange: (offset: number) => void
  className?: string
  /** 放进 Table 的 footer 里：去掉自身内边距，由表尾负责 */
  inline?: boolean
}

export function Pager({ total, limit, offset, onChange, className, inline = false }: PagerProps) {
  if (total <= limit) return null
  const pages = Math.ceil(total / limit)
  const current = Math.floor(offset / limit) + 1
  return (
    <nav className={cx(css.pager, inline && css.inline, className)} aria-label="分页">
      <span aria-live="polite">
        第 {current} / {pages} 页 · 共 {total} 条
      </span>
      <Button size="xs" disabled={offset === 0} onClick={() => onChange(Math.max(0, offset - limit))}>
        上一页
      </Button>
      <Button size="xs" disabled={offset + limit >= total} onClick={() => onChange(offset + limit)}>
        下一页
      </Button>
    </nav>
  )
}
