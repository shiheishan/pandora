/**
 * [INPUT]: 依赖 react 的 ReactNode，依赖 ./logic 的 Tone，依赖 ./system.module.css
 * [OUTPUT]: 对外提供 ChannelCard（通知渠道卡片外壳）
 * [POS]: admin/screens/system 通知渠道三张卡共用的外壳（设计稿 channels）：标题栏名称 + 状态圆点，两列字段区，浅底底栏放测试与保存
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { ReactNode } from 'react'
import type { Tone } from './logic'
import css from './system.module.css'

export function ChannelCard({ title, status, children, foot }: { title: string; status: { label: string; tone: Tone }; children: ReactNode; foot: ReactNode }) {
  return (
    <section className={css.card} aria-label={title}>
      <div className={css.cardHead}>
        <span className={css.cardTitle}>{title}</span>
        <span className={`${css.status} ${css[status.tone]}`}>
          <span className={css.dot} aria-hidden="true" />
          {status.label}
        </span>
      </div>
      <div className={css.cardBody}>{children}</div>
      <div className={css.cardFoot}>{foot}</div>
    </section>
  )
}
