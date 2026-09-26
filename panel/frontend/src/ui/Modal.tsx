/**
 * [INPUT]: 依赖 ./useModalDialog、./Button、./cx、./Modal.module.css
 * [OUTPUT]: 对外提供 Modal 与 ConfirmModal
 * [POS]: ui 的弹窗：宽 400、圆角随入口（门户 14、后台 12）、内边距 22；< 640 时变成底部抽屉（贴底通栏、上圆角 16、按钮通栏），即 brief 里的 Modal/Sheet
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useId, useState, type ReactNode } from 'react'
import { Button } from './Button'
import { cx } from './cx'
import css from './Modal.module.css'
import { useModalDialog } from './useModalDialog'

export interface ModalProps {
  open: boolean
  onClose: () => void
  title: ReactNode
  /** 标题上方的小字，如「敏感操作 · 需要重新认证」 */
  eyebrow?: ReactNode
  /** 底部操作区，放 size="dialog" 的 Button；次操作在前、主操作在后。打开时的焦点给标了 data-autofocus 的元素，没有就给面板 */
  actions?: ReactNode
  /** sm 400（默认）、md 560、lg 720（套餐向导） */
  size?: 'sm' | 'md' | 'lg'
  /** false 时 Esc 与点遮罩都不关闭，用于进行中的提交 */
  dismissible?: boolean
  className?: string
  children?: ReactNode
}

export function Modal({ open, onClose, title, eyebrow, actions, size = 'sm', dismissible = true, className, children }: ModalProps) {
  const { ref, onCancel, onClick } = useModalDialog(open, onClose, dismissible)
  const titleId = useId()
  return (
    <dialog ref={ref} className={css.overlay} aria-labelledby={titleId} onCancel={onCancel} onClick={onClick}>
      {open && (
        <div data-dialog-panel="" tabIndex={-1} className={cx(css.panel, css[size], className)}>
          <div className={css.head}>
            {eyebrow != null && <div className={css.eyebrow}>{eyebrow}</div>}
            <h2 id={titleId} className={css.title}>
              {title}
            </h2>
          </div>
          {children != null && <div className={css.body}>{children}</div>}
          {actions != null && <div className={css.actions}>{actions}</div>}
        </div>
      )}
    </dialog>
  )
}

export interface ConfirmModalProps {
  open: boolean
  title: ReactNode
  /** 规范：先说后果，不写「确定要执行此操作吗？」 */
  body: ReactNode
  /** 规范：动词开头写清楚结果，不写「确定」 */
  confirmLabel: string
  cancelLabel?: string
  tone?: 'primary' | 'danger'
  /** 返回 Promise 时按钮显示进行中，期间不能关闭 */
  onConfirm: () => void | Promise<void>
  onCancel: () => void
}

export function ConfirmModal({ open, title, body, confirmLabel, cancelLabel = '取消', tone = 'primary', onConfirm, onCancel }: ConfirmModalProps) {
  const [busy, setBusy] = useState(false)
  const confirm = async () => {
    setBusy(true)
    try {
      await onConfirm()
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      open={open}
      onClose={onCancel}
      title={title}
      dismissible={!busy}
      actions={
        <>
          <Button size="dialog" onClick={onCancel} disabled={busy}>
            {cancelLabel}
          </Button>
          <Button size="dialog" variant={tone} busy={busy} onClick={confirm}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      {body}
    </Modal>
  )
}
