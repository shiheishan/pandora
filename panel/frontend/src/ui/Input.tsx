/**
 * [INPUT]: 依赖 react 的 forwardRef 与表单元素属性类型，依赖 ./Field 的 Field / useFieldIds / FieldControlProps，依赖 ./control.module.css
 * [OUTPUT]: 对外提供 Input 与 TextArea
 * [POS]: ui 的文本输入：受控与非受控都行，label / hint / error 由 Field 排版并接好无障碍属性；mono 用于邀请码、优惠码、IP 这类要逐字核对的输入
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { forwardRef, type InputHTMLAttributes, type TextareaHTMLAttributes } from 'react'
import { cx } from './cx'
import { Field, hasError, useFieldIds, type FieldControlProps } from './Field'
import css from './control.module.css'

interface Shared extends FieldControlProps {
  mono?: boolean
  /** 外层字段容器的 className */
  fieldClassName?: string
}

export interface InputProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'size'>, Shared {
  size?: 'md' | 'sm'
}

export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { label, hint, error, mono, size = 'md', fieldClassName, id, className, ...rest },
  ref,
) {
  const { controlId, messageId } = useFieldIds(id)
  return (
    <Field label={label} hint={hint} error={error} controlId={controlId} messageId={messageId} className={fieldClassName}>
      <input
        ref={ref}
        id={controlId}
        aria-invalid={hasError(error) || undefined}
        aria-describedby={hasError(error) || hint != null ? messageId : undefined}
        className={cx(css.control, size === 'sm' && css.sm, mono && css.mono, className)}
        {...rest}
      />
    </Field>
  )
})

export interface TextAreaProps extends TextareaHTMLAttributes<HTMLTextAreaElement>, Shared {}

export const TextArea = forwardRef<HTMLTextAreaElement, TextAreaProps>(function TextArea(
  { label, hint, error, mono, fieldClassName, id, className, ...rest },
  ref,
) {
  const { controlId, messageId } = useFieldIds(id)
  return (
    <Field label={label} hint={hint} error={error} controlId={controlId} messageId={messageId} className={fieldClassName}>
      <textarea
        ref={ref}
        id={controlId}
        aria-invalid={hasError(error) || undefined}
        aria-describedby={hasError(error) || hint != null ? messageId : undefined}
        className={cx(css.control, css.area, mono && css.mono, className)}
        {...rest}
      />
    </Field>
  )
})
