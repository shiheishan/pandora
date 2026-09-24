/**
 * [INPUT]: 依赖 react 的 forwardRef、useEffect、useRef 与 input 属性类型，依赖 ./cx、./icons 的 IconCheck、./Checkbox.module.css
 * [OUTPUT]: 对外提供 Checkbox
 * [POS]: ui 的复选框：原生 checkbox 换外观（16px、圆角 4，选中填强调色），支持 indeterminate（表格表头「部分选中」）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { forwardRef, useEffect, useImperativeHandle, useRef, type InputHTMLAttributes, type ReactNode } from 'react'
import { cx } from './cx'
import { IconCheck } from './icons'
import css from './Checkbox.module.css'

export interface CheckboxProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'type'> {
  label?: ReactNode
  /** 部分选中：只能经 DOM 属性设置，没有对应的 HTML 特性 */
  indeterminate?: boolean
  /** 表格里用的 14px 小号 */
  small?: boolean
}

export const Checkbox = forwardRef<HTMLInputElement, CheckboxProps>(function Checkbox(
  { label, indeterminate = false, small = false, className, ...rest },
  ref,
) {
  const inner = useRef<HTMLInputElement>(null)
  useImperativeHandle(ref, () => inner.current!)
  useEffect(() => {
    if (inner.current) inner.current.indeterminate = indeterminate
  }, [indeterminate])

  return (
    <label className={cx(css.wrap, rest.disabled && css.disabled, className)}>
      <span className={cx(css.box, small && css.small)}>
        <input ref={inner} type="checkbox" className={css.input} {...rest} />
        <IconCheck className={css.check} size={small ? 10 : 12} strokeWidth={2} />
        <span className={css.dash} aria-hidden="true" />
      </span>
      {label != null && <span>{label}</span>}
    </label>
  )
})
