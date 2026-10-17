import { Cancel01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useState, type ClipboardEvent, type KeyboardEvent, type ReactNode } from 'react'
import { mergeTags, splitTagInput } from './tag-input'
import css from './TagInput.module.css'

export interface TagInputProps {
  /** 输入框的无障碍名称，如「邮箱后缀」 */
  label: string
  values: readonly string[]
  onChange: (next: string[]) => void
  /** 上限，默认 100 */
  max?: number
  placeholder?: string
  normalize?: (value: string) => string
  /** 不合格的值不加入（如后缀格式不对） */
  validate?: (value: string) => boolean
  /** 计数后面接的说明，如「粘贴多个时用逗号或换行分隔」 */
  hint?: ReactNode
  disabled?: boolean
}

/** 对应样稿 .chips / .chp */
export function TagInput({
  label,
  values,
  onChange,
  max = 100,
  placeholder,
  normalize,
  validate,
  hint,
  disabled,
}: TagInputProps) {
  const [draft, setDraft] = useState('')
  const atMax = values.length >= max
  const inputDisabled = disabled === true || atMax

  const mergeOptions = { max, normalize, validate }

  const commitDraft = (raw: string) => {
    const parts = splitTagInput(raw)
    if (parts.length === 0) return
    const { values: next } = mergeTags(values, parts, mergeOptions)
    onChange(next)
  }

  const onInputKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    // 输入法组字中的回车是选词，不提交
    if (event.nativeEvent.isComposing) return
    if (event.key === 'Enter') {
      event.preventDefault()
      commitDraft(draft)
      setDraft('')
      return
    }
    if (event.key === ',' || event.key === '，') {
      // 拦住逗号本身，免得清空草稿后又被打进输入框
      event.preventDefault()
      commitDraft(draft)
      setDraft('')
      return
    }
    if (event.key === 'Backspace' && draft.length === 0 && values.length > 0) {
      onChange(values.slice(0, -1))
    }
  }

  const onPaste = (event: ClipboardEvent<HTMLInputElement>) => {
    const text = event.clipboardData.getData('text')
    const parts = splitTagInput(text)
    if (parts.length > 1) {
      event.preventDefault()
      const { values: next } = mergeTags(values, parts, mergeOptions)
      onChange(next)
      setDraft('')
    }
  }

  const onBlur = () => {
    if (draft.trim().length === 0) return
    commitDraft(draft)
    setDraft('')
  }

  const removeAt = (index: number) => {
    onChange(values.filter((_, i) => i !== index))
  }

  return (
    <div>
      <div className={css.box}>
        {values.map((v, i) => (
          <span key={`${v}-${i}`} className={css.chip}>
            <span className={css.chipText}>{v}</span>
            <button
              type="button"
              className={css.remove}
              aria-label={`移除 ${v}`}
              disabled={disabled}
              onClick={() => removeAt(i)}
            >
              <HugeiconsIcon icon={Cancel01Icon} size={12} aria-hidden="true" />
            </button>
          </span>
        ))}
        <input
          className={css.input}
          type="text"
          aria-label={label}
          value={draft}
          disabled={inputDisabled}
          placeholder={atMax ? '已到上限' : placeholder}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={onInputKeyDown}
          onPaste={onPaste}
          onBlur={onBlur}
        />
      </div>
      <small className={css.hint}>
        已加 {values.length} / {max} 个
        {hint != null && <> · {hint}</>}
      </small>
    </div>
  )
}
