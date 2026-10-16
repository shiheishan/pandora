import { useId, useState, type ReactNode } from 'react'
import { Button } from './Button'
import { cx } from './cx'
import css from './Modal.module.css'
import { useModalDialog } from './useModalDialog'

/** 弹窗宽度档位（设计稿）：sm=400、md=560、lg=720 是旧名字，等价于对应数字 */
export type ModalWidth = 400 | 440 | 480 | 520 | 560 | 640 | 680 | 720 | 760

export interface ModalProps {
  open: boolean
  onClose: () => void
  title: ReactNode
  /** 标题上方的小字，如「敏感操作 · 需要重新认证」 */
  eyebrow?: ReactNode
  /** 标题下的一行灰字说明 */
  subtitle?: ReactNode
  /** 标题行右侧（如状态标签、关闭按钮） */
  headExtra?: ReactNode
  /** 底部右侧操作：次操作在前、主操作在后（size="dialog" 的 Button） */
  actions?: ReactNode
  /** 底部左侧（如危险的文字按钮「作废」「删除」） */
  footerStart?: ReactNode
  /** 宽度：数字档位或旧名字，默认 400 */
  size?: 'sm' | 'md' | 'lg' | ModalWidth
  /** 详情类：高度固定为 min(860px, 100vh - 32px)，头、底固定，内容区滚动 */
  tall?: boolean
  /** 头部与内容、内容与底部之间画分隔线（内容会滚动的弹窗用） */
  divided?: boolean
  /** false 时 Esc 与点遮罩都不关闭，用于进行中的提交 */
  dismissible?: boolean
  className?: string
  /** 内容区的 className（如去掉内边距，放整块页签） */
  bodyClassName?: string
  children?: ReactNode
}

function resolveWidth(size: ModalProps['size']): number {
  if (typeof size === 'number') return size
  if (size === 'md') return 560
  if (size === 'lg') return 720
  return 400
}

export function Modal({
  open,
  onClose,
  title,
  eyebrow,
  subtitle,
  headExtra,
  actions,
  footerStart,
  size = 400,
  tall = false,
  divided = false,
  dismissible = true,
  className,
  bodyClassName,
  children,
}: ModalProps) {
  const { ref, onCancel, onClick } = useModalDialog(open, onClose, dismissible)
  const titleId = useId()
  const px = `${resolveWidth(size)}px`

  return (
    <dialog ref={ref} className={css.overlay} aria-labelledby={titleId} onCancel={onCancel} onClick={onClick}>
      {open && (
        <div
          data-dialog-panel=""
          tabIndex={-1}
          className={cx(css.panel, tall && css.tall, divided && css.divided, className)}
          style={{ width: px }}
        >
          <div className={css.head}>
            {eyebrow != null && <div className={css.eyebrow}>{eyebrow}</div>}
            <div className={css.titleRow}>
              <h2 id={titleId} className={css.title}>
                {title}
              </h2>
              {headExtra}
            </div>
            {subtitle != null && <div className={css.subtitle}>{subtitle}</div>}
          </div>
          {children != null && <div className={cx(css.body, bodyClassName)}>{children}</div>}
          {(actions != null || footerStart != null) && (
            <div className={css.foot}>
              {footerStart}
              <span className={css.grow} />
              {actions}
            </div>
          )}
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
