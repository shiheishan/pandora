import { forwardRef, type InputHTMLAttributes, type ReactNode } from 'react'
import { cx } from './cx'
import css from './Switch.module.css'

export interface SwitchProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'type' | 'role'> {
  /** 旁边的文字；不传时必须给 aria-label */
  label?: ReactNode
  /** danger：打开时轨道是红色（如「暂停这个功能」类的降级开关） */
  tone?: 'default' | 'danger'
}

export const Switch = forwardRef<HTMLInputElement, SwitchProps>(function Switch({ label, tone = 'default', className, ...rest }, ref) {
  return (
    <label className={cx(css.wrap, rest.disabled && css.disabled, tone === 'danger' && css.danger, className)}>
      <input ref={ref} type="checkbox" role="switch" className={css.input} {...rest} />
      <span className={css.track} aria-hidden="true">
        <span className={css.knob} />
      </span>
      {label != null && <span>{label}</span>}
    </label>
  )
})
