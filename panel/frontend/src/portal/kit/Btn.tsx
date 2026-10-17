// 门户按钮（样稿 .btn）：primary 竖向渐变、hot 横向三色渐变、soft 白底、danger 红；给 href 渲染成链接，busy 时禁用并转圈
import {
  type AnchorHTMLAttributes,
  type ButtonHTMLAttributes,
  type ReactNode,
} from 'react'
import { cx } from './cx'
import css from './Btn.module.css'

export type BtnTone = 'primary' | 'hot' | 'soft' | 'danger'

interface BtnBase {
  tone?: BtnTone
  size?: 'md' | 'sm'
  grow?: boolean
  full?: boolean
  className?: string
  children: ReactNode
}

export type BtnLinkProps = BtnBase &
  { href: string } &
  Omit<AnchorHTMLAttributes<HTMLAnchorElement>, 'href' | 'className' | 'children'>

export type BtnButtonProps = BtnBase &
  { href?: undefined; busy?: boolean } &
  Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'className' | 'children'>

export type BtnProps = BtnLinkProps | BtnButtonProps

function btnClass(
  tone: BtnTone,
  size: 'md' | 'sm',
  grow: boolean,
  full: boolean,
  busy: boolean,
  className?: string,
): string {
  return cx(
    css.btn,
    tone !== 'primary' && css[tone],
    size === 'sm' && css.sm,
    grow && css.grow,
    full && css.full,
    busy && css.busy,
    className,
  )
}

export function Btn(props: BtnProps) {
  const {
    tone = 'primary',
    size = 'md',
    grow = false,
    full = false,
    className,
    children,
    ...rest
  } = props

  if ('href' in rest && rest.href !== undefined) {
    const { href, ...anchorRest } = rest as Omit<BtnLinkProps, keyof BtnBase>
    return (
      <a href={href} className={btnClass(tone, size, grow, full, false, className)} {...anchorRest}>
        {children}
      </a>
    )
  }

  const { busy = false, disabled, type = 'button', ...buttonRest } = rest as Omit<BtnButtonProps, keyof BtnBase>
  return (
    <button
      type={type}
      disabled={disabled || busy}
      aria-busy={busy || undefined}
      className={btnClass(tone, size, grow, full, busy, className)}
      {...buttonRest}
    >
      {busy && <span className={css.spinner} aria-hidden="true" />}
      {children}
    </button>
  )
}
