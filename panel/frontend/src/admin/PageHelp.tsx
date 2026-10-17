import { useEffect, useId, useState } from 'react'
import { HelpCircleIcon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import type { PageHelp } from './page-help'
import css from './PageHelp.module.css'

export function PageHelpTip({ help }: { help: PageHelp }) {
  const [open, setOpen] = useState(false)
  const tooltipId = useId()

  useEffect(() => {
    if (!open) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setOpen(false)
      }
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [open])

  return (
    <div className={css.wrap} onMouseEnter={() => setOpen(true)} onMouseLeave={() => setOpen(false)}>
      <button
        type="button"
        className={`${css.trigger}${open ? ` ${css.open}` : ''}`}
        aria-describedby={tooltipId}
        aria-expanded={open}
        aria-label={help.label}
        title={help.label}
        onFocus={() => setOpen(true)}
        onBlur={() => setOpen(false)}
      >
        <HugeiconsIcon icon={HelpCircleIcon} size={16} strokeWidth={1.6} aria-hidden="true" />
      </button>

      <div role="tooltip" id={tooltipId} className={`${css.tooltip}${open ? ` ${css.open}` : ''}`}>
        {help.sections.map((section, idx) => (
          <div key={idx} className={css.section}>
            <div className={css.sectionTitle}>{section.title}</div>
            {section.ordered ? (
              <ol className={`${css.list} ${css.orderedList}`}>
                {section.items.map((item, itemIdx) => (
                  <li key={itemIdx}>
                    {typeof item === 'string' ? (
                      item
                    ) : (
                      <>
                        <b className={css.term}>{item.term}</b>：{item.text}
                      </>
                    )}
                  </li>
                ))}
              </ol>
            ) : (
              <ul className={`${css.list} ${css.unorderedList}`}>
                {section.items.map((item, itemIdx) => (
                  <li key={itemIdx}>
                    {typeof item === 'string' ? (
                      item
                    ) : (
                      <>
                        <b className={css.term}>{item.term}</b>：{item.text}
                      </>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}
