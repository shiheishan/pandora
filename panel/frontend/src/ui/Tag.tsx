/**
 * [INPUT]: 依赖 react 的 HTMLAttributes，依赖 ./cx 与 ./Tag.module.css
 * [OUTPUT]: 对外提供 Tag、TagTone 类型与 CountBadge
 * [POS]: ui 的标签与角标：标签统一 12px、圆角 5（规范：统一方角，不用胶囊）；状态色只表达状态，危险和错误必须同时有文字；角标用朱砂，不用深红
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { HTMLAttributes } from 'react'
import { cx } from './cx'
import css from './Tag.module.css'

// ok 在线/已支付；warn 待处理/维护中；danger 离线/失败；info 处理中；
// neutral 已关闭/草稿；brand 当前套餐；brandSolid 推荐；outline 协议名等等宽文本
export type TagTone = 'ok' | 'warn' | 'danger' | 'info' | 'neutral' | 'brand' | 'brandSolid' | 'outline'

export interface TagProps extends HTMLAttributes<HTMLSpanElement> {
  tone?: TagTone
}

export function Tag({ tone = 'neutral', className, ...rest }: TagProps) {
  return <span className={cx(css.tag, css[tone], className)} {...rest} />
}

export interface CountBadgeProps extends HTMLAttributes<HTMLSpanElement> {
  count: number
  /** 超过即显示「max+」 */
  max?: number
}

/** 未读、待办数量的圆角标；0 不渲染 */
export function CountBadge({ count, max = 99, className, ...rest }: CountBadgeProps) {
  if (count <= 0) return null
  return (
    <span className={cx(css.count, className)} {...rest}>
      {count > max ? `${max}+` : count}
    </span>
  )
}
