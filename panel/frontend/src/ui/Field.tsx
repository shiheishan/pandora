/**
 * [INPUT]: 依赖 react 的 useId 与 ReactNode，依赖 ./cx 与 ./Field.module.css
 * [OUTPUT]: 对外提供 Field 组件、FieldControlProps 类型与 useFieldIds
 * [POS]: ui 的表单字段外壳：标签 12px 在上、说明或错误 12px 在下；Input / TextArea / Select 都包在它里面，并由它生成 id 把 label、aria-describedby、aria-invalid 接好
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
  /** 错误文字：边框转危险色、屏幕阅读器读到 aria-invalid。规范：说原因和怎么办 */
  error?: ReactNode
}

export function useFieldIds(id?: string) {
  const auto = useId()
  const controlId = id ?? auto
  return { controlId, messageId: `${controlId}-message` }
}

export function Field(props: FieldControlProps & { controlId: string; messageId: string; className?: string; children: ReactNode }) {
  const message = props.error ?? props.hint
  return (
    <div className={cx(css.field, props.className)}>
      {props.label != null && (
        <label className={css.label} htmlFor={props.controlId}>
          {props.label}
        </label>
      )}
      {props.children}
      {message != null && (
        <span id={props.messageId} className={props.error != null ? css.error : css.hint} role={props.error != null ? 'alert' : undefined}>
          {message}
        </span>
      )}
    </div>
  )
}
