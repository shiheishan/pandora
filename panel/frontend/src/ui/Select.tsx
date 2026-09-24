/**
 * [INPUT]: 依赖 react 的 forwardRef 与 select 属性类型，依赖 ./Field、./icons 的 IconChevronDown、./control.module.css
 * [OUTPUT]: 对外提供 Select 与 SelectOption 类型
 * [POS]: ui 的下拉选择：用原生 <select>（设计稿各模块也是原生 select），键盘、读屏与移动端滚轮选择器都由浏览器负责；只换掉系统箭头并套上输入框外观
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { forwardRef, type SelectHTMLAttributes } from 'react'
import { cx } from './cx'
import { Field, useFieldIds, type FieldControlProps } from './Field'
import { IconChevronDown } from './icons'
import css from './control.module.css'

export interface SelectOption {
  value: string
  label: string
  disabled?: boolean
}

export interface SelectProps extends Omit<SelectHTMLAttributes<HTMLSelectElement>, 'size'>, FieldControlProps {
  options: readonly SelectOption[]
  /** 未选择时显示的占位项，值为空串 */
  placeholder?: string
  size?: 'md' | 'sm'
  fieldClassName?: string
}

export const Select = forwardRef<HTMLSelectElement, SelectProps>(function Select(
  { label, hint, error, options, placeholder, size = 'md', fieldClassName, id, className, ...rest },
  ref,
) {
  const { controlId, messageId } = useFieldIds(id)
  return (
    <Field label={label} hint={hint} error={error} controlId={controlId} messageId={messageId} className={fieldClassName}>
      <span className={css.selectWrap}>
        <select
          ref={ref}
          id={controlId}
          aria-invalid={error != null || undefined}
          aria-describedby={error != null || hint != null ? messageId : undefined}
          className={cx(css.control, css.select, size === 'sm' && css.sm, className)}
          {...rest}
        >
          {placeholder != null && (
            <option value="" disabled>
              {placeholder}
            </option>
          )}
          {options.map((o) => (
            <option key={o.value} value={o.value} disabled={o.disabled}>
              {o.label}
            </option>
          ))}
        </select>
        <IconChevronDown className={css.chevron} size={14} />
      </span>
    </Field>
  )
})
