import type { CSSProperties } from 'react'
import { cx } from './cx'
import css from './Skeleton.module.css'

export interface SkeletonProps {
  width?: CSSProperties['width']
  height?: CSSProperties['height']
  radius?: CSSProperties['borderRadius']
  className?: string
}

export function Skeleton({ width = '100%', height = 12, radius, className }: SkeletonProps) {
  return <span aria-hidden="true" className={cx(css.skeleton, className)} style={{ width, height, borderRadius: radius }} />
}
