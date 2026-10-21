// 密码输入：右侧眼睛按钮切换显示明文（缺省隐藏）；其余同 Input。门户与后台共用。
import { forwardRef, useCallback, useRef } from 'react'
import control from './control.module.css'
import { cx } from './cx'
import { Field, hasError, useFieldIds } from './Field'
import { IconEye, IconEyeOff } from './icons'
import type { InputProps } from './Input'
import css from './PasswordInput.module.css'
import { usePasswordReveal } from './usePasswordReveal'

export type PasswordInputProps = Omit<InputProps, 'type'>

export const PasswordInput = forwardRef<HTMLInputElement, PasswordInputProps>(function PasswordInput(
  { label, hint, error, mono, size = 'md', fieldClassName, id, className, disabled, ...rest },
  ref,
) {
  const inner = useRef<HTMLInputElement | null>(null)
  const setRefs = useCallback(
    (node: HTMLInputElement | null) => {
      inner.current = node
      if (typeof ref === 'function') {
        ref(node)
      } else if (ref != null) {
        ref.current = node
      }
    },
    [ref],
  )
  const { controlId, messageId } = useFieldIds(id)
  const reveal = usePasswordReveal(inner)

  return (
    <Field label={label} hint={hint} error={error} controlId={controlId} messageId={messageId} className={fieldClassName}>
      <div className={css.wrap}>
        <input
          ref={setRefs}
          id={controlId}
          type={reveal.type}
          disabled={disabled}
          aria-invalid={hasError(error) || undefined}
          aria-describedby={hasError(error) || hint != null ? messageId : undefined}
          className={cx(control.control, size === 'sm' && control.sm, mono && control.mono, css.input, className)}
          {...rest}
        />
        <button {...reveal.buttonProps} className={css.toggle} disabled={disabled}>
          {reveal.shown ? <IconEyeOff size={16} /> : <IconEye size={16} />}
        </button>
      </div>
    </Field>
  )
})
