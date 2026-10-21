import { useEffect, useState } from 'react'
import { Input } from '../../../ui'

/** 筛选输入框：停手 300ms 后回写地址；地址被外部改了（前进后退、从别的页面带筛选跳进来）跟着地址走 */
export function DebouncedInput({
  value,
  onChange,
  placeholder,
  'aria-label': ariaLabel,
  mono,
  fieldClassName,
}: {
  value: string
  onChange: (text: string) => void
  placeholder: string
  'aria-label': string
  mono?: boolean
  fieldClassName?: string
}) {
  const [text, setText] = useState(value)
  const [synced, setSynced] = useState(value)
  if (value !== synced) {
    setSynced(value)
    setText(value)
  }
  useEffect(() => {
    if (text === value) return
    const timer = setTimeout(() => onChange(text), 300)
    return () => clearTimeout(timer)
  }, [text, value, onChange])
  return <Input size="sm" mono={mono} aria-label={ariaLabel} fieldClassName={fieldClassName} placeholder={placeholder} value={text} onChange={(e) => setText(e.target.value)} />
}
