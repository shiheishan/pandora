import { Cancel01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { type KeyboardEvent, type ReactNode } from 'react'
import { Button } from '../../ui'
import { cx } from './cx'
import css from './Rows.module.css'

// ---------------------------------------------------------------------------
// 行式列表（样稿 .rows / .rw）：表格之外的轻量列表，如佣金设置、待复核提现、钩子。
// ---------------------------------------------------------------------------

export interface RowListProps {
  children: ReactNode
  /** 列表的无障碍名称 */
  label?: string
  className?: string
}

/** 对应样稿 .rows */
export function RowList({ children, label, className }: RowListProps) {
  return (
    <div role="list" aria-label={label} className={cx(css.rows, className)}>
      {children}
    </div>
  )
}

export interface RowProps {
  /** 最左：StatusDot、图标或头像 */
  lead?: ReactNode
  /** 主文案（第一行，13.5 / 500） */
  title: ReactNode
  /** 第二行灰字（12.5、--text-3） */
  meta?: ReactNode
  /** 第三行（标签、补充说明） */
  extra?: ReactNode
  /** 右侧金额或主数字（样稿 .ra：14 / 500，右对齐，最小宽 80） */
  value?: ReactNode
  /** 最右的操作（链接按钮、小按钮） */
  actions?: ReactNode
  /** 整行可点（样稿 .rw.clk）：悬停浅底；行里不要再放按钮 */
  onClick?: () => void
  /** 竖向对齐，默认 center；多行说明较长时用 start */
  align?: 'center' | 'start'
}

/** 对应样稿 .rw */
export function Row({ lead, title, meta, extra, value, actions, onClick, align }: RowProps) {
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    // 只认外层自己收到的按键，行内按钮等交互元素照常工作
    if (e.target !== e.currentTarget) return
    if (e.key !== 'Enter' && e.key !== ' ') return
    e.preventDefault()
    onClick?.()
  }

  return (
    <div
      role="listitem"
      className={cx(css.row, align === 'start' && css.start, onClick && css.clickable)}
      tabIndex={onClick ? 0 : undefined}
      onClick={onClick}
      onKeyDown={onClick ? onKeyDown : undefined}
    >
      {lead != null && <span className={css.lead}>{lead}</span>}
      <div className={css.main}>
        <b className={css.title}>{title}</b>
        {meta != null && <small className={css.meta}>{meta}</small>}
        {extra != null && <div className={css.extra}>{extra}</div>}
      </div>
      {value != null && <span className={css.value}>{value}</span>}
      {actions != null && <span className={css.actions}>{actions}</span>}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 批量条（样稿 .bulk）：节点列表勾选后出现的实心条，count 为 0 不渲染。
// ---------------------------------------------------------------------------

export interface BulkBarProps {
  /** 已选条数；为 0 时组件不渲染 */
  count: number
  /** 一组 BulkAction */
  children: ReactNode
  /** 取消选择 */
  onClear: () => void
}

/** 对应样稿 .bulk */
export function BulkBar({ count, children, onClear }: BulkBarProps) {
  if (count <= 0) return null
  return (
    <div role="toolbar" aria-label="批量操作" className={css.bulk}>
      <b>已选 {count}</b>
      <span className={css.spacer} />
      {children}
      <button type="button" className={css.clear} aria-label="取消选择" onClick={onClear}>
        <HugeiconsIcon icon={Cancel01Icon} size={16} aria-hidden="true" />
      </button>
    </div>
  )
}

export interface BulkActionProps {
  children: ReactNode
  onClick: () => void
  disabled?: boolean
  busy?: boolean
}

/** 对应样稿 .bulk 里的按钮 */
export function BulkAction({ children, onClick, disabled, busy }: BulkActionProps) {
  return (
    <button
      type="button"
      className={css.action}
      disabled={disabled || busy}
      aria-busy={busy || undefined}
      onClick={onClick}
    >
      {children}
    </button>
  )
}

// ---------------------------------------------------------------------------
// 吸底保存条（样稿 .savebar）：有未保存改动时出现在内容列底部。
// ---------------------------------------------------------------------------

export interface SaveBarProps {
  /** 有未保存的改动；false 时不渲染 */
  dirty: boolean
  onSave: () => void
  onReset: () => void
  busy?: boolean
  /** 左侧主文案，默认「有未保存的改动」 */
  message?: ReactNode
  /** 主文案后的灰字（如「保存时要再验证一次身份」），窄于 640 隐藏 */
  hint?: ReactNode
  saveLabel?: string
  resetLabel?: string
}

/** 对应样稿 .savebar */
export function SaveBar({
  dirty,
  onSave,
  onReset,
  busy,
  message = '有未保存的改动',
  hint,
  saveLabel = '保存',
  resetLabel = '还原',
}: SaveBarProps) {
  if (!dirty) return null
  return (
    <div role="region" aria-label="未保存的改动" className={css.bar}>
      <span>{message}</span>
      {hint != null && <small className={css.hint}>{hint}</small>}
      <span className={css.spacer} />
      <Button size="sm" variant="secondary" onClick={onReset} disabled={busy}>
        {resetLabel}
      </Button>
      <Button size="sm" variant="primary" busy={busy} onClick={onSave}>
        {saveLabel}
      </Button>
    </div>
  )
}
