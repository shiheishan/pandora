/**
 * [INPUT]: 依赖 react 的 forwardRef 与 input 属性类型，依赖 ./cx 与 ./Switch.module.css
 * [OUTPUT]: 对外提供 Switch
 * [POS]: ui 的开关：原生 checkbox 加 role="switch"，键盘空格切换与读屏都由浏览器负责；外观 32×18，选中底色随入口（门户朱砂、后台墨色）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { forwardRef, type InputHTMLAttributes, type ReactNode } from 'react'
import { cx } from './cx'
import css from './Switch.module.css'

export interface SwitchProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'type' | 'role'> {
  /** 旁边的文字；不传时必须给 aria-label */
  label?: ReactNode
}

export const Switch = forwardRef<HTMLInputElement, SwitchProps>(function Switch({ label, className, ...rest }, ref) {
  return (
    <label className={cx(css.wrap, rest.disabled && css.disabled, className)}>
      <input ref={ref} type="checkbox" role="switch" className={css.input} {...rest} />
      <span className={css.track} aria-hidden="true">
        <span className={css.knob} />
      </span>
      {label != null && <span>{label}</span>}
    </label>
  )
})
