/**
 * [INPUT]: 依赖 react 的 useId 与 ReactNode，依赖 ./cx 与 ./Field.module.css
 * [OUTPUT]: 对外提供 Field 组件、FieldControlProps 类型、useFieldIds 与 hasError
 * [POS]: ui 的表单字段外壳：标签 12px 在上、说明或错误 12px 在下；Input / TextArea / Select 都包在它里面，并由它生成 id 把 label、aria-describedby、aria-invalid 接好；「算不算错误」只由 hasError 一处判定（空串、false 不算），各控件的 aria-invalid 与这里的错误行同口径
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
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
