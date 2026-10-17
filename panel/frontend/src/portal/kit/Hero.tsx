import type { HTMLAttributes, ReactNode } from 'react'
import { cx } from './cx'
import css from './Hero.module.css'

export interface HeroProps extends Pick<HTMLAttributes<HTMLElement>, 'aria-label' | 'aria-labelledby' | 'id'> {
  tone?: 'live' | 'off' | 'empty'
  pill?: ReactNode
  pillTone?: 'plain' | 'bad'
  as?: 'section' | 'div' | 'article'
  className?: string
  children: ReactNode
}

/** 对应样稿 .hero / .hero.off / .hero.empty */
export function Hero({
  tone = 'live',
  pill,
  pillTone = 'plain',
  as = 'div',
  className,
  children,
  ...rest
}: HeroProps) {
  const Component = as
  const toneClass = tone === 'off' ? css.heroOff : tone === 'empty' ? css.heroEmpty : undefined

  return (
    <Component className={cx(css.hero, toneClass, className)} {...rest}>
      {pill != null && (
        <span className={cx(css.pill, pillTone === 'bad' && css.pillBad)}>
          {pill}
        </span>
      )}
      {children}
    </Component>
  )
}

export interface EyebrowProps {
  children: ReactNode
  className?: string
}

/** 对应样稿 .eyebrow */
export function Eyebrow({ children, className }: EyebrowProps) {
  return <div className={cx(css.eyebrow, className)}>{children}</div>
}

export interface StickerProps {
  children: ReactNode
  as?: 'div' | 'h2' | 'h3' | 'span'
  size?: 'lg' | 'md'
  className?: string
}

/** 对应样稿 .tag */
export function Sticker({
  children,
  as = 'div',
  size = 'lg',
  className,
}: StickerProps) {
  const Component = as
  return (
    <div className={css.stickerRow}>
      <Component className={cx(css.sticker, size === 'md' ? css.stickerMd : css.stickerLg, className)}>
        {children}
      </Component>
    </div>
  )
}

export interface BigNumberProps {
  value: ReactNode
  unit?: ReactNode
  prefix?: ReactNode
  size?: 'xl' | 'lg' | 'md'
  className?: string
}

/** 对应样稿 .big + .unit / .balance .amt */
export function BigNumber({
  value,
  unit,
  prefix,
  size = 'xl',
  className,
}: BigNumberProps) {
  const sizeClass = size === 'xl' ? css.sizeXl : size === 'lg' ? css.sizeLg : css.sizeMd

  return (
    <div className={cx(css.bigNumber, sizeClass, className)}>
      {prefix != null && <span className={css.bigNumPrefix}>{prefix}</span>}
      <span className={css.bigNumVal}>{value}</span>
      {unit != null && <span className={css.bigNumUnit}>{unit}</span>}
    </div>
  )
}

export interface MetaProps {
  children: ReactNode
  size?: 'md' | 'sm'
  className?: string
}

/** 对应样稿 .meta */
export function Meta({ children, size = 'md', className }: MetaProps) {
  return (
    <div className={cx(css.meta, size === 'sm' ? css.metaSm : css.metaMd, className)}>
      {children}
    </div>
  )
}

export interface BarProps {
  value: number
  low?: boolean
  label?: string
  className?: string
}

/** 对应样稿 .bar + i */
export function Bar({ value, low = false, label, className }: BarProps) {
  const clamped = Math.max(0, Math.min(1, Number.isFinite(value) ? value : 0))
  const pct = clamped * 100

  const hasLabel = label != null && label.length > 0
  const accessibilityProps = hasLabel
    ? {
        role: 'progressbar',
        'aria-label': label,
        'aria-valuemin': 0,
        'aria-valuemax': 100,
        'aria-valuenow': Math.round(pct),
      }
    : {
        'aria-hidden': true as const,
      }

  return (
    <div className={cx(css.bar, low && css.barLow, className)} {...accessibilityProps}>
      <i className={css.barFill} style={{ width: `${pct}%` }} />
    </div>
  )
}

export interface LinkChipProps {
  label?: ReactNode
  code: string
  action?: ReactNode
  className?: string
}

/** 对应样稿 .linkchip */
export function LinkChip({ label, code, action, className }: LinkChipProps) {
  return (
    <div className={cx(css.linkChip, className)}>
      {label != null && <span>{label}</span>}
      <code>{code}</code>
      {action}
    </div>
  )
}
