/**
 * [INPUT]: 依赖 ./Skeleton，依赖 ./cx 与 ./StatStrip.module.css
 * [OUTPUT]: 对外提供 StatStrip、StatItem
 * [POS]: ui 的统计条：一排等宽统计格（标签 + 大号等宽数字），格间 1px 缝当分隔线；items 为 undefined 时画同数量的骨架格。来自后台营销页，仪表盘与各模块的汇总行共用；< 960 两列
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { ReactNode } from 'react'
import { cx } from './cx'
import { Skeleton } from './Skeleton'
import css from './StatStrip.module.css'

export interface StatItem {
  label: ReactNode
  value: ReactNode
}

export interface StatStripProps {
  /** undefined = 加载中，按 count 画骨架 */
  items: readonly StatItem[] | undefined
  /** 整组的无障碍名称，如「礼品卡统计」 */
  label: string
  /** 加载中画几格，默认 4 */
  count?: number
  className?: string
}

export function StatStrip({ items, label, count = 4, className }: StatStripProps) {
  return (
    <div className={cx(css.strip, className)} role="group" aria-label={label} aria-busy={items === undefined || undefined}>
      {items
        ? items.map((item, i) => (
            <div key={i} className={css.cell}>
              <div className={css.label}>{item.label}</div>
              <div className={css.value}>{item.value}</div>
            </div>
          ))
        : Array.from({ length: count }, (_, i) => (
            <div key={i} className={css.cell}>
              <Skeleton width={56} height={12} />
              <Skeleton width={96} height={22} className={css.value} />
            </div>
          ))}
    </div>
  )
}
