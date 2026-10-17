import type { CSSProperties, ReactNode } from 'react'
import { cx } from './cx'
import css from './Facts.module.css'

export interface AvatarProps {
  /** 取首字显示 */
  text: string
  size?: 'sm' | 'lg'
  className?: string
}

/** 对应样稿 .av */
export function Avatar({ text, size = 'sm', className }: AvatarProps) {
  const initial = Array.from(text)[0] ?? ''
  return (
    <span
      className={cx(css.avatar, size === 'lg' ? css.avatarLg : css.avatarSm, className)}
      aria-hidden
    >
      {initial}
    </span>
  )
}

export function SideFacts({ children, label }: { children: ReactNode; label?: string }) {
  return (
    <dl className={css.facts} aria-label={label}>
      {children}
    </dl>
  )
}

export interface SideFactProps {
  label: ReactNode
  children: ReactNode
  /** 行内操作（小按钮、链接），在值的右侧 */
  action?: ReactNode
}

export function SideFact({ label, children, action }: SideFactProps) {
  return (
    <div className={css.fact}>
      <dt className={css.factLabel}>{label}</dt>
      <dd className={css.factValue}>{children}</dd>
      {action != null && <dd className={css.factAction}>{action}</dd>}
    </div>
  )
}

export interface KvListProps {
  children: ReactNode
  /** 装进白底描边圆角盒子（样稿 .bgc + .kvb），默认 false */
  boxed?: boolean
  /** 标签列宽，默认 120 */
  labelWidth?: number
  className?: string
}

export function KvList({ children, boxed = false, labelWidth = 120, className }: KvListProps) {
  return (
    <div
      className={cx(css.kv, boxed && css.kvBoxed, className)}
      style={{ '--kv-label': `${labelWidth}px` } as CSSProperties}
    >
      {children}
    </div>
  )
}

export interface KvRowProps {
  label: ReactNode
  children: ReactNode
  /** 右侧小操作（「编辑」链接按钮） */
  action?: ReactNode
  /** 置灰（样稿 .kv.old，已失效的旧值） */
  muted?: boolean
}

export function KvRow({ label, children, action, muted }: KvRowProps) {
  return (
    <div className={cx(css.kvRow, muted && css.kvMuted)}>
      <span className={css.kvLabel}>{label}</span>
      <span className={css.kvValue}>{children}</span>
      {action != null ? <span className={css.kvAction}>{action}</span> : <span className={css.kvAction} />}
    </div>
  )
}

export type StatusTone = 'ok' | 'warn' | 'off' | 'bad'

export interface StatusDotProps {
  tone: StatusTone
  children: ReactNode
  className?: string
}

const statusToneClass: Record<StatusTone, string | undefined> = {
  ok: css.statusOk,
  warn: css.statusWarn,
  off: css.statusOff,
  bad: css.statusBad,
}

/** 对应样稿 .st */
export function StatusDot({ tone, children, className }: StatusDotProps) {
  return (
    <span className={cx(css.status, statusToneClass[tone], className)}>
      <span className={css.statusText}>{children}</span>
    </span>
  )
}

export interface SectionTitleProps {
  children: ReactNode
  /** 标题后的灰色小字 */
  hint?: ReactNode
  /** 靠右的操作（「编辑」「查看全部」） */
  action?: ReactNode
  /** block = 详情主栏分节标题（样稿 .dh3，默认）；group = 设置页里盒子上方的分组标题（样稿 .sect） */
  variant?: 'block' | 'group'
  as?: 'h2' | 'h3'
}

/** 对应样稿 .dh3 / .sect */
export function SectionTitle({
  children,
  hint,
  action,
  variant = 'block',
  as = 'h3',
}: SectionTitleProps) {
  const Component = as

  if (variant === 'group') {
    return (
      <Component className={css.sectionGroup}>
        {children}
        {hint != null && <small className={css.hintGroup}>{hint}</small>}
        {action != null && <span className={css.sectionAction}>{action}</span>}
      </Component>
    )
  }

  return (
    <Component className={css.sectionBlock}>
      {children}
      {hint != null && <small className={css.hintBlock}>{hint}</small>}
      {action != null && <span className={css.sectionAction}>{action}</span>}
    </Component>
  )
}
