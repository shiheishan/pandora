import { Copy01Icon, Tick02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Button, useToast } from '../../ui'
import { cx } from './cx'
import css from './Notes.module.css'

export interface CalloutProps {
  children: ReactNode
  /** 右侧操作（一个 Button size="sm"） */
  action?: ReactNode
  className?: string
}

/** 对应样稿 .callout */
export function Callout({ children, action, className }: CalloutProps) {
  return (
    <div className={cx(css.callout, className)}>
      <span className={css.calloutText}>{children}</span>
      {action}
    </div>
  )
}

export interface DangerRowProps {
  title: ReactNode
  description?: ReactNode
  /** 危险按钮的文字 */
  actionLabel: string
  onAction: () => void
  busy?: boolean
  disabled?: boolean
}

/** 对应样稿 .arch */
export function DangerRow({ title, description, actionLabel, onAction, busy, disabled }: DangerRowProps) {
  return (
    <div className={css.dangerRow}>
      <div className={css.dangerBody}>
        <b className={css.dangerTitle}>{title}</b>
        {description != null && <small className={css.dangerDescription}>{description}</small>}
      </div>
      <Button variant="danger" size="sm" busy={busy} disabled={disabled} onClick={onAction}>
        {actionLabel}
      </Button>
    </div>
  )
}

export interface CopyButtonProps {
  /** 要复制的文本 */
  value: string
  /** 无障碍名称，如「复制用户 ID」 */
  label: string
  className?: string
}

/** 对应样稿 .cpy */
export function CopyButton({ value, label, className }: CopyButtonProps) {
  const toast = useToast()
  const [copied, setCopied] = useState(false)
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    return () => {
      if (timerRef.current != null) clearTimeout(timerRef.current)
    }
  }, [])

  const onClick = async () => {
    try {
      await navigator.clipboard.writeText(value)
      toast('已复制')
      setCopied(true)
      if (timerRef.current != null) clearTimeout(timerRef.current)
      timerRef.current = setTimeout(() => {
        timerRef.current = null
        setCopied(false)
      }, 1500)
    } catch {
      toast('复制失败，请手动选择复制', 'danger')
    }
  }

  return (
    <button type="button" className={cx(css.copy, className)} aria-label={label} title={label} onClick={onClick}>
      <HugeiconsIcon
        icon={copied ? Tick02Icon : Copy01Icon}
        size={13}
        strokeWidth={1.6}
        aria-hidden
      />
    </button>
  )
}
