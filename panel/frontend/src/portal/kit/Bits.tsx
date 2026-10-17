// 门户小件：Num（展示字体数字，样稿 .num）、Chip（档位名胶囊，样稿 .chip）、Avatar（首字头像，样稿 .avatar）
import { type ReactNode } from 'react'
import { cx } from './cx'
import css from './Bits.module.css'

export interface NumProps {
  children: ReactNode
  className?: string
  as?: 'span' | 'b' | 'strong' | 'code'
}

export function Num({ children, className, as: Tag = 'span' }: NumProps) {
  return <Tag className={cx(css.num, className)}>{children}</Tag>
}

export interface ChipProps {
  children: ReactNode
  tone?: 'soft' | 'solid'
  className?: string
}

export function Chip({ children, tone = 'soft', className }: ChipProps) {
  return <span className={cx(css.chip, tone === 'solid' ? css.solid : css.soft, className)}>{children}</span>
}

export interface AvatarProps {
  name: string
  size?: 'sm' | 'md' | 'lg'
  solid?: boolean
  className?: string
}

function avatarInitial(name: string): string {
  return Array.from(name.trim())[0]?.toUpperCase() ?? '·'
}

export function Avatar({ name, size = 'sm', solid = false, className }: AvatarProps) {
  return (
    <span className={cx(css.avatar, css[size], solid && css.avatarSolid, className)} aria-hidden="true">
      {avatarInitial(name)}
    </span>
  )
}
