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
