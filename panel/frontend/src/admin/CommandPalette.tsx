import { useEffect, useId, useRef, useState, type KeyboardEvent } from 'react'
import { HugeiconsIcon } from '@hugeicons/react'
import { Search01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { navigate } from '../core/router'
import css from './CommandPalette.module.css'
import { useEntitySearch, isEntityItem } from './entity-search'
import { paletteGroups, type PaletteGroup, type PaletteItem } from './modules'

export function CommandPalette({
  open,
  onClose,
  onPick,
  sources,
}: {
  open: boolean
  onClose: () => void
  onPick: (item: PaletteItem) => void
  /** 额外的候选来源（如实体搜索）：有查询时，结果分组接在「页面」组之后 */
  sources?: ReadonlyArray<(query: string) => PaletteGroup[]>
}) {
  const baseId = useId()
  const listId = `${baseId}-list`
  const dialog = useRef<HTMLDialogElement>(null)
  const input = useRef<HTMLInputElement>(null)
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const entity = useEntitySearch(query, open)

  // 实体组（用户 / 订单 / 节点）接在「页面」之后、外部 sources 之前
  const groups = [
    ...paletteGroups(query),
    ...(query.trim() ? [...entity.groups, ...(sources ?? []).flatMap((s) => s(query))] : []),
  ].filter((g) => g.items.length > 0)
  const flat = groups.flatMap((g) => g.items)
  const index = Math.min(active, Math.max(0, flat.length - 1))

  let currentOffset = 0
  const groupsWithOffset = groups.map((group) => {
    const start = currentOffset
    currentOffset += group.items.length
    return { group, start }
  })

  useEffect(() => {
    const el = dialog.current
    if (!el) return
    if (open && !el.open) {
      setQuery('')
      setActive(0)
      el.showModal()
      input.current?.focus()
    } else if (!open && el.open) {
      el.close()
    }
  }, [open])

  useEffect(() => {
    if (flat.length === 0) return
    const el = document.getElementById(`${baseId}-opt-${index}`)
    el?.scrollIntoView({ block: 'nearest' })
  }, [baseId, index, flat.length])

  const pick = (item: PaletteItem | undefined) => {
    if (!item) return
    // 实体候选自带完整地址，直接打开那一条；其余走调用方的 onPick
    if (isEntityItem(item)) navigate(item.path)
    else onPick(item)
    onClose()
  }

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (flat.length === 0) return
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      setActive((index + 1) % flat.length)
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      setActive((index - 1 + flat.length) % flat.length)
    } else if (event.key === 'Enter') {
      event.preventDefault()
      pick(flat[index])
    }
  }

  return (
    <dialog
      ref={dialog}
      className={css.overlay}
      aria-label="搜索与跳转"
      onCancel={(event) => {
        event.preventDefault()
        onClose()
      }}
      onClick={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
    >
      {open && (
        <div className={css.panel}>
          <div className={css.inputRow}>
            <HugeiconsIcon icon={Search01Icon} size={16} strokeWidth={1.75} aria-hidden="true" className={css.searchIcon} />
            <input
              ref={input}
              className={css.input}
              value={query}
              placeholder="搜索用户邮箱、订单号、节点名，或输入模块名跳转"
              role="combobox"
              aria-expanded="true"
              aria-controls={listId}
              aria-activedescendant={flat[index] ? `${baseId}-opt-${index}` : undefined}
              onChange={(e) => {
                setQuery(e.target.value)
                setActive(0)
              }}
              onKeyDown={onKeyDown}
            />
            <kbd className={css.escKbd}>esc</kbd>
          </div>
          <div id={listId} role="listbox" aria-label="搜索结果" aria-busy={entity.pending} className={css.list}>
            {groupsWithOffset.map(({ group, start }, gIdx) => {
              const gId = `${baseId}-g-${gIdx}`
              return (
                <div key={`${group.label}-${gIdx}`} role="group" aria-labelledby={gId}>
                  <div id={gId} className={css.groupLabel}>
                    {group.label}
                  </div>
                  {group.items.map((item, i) => {
                    const flatIdx = start + i
                    const isSelected = flatIdx === index
                    const optId = `${baseId}-opt-${flatIdx}`
                    return (
                      <button
                        key={item.key}
                        id={optId}
                        type="button"
                        role="option"
                        aria-selected={isSelected}
                        tabIndex={-1}
                        className={isSelected ? `${css.item} ${css.active}` : css.item}
                        onMouseMove={() => setActive(flatIdx)}
                        onClick={() => pick(item)}
                      >
                        <span className={css.kind}>{item.kind}</span>
                        <span className={item.mono ? `${css.title} ${css.mono}` : css.title}>{item.title}</span>
                        <span className={css.sub}>{item.sub}</span>
                        {isSelected && (
                          <span className={css.enter} aria-hidden="true">
                            ↵
                          </span>
                        )}
                      </button>
                    )
                  })}
                </div>
              )
            })}
            {/* 有结果但实体搜索还在跑时，列表末尾给一行小字 */}
            {flat.length > 0 && entity.pending && (
              <div className={css.searching} aria-live="polite">
                正在搜索用户、订单、节点…
              </div>
            )}
            {flat.length === 0 && (
              <div className={css.empty}>
                {entity.pending ? '正在搜索…' : `没有找到「${query}」相关的结果`}
              </div>
            )}
          </div>
          <div className={css.footer} aria-hidden="true">
            <span>↑↓ 选择</span>
            <span>↵ 打开</span>
            <span>esc 关闭</span>
            <span className={css.footerSpacer} />
            <span className={css.shortcutHint}>⌘K / Ctrl K 随时唤起</span>
          </div>
        </div>
      )}
    </dialog>
  )
}
