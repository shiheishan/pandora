/**
 * [INPUT]: 依赖 react 的 state / ref / effect，依赖 ./modules 的 paletteItems / PaletteItem / Permissions，依赖 ./CommandPalette.module.css
 * [OUTPUT]: 对外提供 CommandPalette
 * [POS]: admin 的 ⌘K 命令面板（管理后台.dc.html pal）：原生 <dialog> 顶部 12vh、宽 560，输入即筛选有读权限的模块、标签与深链，↑↓ 选择、↵ 打开、Esc 关闭；纯前端，无接口
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useEffect, useRef, useState, type KeyboardEvent } from 'react'
import css from './CommandPalette.module.css'
import { paletteItems, type PaletteItem, type Permissions } from './modules'

const NO_PERMISSIONS: Permissions = new Set()

export function CommandPalette({
  open,
  perms = NO_PERMISSIONS,
  onClose,
  onPick,
}: {
  open: boolean
  perms?: Permissions
  onClose: () => void
  onPick: (item: PaletteItem) => void
}) {
  const dialog = useRef<HTMLDialogElement>(null)
  const input = useRef<HTMLInputElement>(null)
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const items = paletteItems(query, perms)
  const index = Math.min(active, Math.max(0, items.length - 1))

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

  const pick = (item: PaletteItem | undefined) => {
    if (!item) return
    onPick(item)
    onClose()
  }

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      setActive(Math.min(index + 1, items.length - 1))
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      setActive(Math.max(index - 1, 0))
    } else if (event.key === 'Enter') {
      event.preventDefault()
      pick(items[index])
    }
  }

  return (
    <dialog
      ref={dialog}
      className={css.overlay}
      aria-label="搜索或跳转"
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
          <input
            ref={input}
            className={css.input}
            value={query}
            placeholder="跳转到页面，如「礼品卡」「REALITY」「提现」"
            role="combobox"
            aria-expanded="true"
            aria-controls="palette-list"
            aria-activedescendant={items[index] ? `palette-${index}` : undefined}
            onChange={(e) => {
              setQuery(e.target.value)
              setActive(0)
            }}
            onKeyDown={onKeyDown}
          />
          <div id="palette-list" role="listbox" className={css.list}>
            {items.map((item, i) => (
              <button
                key={item.key}
                id={`palette-${i}`}
                type="button"
                role="option"
                aria-selected={i === index}
                tabIndex={-1}
                className={i === index ? `${css.item} ${css.active}` : css.item}
                onMouseMove={() => setActive(i)}
                onClick={() => pick(item)}
              >
                <span className={css.title}>{item.title}</span>
                <span className={css.path}>{item.path}</span>
              </button>
            ))}
            {items.length === 0 && <div className={css.empty}>没有匹配的页面</div>}
          </div>
          <div className={css.keys} aria-hidden="true">
            <span>↑↓ 选择</span>
            <span>↵ 打开</span>
            <span>esc 关闭</span>
          </div>
        </div>
      )}
    </dialog>
  )
}
