/**
 * [INPUT]: 依赖 react 的 CSSProperties，依赖 ./cx 与 ./Skeleton.module.css
 * [OUTPUT]: 对外提供 Skeleton
 * [POS]: ui 的加载骨架：--surface-3 块，尺寸应与真实内容一致；规范要求超过 300ms 才出现，用 CSS 动画延迟实现，快请求不闪
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
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
