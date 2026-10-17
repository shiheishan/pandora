// 门户图标：按语义名取 Hugeicons，描边 1.7（样稿 svg.i）；没有 label 时只是装饰（aria-hidden）
import { HugeiconsIcon } from '@hugeicons/react'
import { cx } from './cx'
import { ICONS, type IconName } from './icons'
import css from './Bits.module.css'

export interface IconProps {
  name: IconName
  size?: number
  className?: string
  label?: string
}

export function Icon({ name, size = 20, className, label }: IconProps) {
  const a11y = label
    ? { role: 'img' as const, 'aria-label': label }
    : { 'aria-hidden': true as const }

  return (
    <HugeiconsIcon
      icon={ICONS[name]}
      size={size}
      strokeWidth={1.7}
      className={cx(css.icon, className)}
      {...a11y}
    />
  )
}
