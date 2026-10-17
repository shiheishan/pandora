import { useRef, type KeyboardEvent, type ReactNode } from 'react'
import { cx } from './cx'
import css from './FilterTabs.module.css'

export interface FilterTabItem<T extends string> {
  value: T
  label: ReactNode
  /** 计数；不给就不显示数字 */
  count?: number
}

export interface FilterTabsProps<T extends string> {
  items: readonly FilterTabItem<T>[]
  value: T
  onChange: (value: T) => void
  /** 无障碍名称，如「按类型筛选」 */
  label: string
  className?: string
}

export function FilterTabs<T extends string>({ items, value, onChange, label, className }: FilterTabsProps<T>) {
  const refs = useRef<(HTMLButtonElement | null)[]>([])

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (items.length === 0) return
    const current = items.findIndex((t) => t.value === value)
    const last = items.length - 1
    const next =
      event.key === 'ArrowRight' || event.key === 'ArrowDown'
        ? (current + 1) % items.length
        : event.key === 'ArrowLeft' || event.key === 'ArrowUp'
          ? (current - 1 + items.length) % items.length
          : event.key === 'Home'
            ? 0
            : event.key === 'End'
              ? last
              : -1

    if (next < 0) return
    event.preventDefault()
    onChange(items[next]!.value)
    refs.current[next]?.focus()
  }

  return (
    <div role="radiogroup" aria-label={label} className={cx(css.tabs, className)} onKeyDown={onKeyDown}>
      {items.map((t, i) => {
        const on = t.value === value
        return (
          <button
            key={t.value}
            ref={(el) => {
              refs.current[i] = el
            }}
            type="button"
            role="radio"
            aria-checked={on}
            tabIndex={on ? 0 : -1}
            className={cx(css.tab, on && css.on)}
            onClick={() => onChange(t.value)}
          >
            <span>{t.label}</span>
            {t.count != null && <span className={css.count}>{t.count}</span>}
          </button>
        )
      })}
    </div>
  )
}
