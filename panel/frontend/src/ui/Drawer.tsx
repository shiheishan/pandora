/**
 * [INPUT]: 依赖 ./useModalDialog、./icons 的 IconClose、./cx、./Drawer.module.css
 * [OUTPUT]: 对外提供 Drawer
 * [POS]: ui 的侧边抽屉：后台详情（用户 560、订单 480、节点）从右侧滑出，头部固定、内容滚动、底部可放操作；< 640 占满全宽
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useId, type ReactNode } from 'react'
import { cx } from './cx'
import css from './Drawer.module.css'
import { IconClose } from './icons'
import { useModalDialog } from './useModalDialog'

export interface DrawerProps {
  open: boolean
  onClose: () => void
  title: ReactNode
  /** 标题下一行：状态标签、时间 */
  subtitle?: ReactNode
  /** 头部右侧、关闭按钮之前的操作 */
  extra?: ReactNode
  /** 贴在标题下方的内容，如 Tabs */
  toolbar?: ReactNode
  actions?: ReactNode
  /** 面板宽度，默认 480 */
  width?: number
  dismissible?: boolean
  children?: ReactNode
}

export function Drawer({ open, onClose, title, subtitle, extra, toolbar, actions, width = 480, dismissible = true, children }: DrawerProps) {
  const { ref, onCancel, onClick } = useModalDialog(open, onClose, dismissible)
  const titleId = useId()
  return (
    <dialog ref={ref} className={css.overlay} aria-labelledby={titleId} onCancel={onCancel} onClick={onClick}>
      {open && (
        <aside data-dialog-panel="" tabIndex={-1} className={css.panel} style={{ width }}>
          <header className={cx(css.head, toolbar != null && css.headWithToolbar)}>
            <div className={css.headRow}>
              <div className={css.headText}>
                <h2 id={titleId} className={css.title}>
                  {title}
                </h2>
                {subtitle != null && <div className={css.subtitle}>{subtitle}</div>}
              </div>
              {extra}
              <button type="button" className={css.close} aria-label="关闭" onClick={onClose} disabled={!dismissible}>
                <IconClose />
              </button>
            </div>
            {toolbar}
          </header>
          <div className={css.body}>{children}</div>
          {actions != null && <footer className={css.actions}>{actions}</footer>}
        </aside>
      )}
    </dialog>
  )
}
