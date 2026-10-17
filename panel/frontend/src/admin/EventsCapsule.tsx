import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
  type Dispatch,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
  type SetStateAction,
} from 'react'
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
// 实时动态：一个数据源 + 两种触发器。
// 数据源只此一份（EventsProvider 里调一次 useRealtime(true, onEvent)），
// 侧栏第一项（variant="nav"）与手机顶栏按钮（variant="top"）两个触发器共用，
// 各自管理弹层开关状态。已读只在内存里，刷新即清（10-16 用户定）。
// ---------------------------------------------------------------------------

type RealtimeStatus = ReturnType<typeof useRealtime>

interface EventsContextValue {
  items: EventItem[]
  setItems: Dispatch<SetStateAction<EventItem[]>>
  status: RealtimeStatus
  onOpen: (path: string) => void
  /** 最新一条的 key，只用来给它加淡入动画 */
  freshKey: number | null
}

const EventsContext = createContext<EventsContextValue | null>(null)

function useEvents(): EventsContextValue {
  const ctx = useContext(EventsContext)
  if (!ctx) throw new Error('EventsTrigger must be used inside <EventsProvider>')
  return ctx
}

/** 持有实时事件的唯一数据源：调一次 useRealtime(true, onEvent)，条目状态、相对时间刷新都在这里 */
export function EventsProvider({ onOpen, children }: { onOpen: (path: string) => void; children: ReactNode }) {
  const [items, setItems] = useState<EventItem[]>([])
  const [freshKey, setFreshKey] = useState<number | null>(null)
  const seq = useRef(0)
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

  return <EventsContext.Provider value={{ items, setItems, status, onOpen, freshKey }}>{children}</EventsContext.Provider>
}

/**
 * 实时动态的触发器 + 弹层。两种外观：
 * - nav：侧栏导航第一项（脉冲点 + 「实时动态」+ 右侧绿色等宽未读数）
 * - top：手机顶栏按钮（描边小胶囊，脉冲点 + 「实时」+ 等宽数字）
 * 推送不可用（status 为 unavailable / off）时返回 null。
 */
export function EventsTrigger({ variant }: { variant: 'nav' | 'top' }) {
  const { items, setItems, status, onOpen } = useEvents()
  const [open, setOpen] = useState(false)
  const root = useRef<HTMLDivElement>(null)
  const button = useRef<HTMLButtonElement>(null)

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

  const openItem = (item: EventItem) => {
    setItems((list) => markRead(list, item.key))
    if (item.path) {
      onOpen(item.path)
      setOpen(false)
    }
  }

  const pulse = <span className={live ? css.pulse : css.pulseWait} aria-hidden="true" />

  return (
    <div ref={root} className={variant === 'nav' ? css.navRoot : css.topRoot}>
      <button
        ref={button}
        type="button"
        className={variant === 'nav' ? css.navBtn : css.topBtn}
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-label={unread > 0 ? `实时动态，${unread} 条未读` : '实时动态'}
        onClick={() => setOpen(!open)}
      >
        {variant === 'nav' ? (
          <>
            <span className={css.dotCell}>{pulse}</span>
            <span>实时动态</span>
            {unread > 0 && <span className={css.navCount}>{unread > 99 ? '99+' : unread}</span>}
          </>
        ) : (
          <>
            {pulse}
            <span className={css.topLabel}>实时</span>
            {unread > 0 && <span className={css.topCount}>{unread > 99 ? '99+' : unread}</span>}
          </>
        )}
      </button>
      {open && (
        <div className={`${css.panel} ${variant === 'nav' ? css.panelNav : css.panelTop}`} role="dialog" aria-label="实时动态">
          <EventsPanel live={live} onOpenItem={openItem} onClose={() => setOpen(false)} />
        </div>
      )}
    </div>
  )
}

// 弹层内容：头、页签、列表、底栏，两种触发器共用；页签状态各开各的。
function EventsPanel({
  live,
  onOpenItem,
  onClose,
}: {
  live: boolean
  onOpenItem: (item: EventItem) => void
  onClose: () => void
}) {
  const { items, setItems, onOpen, freshKey } = useEvents()
  const [tab, setTab] = useState<EventTab>('all')
  const unread = unreadCount(items)
  const visible = eventsInTab(items, tab)

  // 页签上左右方向键移动并切换
  const onTabKey = (event: ReactKeyboardEvent) => {
    if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
    event.preventDefault()
    const index = EVENT_TABS.findIndex(([key]) => key === tab)
    const next = EVENT_TABS[(index + (event.key === 'ArrowRight' ? 1 : EVENT_TABS.length - 1)) % EVENT_TABS.length]![0]
    setTab(next)
  }

  return (
    <>
      <div className={css.head}>
        <div className={css.headText}>
          <span className={css.headTitle}>实时动态</span>
          <span className={css.state}>
            <span className={live ? css.stateDotOk : css.stateDotWarn} aria-hidden="true" />
            {live ? '推送已连接' : '推送重连中'}
          </span>
        </div>
        {unread > 0 && (
          <button type="button" className={css.readAll} onClick={() => setItems((list) => markAllRead(list))}>
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
              className={`${css.ev}${item.unread ? '' : ` ${css.read}`}${item.key === freshKey ? ` ${css.fresh}` : ''}`}
              onClick={() => onOpenItem(item)}
            >
              <span className={css.evDot} aria-hidden="true" />
              <span className={css.text}>
                <span className={css.title}>{item.title}</span>
                {item.meta && <span className={css.meta}>{item.meta}</span>}
              </span>
              <span className={css.rightCol}>
                <span className={css.at}>{relativeTime(item.at)}</span>
                {item.unread && <span className={css.srOnly}>未读</span>}
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
            onClose()
          }}
        >
          审计日志 →
        </button>
      </div>
    </>
  )
}
