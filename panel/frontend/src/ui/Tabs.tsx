import { useCallback, useEffect, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
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
  const scrollRef = useRef<HTMLDivElement>(null)
  const refs = useRef<(HTMLButtonElement | null)[]>([])
  const [fadeLeft, setFadeLeft] = useState(false)
  const [fadeRight, setFadeRight] = useState(false)

  const updateFade = useCallback(() => {
    const el = scrollRef.current
    if (!el) return
    const canLeft = el.scrollLeft > 1
    const canRight = el.scrollLeft + el.clientWidth < el.scrollWidth - 1
    setFadeLeft(canLeft)
    setFadeRight(canRight)
  }, [])

  useEffect(() => {
    const el = scrollRef.current
    if (!el) return
    updateFade()
    const ro = new ResizeObserver(() => updateFade())
    ro.observe(el)
    el.addEventListener('scroll', updateFade, { passive: true })
    return () => {
      ro.disconnect()
      el.removeEventListener('scroll', updateFade)
    }
  }, [updateFade, items])

  // 选中的页签在可视区外时只横向滚动页签栏，不用 scrollIntoView（它会连带滚动整页）
  useEffect(() => {
    const el = scrollRef.current
    const tab = refs.current[items.findIndex((t) => t.value === value)]
    if (!el || !tab) return
    const left = tab.getBoundingClientRect().left - el.getBoundingClientRect().left + el.scrollLeft
    const right = left + tab.offsetWidth
    if (left < el.scrollLeft) el.scrollLeft = left
    else if (right > el.scrollLeft + el.clientWidth) el.scrollLeft = right - el.clientWidth
  }, [value, items])

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
    <div
      ref={scrollRef}
      role="tablist"
      aria-label={label}
      className={cx(css.tabs, className)}
      data-fade-left={fadeLeft ? '' : undefined}
      data-fade-right={fadeRight ? '' : undefined}
      onKeyDown={onKeyDown}
    >
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
