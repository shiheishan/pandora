import { forwardRef, type InputHTMLAttributes, type ReactNode } from 'react'
import { HugeiconsIcon } from '@hugeicons/react'
import { Search01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { cx } from './cx'
import css from './Toolbar.module.css'

export interface ToolbarProps {
  children: ReactNode
  /** 靠右的一组（操作按钮、计数说明） */
  end?: ReactNode
  className?: string
}

export function Toolbar({ children, end, className }: ToolbarProps) {
  return (
    <div className={cx(css.toolbar, className)}>
      {children}
      {end != null && <div className={css.end}>{end}</div>}
    </div>
  )
}

export interface SearchFieldProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'type' | 'size'> {
  /** 无障碍名称（必填，搜索框没有可见标签） */
  'aria-label': string
  /** 宽度，默认 320px */
  width?: number | string
}

export const SearchField = forwardRef<HTMLInputElement, SearchFieldProps>(function SearchField(
  { 'aria-label': ariaLabel, width = 320, className, ...rest },
  ref,
) {
  return (
    <label className={css.search} style={{ width }}>
      <span className={css.icon}>
        <HugeiconsIcon icon={Search01Icon} size={14} strokeWidth={1.75} aria-hidden="true" />
      </span>
      <input ref={ref} type="search" aria-label={ariaLabel} className={cx(css.input, className)} {...rest} />
    </label>
  )
})
