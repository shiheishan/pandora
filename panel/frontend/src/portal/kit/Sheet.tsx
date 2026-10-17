import type { ReactNode } from 'react'
import { Modal, type ModalWidth } from '../../ui'
import css from './Sheet.module.css'

export interface SheetProps {
  open: boolean
  onClose: () => void
  title: ReactNode
  children?: ReactNode
  /** 底部按钮（放 kit 的 Btn，门户里会平分整行） */
  actions?: ReactNode
  footerStart?: ReactNode
  /** 默认 440（样稿 .sh-card） */
  size?: ModalWidth
  /** false 时不显示 ×，Esc 与点遮罩也不关（提交中） */
  dismissible?: boolean
  className?: string
  bodyClassName?: string
}

/** 对应样稿 .sheet / .sh-card / .sh-top / .x */
export function Sheet({
  size = 440,
  dismissible = true,
  onClose,
  open,
  title,
  children,
  actions,
  footerStart,
  className,
  bodyClassName,
}: SheetProps) {
  const closeButton = dismissible ? (
    <button
      type="button"
      className={css.close}
      aria-label="关闭"
      onClick={onClose}
    >
      ×
    </button>
  ) : undefined

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={title}
      size={size}
      dismissible={dismissible}
      headExtra={closeButton}
      actions={actions}
      footerStart={footerStart}
      className={className}
      bodyClassName={bodyClassName}
    >
      {children}
    </Modal>
  )
}
