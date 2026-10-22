import type { ReactNode } from 'react'
import css from './Flow.module.css'

// ---------------------------------------------------------------------------
// 购买流程的公共件：会发生什么、单选列表、小选项。只放展示，不取数。
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

interface ChoiceItem {
  key: string
  label: ReactNode
  desc?: ReactNode
  badge?: string
  /** 不可选：置灰、点了没反应，desc 写原因 */
  disabled?: boolean
}

/** 单选列表（原型 .choice）：selected 为 null 时一个都不选 */
export function ChoiceList({ items, selected, onSelect, label }: { items: readonly ChoiceItem[]; selected: string | null; onSelect: (key: string) => void; label: string }) {
  return (
    <div className={css.choices} role="radiogroup" aria-label={label}>
      {items.map((it) => (
        <button
          key={it.key}
          type="button"
          role="radio"
          aria-checked={it.key === selected}
          disabled={it.disabled}
          aria-disabled={it.disabled ? 'true' : undefined}
          className={it.disabled ? `${css.choice} ${css.choiceOff}` : css.choice}
          onClick={() => {
            if (!it.disabled) onSelect(it.key)
          }}
        >
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

interface ChipItem {
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

export { css as flowCss }
