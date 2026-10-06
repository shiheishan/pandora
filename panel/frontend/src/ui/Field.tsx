import { useId, type ReactNode } from 'react'
import { cx } from './cx'
import css from './Field.module.css'

/** 各输入控件共用的字段参数 */
export interface FieldControlProps {
  label?: ReactNode
  /** 说明文字；有 error 时被错误替换 */
  hint?: ReactNode
  /** 错误文字：边框转危险色、屏幕阅读器读到 aria-invalid。规范：说原因和怎么办。空串视为没有错误（页面清错时常置空串） */
  error?: ReactNode
}

/** 有没有错误：null / undefined / 空串 / false 都算没有，否则一个清掉的错误还会留下红框 */
export function hasError(error: ReactNode): boolean {
  return error != null && error !== '' && error !== false
}

export function useFieldIds(id?: string) {
  const auto = useId()
  const controlId = id ?? auto
  return { controlId, messageId: `${controlId}-message` }
}

export function Field(props: FieldControlProps & { controlId: string; messageId: string; className?: string; children: ReactNode }) {
  const failed = hasError(props.error)
  const message = failed ? props.error : props.hint
  return (
    <div className={cx(css.field, props.className)}>
      {props.label != null && (
        <label className={css.label} htmlFor={props.controlId}>
          {props.label}
        </label>
      )}
      {props.children}
      {message != null && (
        <span id={props.messageId} className={failed ? css.error : css.hint} role={failed ? 'alert' : undefined}>
          {message}
        </span>
      )}
    </div>
  )
}
