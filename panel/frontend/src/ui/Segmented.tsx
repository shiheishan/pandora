import { useRef, type KeyboardEvent, type ReactNode } from 'react'
import { cx } from './cx'
import css from './Segmented.module.css'

export interface SegmentedOption<T extends string> {
  value: T
  label: ReactNode
}

export interface SegmentedProps<T extends string> {
  options: readonly SegmentedOption<T>[]
  value: T
  onChange: (value: T) => void
  label: string
  size?: 'md' | 'sm'
  className?: string
}

export function Segmented<T extends string>({ options, value, onChange, label, size = 'md', className }: SegmentedProps<T>) {
  const refs = useRef<(HTMLButtonElement | null)[]>([])

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const step = event.key === 'ArrowRight' || event.key === 'ArrowDown' ? 1 : event.key === 'ArrowLeft' || event.key === 'ArrowUp' ? -1 : 0
    if (!step) return
    event.preventDefault()
    const current = options.findIndex((o) => o.value === value)
    const next = (current + step + options.length) % options.length
    onChange(options[next]!.value)
    refs.current[next]?.focus()
  }

  return (
    <div role="radiogroup" aria-label={label} className={cx(css.group, size === 'sm' && css.sm, className)} onKeyDown={onKeyDown}>
      {options.map((o, i) => {
        const on = o.value === value
        return (
          <button
            key={o.value}
            ref={(el) => {
              refs.current[i] = el
            }}
            type="button"
            role="radio"
            aria-checked={on}
            tabIndex={on ? 0 : -1}
            className={cx(css.option, on && css.on)}
            onClick={() => onChange(o.value)}
          >
            {o.label}
          </button>
        )
      })}
    </div>
  )
}
