/**
 * [INPUT]: 依赖 react 的 context、state、ref 与 effect，依赖 ./Toast.module.css
 * [OUTPUT]: 对外提供 ToastProvider、useToast 与 ToastTone 类型
 * [POS]: ui 的轻提示：反色底（浅色模式墨底、深色模式纸白底）+ 状态小圆点，底部居中，2.6 秒消失（设计稿外壳的时长）；容器是 popover，每条新提示都把它重新推到顶层，所以弹窗开着时也看得见
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import css from './Toast.module.css'

export type ToastTone = 'ok' | 'danger'

interface ToastItem {
  id: number
  message: ReactNode
  tone: ToastTone
}

type ToastFn = (message: ReactNode, tone?: ToastTone) => void

const ToastContext = createContext<ToastFn | null>(null)

const DURATION_MS = 2600

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([])
  const nextId = useRef(1)
  const host = useRef<HTMLDivElement>(null)
  const timers = useRef(new Set<number>())

  const toast = useCallback<ToastFn>((message, tone = 'ok') => {
    const id = nextId.current++
    setItems((list) => [...list, { id, message, tone }])
    const timer = window.setTimeout(() => {
      timers.current.delete(timer)
      setItems((list) => list.filter((t) => t.id !== id))
    }, DURATION_MS)
    timers.current.add(timer)
  }, [])

  // 顶层（top layer）里后进者在上：<dialog> 用 showModal() 打开后会盖住普通元素，
  // 所以每次有新提示都先收起再展开容器，把它排到最上面
  useEffect(() => {
    const el = host.current
    if (!el || typeof el.showPopover !== 'function') return
    if (items.length === 0) {
      if (el.matches(':popover-open')) el.hidePopover()
      return
    }
    if (el.matches(':popover-open')) el.hidePopover()
    el.showPopover()
  }, [items])

  useEffect(() => {
    const pending = timers.current
    return () => pending.forEach((t) => window.clearTimeout(t))
  }, [])

  const value = useMemo(() => toast, [toast])

  return (
    <ToastContext.Provider value={value}>
      {children}
      <div ref={host} popover="manual" className={css.host} role="status" aria-live="polite">
        {items.map((t) => (
          <div key={t.id} className={css.toast}>
            <span className={t.tone === 'ok' ? css.dotOk : css.dotDanger} aria-hidden="true" />
            <span>{t.message}</span>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  )
}

export function useToast(): ToastFn {
  const toast = useContext(ToastContext)
  if (!toast) throw new Error('useToast must be used inside <ToastProvider>')
  return toast
}
