import type { ReactNode } from 'react'
import css from './Flow.module.css'

// ---------------------------------------------------------------------------
// 购买流程的公共件：会发生什么、单选列表、明细行、退路。只放展示，不取数。
// ---------------------------------------------------------------------------

/** 「会发生什么」：每个选择旁边直接给结果 */
export function Callout({ title = '会发生什么', children, id }: { title?: string; children: ReactNode; id?: string }) {
  return (
    <div className={css.callout} id={id}>
      <span className={css.calloutTitle}>{title}</span>
      <div>{children}</div>
    </div>
  )
}

export interface ChoiceItem {
  key: string
  label: ReactNode
  desc?: ReactNode
  badge?: string
}

/** 单选列表（原型 .choice）：selected 为 null 时一个都不选 */
export function ChoiceList({ items, selected, onSelect, label }: { items: readonly ChoiceItem[]; selected: string | null; onSelect: (key: string) => void; label: string }) {
  return (
    <div className={css.choices} role="radiogroup" aria-label={label}>
      {items.map((it) => (
        <button key={it.key} type="button" role="radio" aria-checked={it.key === selected} className={css.choice} onClick={() => onSelect(it.key)}>
          <span className={css.dot} aria-hidden="true" />
          <span className={css.choiceText}>
            <span className={css.choiceLabel}>{it.label}</span>
            {it.badge && <em className={css.badge}>{it.badge}</em>}
            {it.desc && <span className={css.choiceDesc}>{it.desc}</span>}
          </span>
        </button>
      ))}
    </div>
  )
}

export interface ChipItem {
  key: string
  label: ReactNode
  note?: ReactNode
}

export function Chips({ items, selected, onSelect, label }: { items: readonly ChipItem[]; selected: string | null; onSelect: (key: string) => void; label: string }) {
  return (
    <div className={css.chips} role="radiogroup" aria-label={label}>
      {items.map((it) => (
        <button key={it.key} type="button" role="radio" aria-checked={it.key === selected} className={css.chip} onClick={() => onSelect(it.key)}>
          {it.label}
          {it.note && <small className={css.chipNote}>{it.note}</small>}
        </button>
      ))}
    </div>
  )
}

export interface RowItem {
  k: ReactNode
  v: ReactNode
  ok?: boolean
  /** 行下面的展开说明（「怎么算的」） */
  extra?: ReactNode
}

export function Rows({ rows }: { rows: readonly RowItem[] }) {
  return (
    <dl className={css.rows}>
      {rows.map((r, i) => (
        <div key={i}>
          <div className={r.ok ? `${css.row} ${css.rowOk}` : css.row}>
            <dt>{r.k}</dt>
            <dd>{r.v}</dd>
          </div>
          {r.extra}
        </div>
      ))}
    </dl>
  )
}

export function Notes({ items }: { items: readonly ReactNode[] }) {
  if (!items.length) return null
  return (
    <ul className={css.notes}>
      {items.map((n, i) => (
        <li key={i}>{n}</li>
      ))}
    </ul>
  )
}

/** 主按钮下面隔开的一行退路（「要给别人另买一份？」） */
export function Alt({ href, children, onClick }: { href?: string; children: ReactNode; onClick?: () => void }) {
  return (
    <div className={css.alt}>
      {href ? (
        <a className={css.altButton} href={href}>
          {children} →
        </a>
      ) : (
        <button type="button" className={css.altButton} onClick={onClick}>
          {children} →
        </button>
      )}
    </div>
  )
}

export { css as flowCss }
