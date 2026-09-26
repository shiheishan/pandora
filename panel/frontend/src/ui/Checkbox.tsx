/**
 * [INPUT]: 依赖 react 的 forwardRef、useEffect、useId、useRef 与 input 属性类型，依赖 ./cx、./icons 的 IconCheck、./Checkbox.module.css
 * [OUTPUT]: 对外提供 Checkbox
 * [POS]: ui 的复选框：原生 checkbox 换外观（16px、圆角 4，选中填强调色），支持 indeterminate（表格表头「部分选中」）；外层 <label> 用 htmlFor 显式指向 input 的 id（调用方给了 id 就用调用方的），标签文字是 label 的直接文字，检查工具与读屏读到的都是「基础版」而不是 value「on」
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { forwardRef, useEffect, useId, useImperativeHandle, useRef, type InputHTMLAttributes, type ReactNode } from 'react'
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
  // 名字按规范取自外层 label 的全部文字，读屏本来就认；但浏览器检查工具（各会话实测用的 find / read_page）
  // 只认「label[for] 的直接文字」，否则退回 input 的 value「on」。所以显式 for / id，文字也不再包一层 span。
  // aria-label 仍优先于 label，调用方自己命名不受影响
  const autoId = useId()
  const inputId = rest.id ?? autoId
  useImperativeHandle(ref, () => inner.current!)
  useEffect(() => {
    if (inner.current) inner.current.indeterminate = indeterminate
  }, [indeterminate])

  return (
    <label htmlFor={inputId} className={cx(css.wrap, rest.disabled && css.disabled, className)}>
      <span className={cx(css.box, small && css.small)}>
        <input ref={inner} type="checkbox" className={css.input} {...rest} id={inputId} />
        <IconCheck className={css.check} size={small ? 10 : 12} strokeWidth={2} />
        <span className={css.dash} aria-hidden="true" />
      </span>
      {label}
    </label>
  )
})
