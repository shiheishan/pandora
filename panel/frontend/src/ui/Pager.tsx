/**
 * [INPUT]: 依赖 ./Button，依赖 ./cx 与 ./Pager.module.css
 * [OUTPUT]: 对外提供 Pager、PagerProps
 * [POS]: ui 的分页条：「第 N / M 页 · 共 T 条」加上一页 / 下一页，按 offset / limit 翻页（与后端列表接口的分页参数同形）；总数不超过一页时不渲染。来自后台营销页，各列表共用
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { Button } from './Button'
import { cx } from './cx'
import css from './Pager.module.css'

export interface PagerProps {
  total: number
  limit: number
  offset: number
  onChange: (offset: number) => void
  className?: string
}

export function Pager({ total, limit, offset, onChange, className }: PagerProps) {
  if (total <= limit) return null
  const pages = Math.ceil(total / limit)
  const current = Math.floor(offset / limit) + 1
  return (
    <nav className={cx(css.pager, className)} aria-label="分页">
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
