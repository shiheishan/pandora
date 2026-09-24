/**
 * [INPUT]: 依赖 react 的 state / ref / effect，依赖 zod，依赖 ../core/format 的 relativeTime，依赖 ../core/sse 的 SseEvent，依赖 ../shell/runtime 的 useRealtime，依赖 ./modules 的 ModuleKey，依赖 ./EventsCapsule.module.css
 * [OUTPUT]: 对外提供 EventsCapsule 与 describeEvent
 * [POS]: admin 顶栏的「实时事件」胶囊与下拉（管理后台.dc.html toggleEv）：持有后台唯一一条 SSE 连接，事件同时驱动 react-query 失效；没有 ops.notification.read 或流返回 4xx 时整个胶囊不渲染
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { z } from 'zod'
import { relativeTime } from '../core/format'
import type { SseEvent } from '../core/sse'
import { useRealtime } from '../shell/runtime'
import css from './EventsCapsule.module.css'
import type { ModuleKey } from './modules'

// ---------------------------------------------------------------------------
// 事件只说「哪张表哪一行变了」，没有标题正文（可读事件流是待决 D-A-1）。
// 在它定下之前按契约用 topic + op 生成通用条目，点击跳到对应模块。
// ---------------------------------------------------------------------------
const TOPICS: Record<string, { label: string; module: ModuleKey | null; tab?: string; tone: 'ok' | 'warn' | 'danger' | 'info' }> = {
  'orders.changed': { label: '订单', module: 'billing', tab: 'orders', tone: 'ok' },
  'subscriptions.changed': { label: '订阅', module: 'users', tab: 'list', tone: 'info' },
  'tickets.changed': { label: '工单', module: 'tickets', tone: 'danger' },
  'ticket.updated': { label: '工单回复', module: 'tickets', tone: 'danger' },
  'plans.changed': { label: '套餐', module: 'plans', tone: 'info' },
  'nodes.changed': { label: '节点', module: 'nodes', tab: 'nodes', tone: 'warn' },
  'announcements.changed': { label: '公告', module: 'content', tab: 'announce', tone: 'info' },
}
const OPS: Record<string, string> = { INSERT: '新建', UPDATE: '更新', DELETE: '删除' }

const payloadSchema = z.object({ table: z.string().optional(), op: z.string().optional(), id: z.string().optional(), ticket_id: z.string().optional() })

export interface EventItem {
  key: number
  title: string
  body: string
  at: Date
  tone: 'ok' | 'warn' | 'danger' | 'info'
  module: ModuleKey | null
  tab?: string
}

export function describeEvent(event: SseEvent, key: number, at = new Date()): EventItem {
  const topic = TOPICS[event.event] ?? { label: '数据', module: null, tone: 'info' as const }
  let payload: z.infer<typeof payloadSchema> = {}
  try {
    const parsed = payloadSchema.safeParse(JSON.parse(event.data))
    if (parsed.success) payload = parsed.data
  } catch {
    // data 不是 JSON：只显示 topic
  }
  const op = payload.op ? (OPS[payload.op] ?? payload.op) : null
  const id = payload.id ?? payload.ticket_id
  return {
    key,
    title: op ? `${topic.label}变更 · ${op}` : `${topic.label}变更`,
    body: [payload.table, id?.slice(0, 8)].filter(Boolean).join(' · '),
    at,
    tone: topic.tone,
    module: topic.module,
    tab: topic.tab,
  }
}

const MAX_ITEMS = 12

export function EventsCapsule({ enabled, onNavigate }: { enabled: boolean; onNavigate: (module: ModuleKey, tab?: string) => void }) {
  const [items, setItems] = useState<EventItem[]>([])
  const [unread, setUnread] = useState(0)
  const [open, setOpen] = useState(false)
  const openRef = useRef(open)
  const seq = useRef(0)
  const root = useRef<HTMLDivElement>(null)
  const [, setTick] = useState(0)

  useEffect(() => {
    openRef.current = open
  })

  const onEvent = useCallback((event: SseEvent) => {
    const item = describeEvent(event, ++seq.current)
    setItems((list) => [item, ...list].slice(0, MAX_ITEMS))
    if (!openRef.current) setUnread((n) => n + 1)
  }, [])
  const status = useRealtime(enabled, onEvent)

  // 相对时间每 30 秒刷新一次
  useEffect(() => {
    const timer = window.setInterval(() => setTick((n) => n + 1), 30_000)
    return () => window.clearInterval(timer)
  }, [])

  useEffect(() => {
    if (!open) return
    const onPointer = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) setOpen(false)
    }
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false)
    }
    document.addEventListener('pointerdown', onPointer)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('pointerdown', onPointer)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  if (!enabled || status === 'unavailable' || status === 'off') return null

  const live = status === 'open'
  return (
    <div ref={root} className={css.root}>
      <button
        type="button"
        className={css.capsule}
        aria-expanded={open}
        aria-haspopup="true"
        onClick={() => {
          setOpen(!open)
          setUnread(0)
        }}
      >
        <span className={live ? css.dotLive : css.dotWait} aria-hidden="true" />
        <span>实时事件</span>
        {unread > 0 && <span className={css.count}>{unread > 99 ? '99+' : unread}</span>}
      </button>
      {open && (
        <div className={css.panel}>
          <div className={css.head}>
            <span className={css.headTitle}>实时事件</span>
            <span className={css.state}>{live ? 'SSE · 已连接' : 'SSE · 重连中…'}</span>
          </div>
          <div className={css.list}>
            {items.length === 0 && <div className={css.empty}>暂无事件，数据有变化时会出现在这里</div>}
            {items.map((item) => (
              <button
                key={item.key}
                type="button"
                className={css.item}
                disabled={!item.module}
                onClick={() => {
                  if (!item.module) return
                  onNavigate(item.module, item.tab)
                  setOpen(false)
                }}
              >
                <span className={css[item.tone]} aria-hidden="true" />
                <span className={css.text}>
                  <span className={css.title}>{item.title}</span>
                  {item.body && <span className={css.body}>{item.body}</span>}
                </span>
                <span className={css.at}>{relativeTime(item.at)}</span>
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
