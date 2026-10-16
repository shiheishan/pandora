import { useCallback, useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from 'react'
import { relativeTime } from '../core/format'
import type { SseEvent } from '../core/sse'
import { useRealtime } from '../shell/runtime'
import css from './EventsCapsule.module.css'
import {
  describeEvent,
  eventsInTab,
  EVENT_TABS,
  markAllRead,
  markRead,
  pushEvent,
  unreadCount,
  type EventItem,
  type EventTab,
} from './events'

// ---------------------------------------------------------------------------
// 顶栏「实时事件」：收 SSE 推送，按 events.ts 的模型画未读、页签与跳转。
// 已读只在内存里，刷新即清（10-16 用户定）；面板开着时新事件也算未读。
// ---------------------------------------------------------------------------
export function EventsCapsule({ onOpen }: { onOpen: (path: string) => void }) {
  const [items, setItems] = useState<EventItem[]>([])
  const [open, setOpen] = useState(false)
  const [tab, setTab] = useState<EventTab>('all')
  // 最新一条的 key，只用来给它加淡入动画
  const [freshKey, setFreshKey] = useState<number | null>(null)
  const seq = useRef(0)
  const root = useRef<HTMLDivElement>(null)
  const button = useRef<HTMLButtonElement>(null)
  const [, setTick] = useState(0)

  const onEvent = useCallback((event: SseEvent) => {
    const item = describeEvent(event, ++seq.current)
    setFreshKey(item.key)
    setItems((list) => pushEvent(list, item))
  }, [])
  const status = useRealtime(true, onEvent)

  // 相对时间每 30 秒刷新一次
  useEffect(() => {
    const timer = window.setInterval(() => setTick((n) => n + 1), 30_000)
    return () => window.clearInterval(timer)
  }, [])

  // 点面板外 / Esc 关闭；Esc 关闭后焦点回到按钮
  useEffect(() => {
    if (!open) return
    const onPointer = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) setOpen(false)
    }
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setOpen(false)
        button.current?.focus()
      }
    }
    document.addEventListener('pointerdown', onPointer)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('pointerdown', onPointer)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  if (status === 'unavailable' || status === 'off') return null

  const live = status === 'open'
  const unread = unreadCount(items)
  const visible = eventsInTab(items, tab)

  const openItem = (item: EventItem) => {
    setItems((list) => markRead(list, item.key))
    if (item.path) {
      onOpen(item.path)
      setOpen(false)
    }
  }

  // 页签上左右方向键移动并切换
  const onTabKey = (event: ReactKeyboardEvent) => {
    if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
    event.preventDefault()
    const index = EVENT_TABS.findIndex(([key]) => key === tab)
    const next = EVENT_TABS[(index + (event.key === 'ArrowRight' ? 1 : EVENT_TABS.length - 1)) % EVENT_TABS.length]![0]
    setTab(next)
  }

  return (
    <div ref={root} className={css.root}>
      <button
        ref={button}
        type="button"
        className={css.capsule}
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-label={unread > 0 ? `实时事件，${unread} 条未读` : '实时事件'}
        onClick={() => setOpen(!open)}
      >
        <span className={live ? css.dotLive : css.dotWait} aria-hidden="true" />
        <span className={css.label}>实时事件</span>
        {unread > 0 && <span className={css.count}>{unread > 99 ? '99+' : unread}</span>}
      </button>
      {open && (
        <div className={css.panel} role="dialog" aria-label="实时事件">
          <div className={css.head}>
            <div className={css.headText}>
              <span className={css.headTitle}>实时事件</span>
              <span className={css.state}>
                <span className={live ? css.stateDotOk : css.stateDotWarn} aria-hidden="true" />
                {live ? '推送已连接 · 新事件会自动出现在这里' : '推送重连中 · 恢复后继续接收'}
              </span>
            </div>
            {unread > 0 && (
              <button
                type="button"
                className={css.readAll}
                onClick={() => setItems((list) => markAllRead(list))}
              >
                全部标为已读
              </button>
            )}
          </div>
          <div className={css.tabs} role="tablist" onKeyDown={onTabKey}>
            {EVENT_TABS.map(([key, label]) => (
              <button
                key={key}
                type="button"
                role="tab"
                aria-selected={tab === key}
                className={tab === key ? css.tabOn : css.tab}
                onClick={() => setTab(key)}
              >
                {label}
              </button>
            ))}
          </div>
          <div className={css.list}>
            {items.length === 0 ? (
              <div className={css.empty}>暂无事件，数据有变化时会出现在这里</div>
            ) : visible.length === 0 ? (
              <div className={css.empty}>这一类暂时没有事件</div>
            ) : (
              visible.map((item) => (
                <button
                  key={item.key}
                  type="button"
                  className={`${item.unread ? css.itemUnread : css.item}${item.key === freshKey ? ` ${css.fresh}` : ''}`}
                  onClick={() => openItem(item)}
                >
                  <span className={css[item.tone]} aria-hidden="true" />
                  <span className={css.text}>
                    <span className={css.title}>{item.title}</span>
                    {item.meta && <span className={css.meta}>{item.meta}</span>}
                  </span>
                  <span className={css.rightCol}>
                    <span className={css.at}>{relativeTime(item.at)}</span>
                    {item.unread && (
                      <>
                        <span className={css.unreadDot} aria-hidden="true" />
                        <span className={css.srOnly}>未读</span>
                      </>
                    )}
                  </span>
                </button>
              ))
            )}
          </div>
          <div className={css.footer}>
            <span>点击事件跳到对应页面处理</span>
            <button
              type="button"
              className={css.auditLink}
              onClick={() => {
                onOpen('/security/audit')
                setOpen(false)
              }}
            >
              审计日志 →
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
