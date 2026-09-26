/**
 * [INPUT]: 依赖 react 的 useRef 与 KeyboardEvent，依赖 ./cx 与 ./Tabs.module.css
 * [OUTPUT]: 对外提供 Tabs 与 TabItem 类型
 * [POS]: ui 的标签页导航：下划线样式，当前项 600 字重加 2px 强调色下划线（门户朱砂、后台墨色）；按 WAI-ARIA tabs 模式实现左右方向键、Home/End 切换与漫游 tabindex
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useRef, type KeyboardEvent, type ReactNode } from 'react'
import { cx } from './cx'
import css from './Tabs.module.css'

export interface TabItem<T extends string> {
  value: T
  label: ReactNode
  /** 标签后的计数，如待处理数 */
  badge?: ReactNode
}

export interface TabsProps<T extends string> {
  items: readonly TabItem<T>[]
  value: T
  onChange: (value: T) => void
  label: string
  /** 对应面板 id 的前缀；给了则每个 tab 带 aria-controls=`${idPrefix}-${value}` */
  idPrefix?: string
  className?: string
}

export function Tabs<T extends string>({ items, value, onChange, label, idPrefix, className }: TabsProps<T>) {
  const refs = useRef<(HTMLButtonElement | null)[]>([])

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const current = items.findIndex((t) => t.value === value)
    const last = items.length - 1
    const next =
      event.key === 'ArrowRight' ? (current + 1) % items.length
      : event.key === 'ArrowLeft' ? (current - 1 + items.length) % items.length
      : event.key === 'Home' ? 0
      : event.key === 'End' ? last
      : -1
    if (next < 0) return
    event.preventDefault()
    onChange(items[next]!.value)
    refs.current[next]?.focus()
  }

  return (
    <div role="tablist" aria-label={label} className={cx(css.tabs, className)} onKeyDown={onKeyDown}>
      {items.map((t, i) => {
        const on = t.value === value
        return (
          <button
            key={t.value}
            ref={(el) => {
              refs.current[i] = el
            }}
            type="button"
            role="tab"
            id={idPrefix ? `${idPrefix}-tab-${t.value}` : undefined}
            aria-selected={on}
            aria-controls={idPrefix ? `${idPrefix}-${t.value}` : undefined}
            tabIndex={on ? 0 : -1}
            className={cx(css.tab, on && css.on)}
            onClick={() => onChange(t.value)}
          >
            {t.label}
            {t.badge != null && <span className={css.badge}>{t.badge}</span>}
          </button>
        )
      })}
    </div>
  )
}
