import type { ReactNode } from 'react'
import { cx } from './cx'
import { Icon } from './Icon'
import type { IconName } from './icons'
import css from './List.module.css'

export interface RowsProps {
  children: ReactNode
  className?: string
  as?: 'div' | 'ul' | 'section'
  'aria-label'?: string
}

/** 对应样稿 .rows */
export function Rows({
  children,
  className,
  as = 'div',
  'aria-label': ariaLabel,
}: RowsProps) {
  const Component = as
  return (
    <Component className={cx(css.rows, className)} aria-label={ariaLabel}>
      {children}
    </Component>
  )
}

interface RowBase {
  /** 左侧图标（样稿 .ri 方块）；与 lead 二选一 */
  icon?: IconName
  /** 图标块样式：square = .ri（默认）；round = .ico；roundMuted = .ico.out */
  iconStyle?: 'square' | 'round' | 'roundMuted'
  /** 自定义左侧内容（如头像），给了就不画 icon */
  lead?: ReactNode
  title: ReactNode
  /** 标题下的小字 .rt small */
  sub?: ReactNode
  /** 右侧值 .rv */
  value?: ReactNode
  /** text = 灰字（默认）；num = 展示字体数字、正文色；plus = 展示字体数字、主题色（入账）；action = 主题色 600 的文字（如「复制」） */
  valueTone?: 'text' | 'num' | 'plus' | 'action'
  /** 右侧任意内容（分段控件、小按钮），放在 value 之后、chevron 之前 */
  end?: ReactNode
  /** 右侧 › ；链接行与按钮行默认 true，静态行默认 false */
  chevron?: boolean
  className?: string
}

export type RowProps =
  | (RowBase & { href: string; onClick?: never; disabled?: never })
  | (RowBase & { onClick: () => void; href?: never; disabled?: boolean })
  | (RowBase & { href?: never; onClick?: never; disabled?: never })

/** 对应样稿 .row / .row.static / button.row */
export function Row({
  icon,
  iconStyle = 'square',
  lead,
  title,
  sub,
  value,
  valueTone = 'text',
  end,
  chevron,
  className,
  ...props
}: RowProps) {
  const isLink = 'href' in props && typeof props.href === 'string'
  const isButton = 'onClick' in props && typeof props.onClick === 'function'

  const showChevron = chevron ?? (isLink || isButton)

  let iconBoxClass = css.ri
  if (iconStyle === 'round') {
    iconBoxClass = css.ico
  } else if (iconStyle === 'roundMuted') {
    iconBoxClass = css.icoMuted
  }

  let valueClass = css.rv
  if (valueTone === 'num') {
    valueClass = css.rvNum
  } else if (valueTone === 'plus') {
    valueClass = css.rvPlus
  } else if (valueTone === 'action') {
    valueClass = css.rvAction
  }

  const content = (
    <>
      {lead != null ? (
        lead
      ) : icon != null ? (
        <span className={iconBoxClass}>
          <Icon name={icon} size={20} />
        </span>
      ) : null}
      <span className={css.rt}>
        {title}
        {sub != null && <small>{sub}</small>}
      </span>
      {value != null && <span className={valueClass}>{value}</span>}
      {end}
      {showChevron && <Icon name="chev" size={18} className={css.chev} />}
    </>
  )

  if (isLink) {
    return (
      <a href={props.href} className={cx(css.row, className)}>
        {content}
      </a>
    )
  }

  if (isButton) {
    return (
      <button
        type="button"
        onClick={props.onClick}
        disabled={props.disabled}
        className={cx(css.row, className)}
      >
        {content}
      </button>
    )
  }

  return <div className={cx(css.row, css.rowStatic, className)}>{content}</div>
}

export interface IconDiscProps {
  name: IconName
  tone?: 'brand' | 'muted' | 'solid'
  className?: string
}

/** 对应样稿 .ico / .ico.out / .hg .ico */
export function IconDisc({ name, tone = 'brand', className }: IconDiscProps) {
  let toneClass = css.ico
  if (tone === 'muted') {
    toneClass = css.icoMuted
  } else if (tone === 'solid') {
    toneClass = css.icoSolid
  }

  return (
    <span className={cx(toneClass, className)}>
      <Icon name={name} size={20} />
    </span>
  )
}

export interface SectionTitleProps {
  children: ReactNode
  aside?: ReactNode
  as?: 'h2' | 'h3'
  className?: string
}

/** 对应样稿 h3 / .h-row + h3 */
export function SectionTitle({
  children,
  aside,
  as = 'h2',
  className,
}: SectionTitleProps) {
  const Component = as

  if (aside != null) {
    return (
      <div className={cx(css.hRow, className)}>
        <Component className={css.sectionTitle}>{children}</Component>
        {aside}
      </div>
    )
  }

  return <Component className={cx(css.sectionTitle, className)}>{children}</Component>
}

export interface GroupHeaderProps {
  children: ReactNode
  aside?: ReactNode
  className?: string
}

/** 对应样稿 .group-h */
export function GroupHeader({ children, aside, className }: GroupHeaderProps) {
  return (
    <div className={cx(css.groupH, className)}>
      <span>{children}</span>
      {aside != null && <span>{aside}</span>}
    </div>
  )
}

interface NoteBase {
  tag?: ReactNode
  tagTone?: 'warn' | 'danger' | 'brand'
  children: ReactNode
  sub?: ReactNode
  go?: ReactNode
  className?: string
}

export type NoteProps =
  | (NoteBase & { href: string; onClick?: never })
  | (NoteBase & { onClick: () => void; href?: never })
  | (NoteBase & { href?: never; onClick?: never })

/** 对应样稿 .note */
export function Note({
  tag,
  tagTone = 'warn',
  children,
  sub,
  go,
  className,
  ...props
}: NoteProps) {
  const isLink = 'href' in props && typeof props.href === 'string'
  const isButton = 'onClick' in props && typeof props.onClick === 'function'

  let tagClass = css.noteTagWarn
  if (tagTone === 'danger') {
    tagClass = css.noteTagDanger
  } else if (tagTone === 'brand') {
    tagClass = css.noteTagBrand
  }

  const content = (
    <>
      {tag != null && <span className={cx(css.noteTag, tagClass)}>{tag}</span>}
      <span className={css.noteText}>
        {children}
        {sub != null && <small>{sub}</small>}
      </span>
      {go != null && <span className={css.noteGo}>{go}</span>}
    </>
  )

  if (isLink) {
    return (
      <a href={props.href} className={cx(css.note, className)}>
        {content}
      </a>
    )
  }

  if (isButton) {
    return (
      <button type="button" onClick={props.onClick} className={cx(css.note, className)}>
        {content}
      </button>
    )
  }

  return <div className={cx(css.note, className)}>{content}</div>
}
