import { useEffect, useState, type KeyboardEvent } from 'react'
import { navigate } from '../../../core/router'
import { copyTabText, isExpiredNow } from '../common/card-text'
import { isLow, type Naming } from '../common/purchase'
import type { Subscription } from '../common/subscriptions'
import css from './Subs.module.css'

export function Copies({
  held,
  naming,
  selected,
}: {
  held: readonly Subscription[]
  naming: Naming
  selected: string
}) {
  const [now, setNow] = useState(() => new Date())

  useEffect(() => {
    const timer = setInterval(() => {
      setNow(new Date())
    }, 30_000)
    return () => clearInterval(timer)
  }, [])

  const pick = (id: string) => {
    navigate('/subs', { query: { c: id }, replace: true })
  }

  const onKey = (e: KeyboardEvent<HTMLButtonElement>, currentId: string) => {
    const currentIndex = held.findIndex((s) => s.id === currentId)
    if (currentIndex === -1) return

    let nextIndex = -1
    if (e.key === 'ArrowRight' || e.key === 'ArrowDown') {
      e.preventDefault()
      nextIndex = (currentIndex + 1) % held.length
    } else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') {
      e.preventDefault()
      nextIndex = (currentIndex - 1 + held.length) % held.length
    } else if (e.key === 'Home') {
      e.preventDefault()
      nextIndex = 0
    } else if (e.key === 'End') {
      e.preventDefault()
      nextIndex = held.length - 1
    }

    if (nextIndex !== -1) {
      const nextId = held[nextIndex]!.id
      pick(nextId)
      document.getElementById(`tab-${nextId}`)?.focus()
    }
  }

  return (
    <div role="tablist" aria-label="你的几份" className={css.copies}>
      {held.map((s) => {
        const isExpired = isExpiredNow(s, now)
        const warn = (!isExpired && isLow(s)) || isExpired
        const isSelected = s.id === selected

        return (
          <button
            key={s.id}
            type="button"
            role="tab"
            id={`tab-${s.id}`}
            aria-selected={isSelected}
            aria-controls={`copy-${s.id}`}
            tabIndex={isSelected ? 0 : -1}
            className={css.cp}
            onClick={() => pick(s.id)}
            onKeyDown={(e) => onKey(e, s.id)}
          >
            <b>
              {naming.sn(s)}
              {warn && <i className={css.warnDot} aria-hidden="true" />}
            </b>{' '}
            <small>{copyTabText(s, now)}</small>
          </button>
        )
      })}
    </div>
  )
}
