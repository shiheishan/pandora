/**
 * [INPUT]: 依赖 react 的 state、ref、effect、id 与键盘事件，依赖 ./cx 与 ./Menu.module.css
 * [OUTPUT]: 对外提供 Menu 与 MenuEntry 类型
 * [POS]: ui 的下拉菜单（门户头像菜单、后台账户与行内「更多」），自己渲染触发按钮、调用方只给按钮内容与样式：宽 248、圆角 12、内边距 6，菜单项圆角 7、高随入口；按 WAI-ARIA menu button 模式实现方向键、Home/End、Esc 回到触发按钮、点外面关闭
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useCallback, useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { cx } from './cx'
import css from './Menu.module.css'

export type MenuEntry =
  | {
      kind?: 'item'
      key: string
      label: ReactNode
      onSelect: () => void
      /** 右侧辅助信息（余额等），--text-3 等宽数字 */
      hint?: ReactNode
      /** 当前页：--surface-3 底 */
      current?: boolean
      danger?: boolean
      disabled?: boolean
    }
  | {
      kind: 'toggle'
      key: string
      label: ReactNode
      checked: boolean
      onChange: (checked: boolean) => void
    }
  | { kind: 'separator'; key: string }

export interface MenuProps {
  /** 触发按钮的内容（头像、「更多」图标、文字） */
  trigger: ReactNode
  /** 触发按钮只有图标时必须给 */
  triggerLabel?: string
  triggerClassName?: string
  entries: readonly MenuEntry[]
  /** 菜单顶部的非交互内容，如用户名与邮箱 */
  header?: ReactNode
  align?: 'start' | 'end'
  label: string
  className?: string
}

export function Menu({ trigger, triggerLabel, triggerClassName, entries, header, align = 'end', label, className }: MenuProps) {
  const [open, setOpen] = useState(false)
  const menuId = useId()
  const root = useRef<HTMLDivElement>(null)
  const button = useRef<HTMLButtonElement>(null)
  const items = useRef<(HTMLButtonElement | null)[]>([])
  const pendingFocus = useRef<'first' | 'last' | null>(null)

  const focusables = () => items.current.filter((el): el is HTMLButtonElement => el != null && !el.disabled)

  const close = useCallback((restoreFocus: boolean) => {
    setOpen(false)
    if (restoreFocus) button.current?.focus()
  }, [])

  // 打开后把焦点放到第一项（或按上键打开时的最后一项）
  useEffect(() => {
    if (!open || !pendingFocus.current) return
    const list = focusables()
    ;(pendingFocus.current === 'first' ? list[0] : list[list.length - 1])?.focus()
    pendingFocus.current = null
  }, [open])

  // 点菜单外关闭；焦点离开菜单（Tab 走出去）也关闭
  useEffect(() => {
    if (!open) return
    const onPointer = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) close(false)
    }
    document.addEventListener('pointerdown', onPointer)
    return () => document.removeEventListener('pointerdown', onPointer)
  }, [open, close])

  const openWith = (focus: 'first' | 'last') => {
    pendingFocus.current = focus
    setOpen(true)
  }

  const onMenuKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const list = focusables()
    const at = list.indexOf(document.activeElement as HTMLButtonElement)
    const move = (index: number) => {
      event.preventDefault()
      list[(index + list.length) % list.length]?.focus()
    }
    if (event.key === 'ArrowDown') move(at + 1)
    else if (event.key === 'ArrowUp') move(at - 1)
    else if (event.key === 'Home') move(0)
    else if (event.key === 'End') move(list.length - 1)
    else if (event.key === 'Escape') {
      event.preventDefault()
      close(true)
    } else if (event.key === 'Tab') close(false)
  }

  let index = 0
  return (
    <div ref={root} className={cx(css.root, className)}>
      <button
        ref={button}
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        aria-label={triggerLabel}
        className={cx(css.trigger, triggerClassName)}
        onClick={() => (open ? close(false) : openWith('first'))}
        onKeyDown={(event) => {
          if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
            event.preventDefault()
            openWith(event.key === 'ArrowDown' ? 'first' : 'last')
          }
        }}
      >
        {trigger}
      </button>
      {open && (
        <div id={menuId} role="menu" aria-label={label} className={cx(css.menu, align === 'end' ? css.end : css.start)} onKeyDown={onMenuKeyDown}>
          {header != null && (
            <>
              <div className={css.header}>{header}</div>
              <div role="separator" className={css.separator} />
            </>
          )}
          {entries.map((entry) => {
            if (entry.kind === 'separator') return <div key={entry.key} role="separator" className={css.separator} />
            const slot = index++
            const ref = (el: HTMLButtonElement | null) => {
              items.current[slot] = el
            }
            if (entry.kind === 'toggle') {
              return (
                <button
                  key={entry.key}
                  ref={ref}
                  type="button"
                  role="menuitemcheckbox"
                  aria-checked={entry.checked}
                  tabIndex={-1}
                  className={css.item}
                  onClick={() => entry.onChange(!entry.checked)}
                >
                  <span className={css.label}>{entry.label}</span>
                  <span className={cx(css.switch, entry.checked && css.switchOn)} aria-hidden="true" />
                </button>
              )
            }
            return (
              <button
                key={entry.key}
                ref={ref}
                type="button"
                role="menuitem"
                tabIndex={-1}
                disabled={entry.disabled}
                aria-current={entry.current ? 'page' : undefined}
                className={cx(css.item, entry.current && css.current, entry.danger && css.danger)}
                onClick={() => {
                  close(true)
                  entry.onSelect()
                }}
              >
                <span className={css.label}>{entry.label}</span>
                {entry.hint != null && <span className={css.hint}>{entry.hint}</span>}
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}
