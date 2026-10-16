import { useEffect, useState } from 'react'
import { href } from '../../../core/router'
import { Button, Empty, FilterTabs, Skeleton, Tag } from '../../../ui'
import { useTicketQueue, type QueueParams, type Ticket } from './api'
import { CATEGORY_LABELS, FILTERS, PRIORITY_VIEW, statusView, waitLabel, type QueueFilter } from './model'
import css from './Tickets.module.css'

export function Queue({
  filter,
  query,
  params,
  selected,
  onFilter,
}: {
  filter: QueueFilter
  /** 地址上的搜索词，只用来拼行的链接（搜索框在页面顶部的工具栏里） */
  query: string
  params: QueueParams
  selected: string | null
  onFilter: (f: QueueFilter) => void
}) {
  const q = useTicketQueue(params)
  const tickets = q.data?.pages.flatMap((p) => p.tickets) ?? []
  const total = q.data?.pages[0]?.total ?? 0
  const now = useNow()

  return (
    <div className={css.queue}>
      <div className={css.queueHead}>
        <FilterTabs
          label="工单筛选"
          className={css.filters}
          items={FILTERS.map(([value, label]) => ({ value, label }))}
          value={filter}
          onChange={onFilter}
        />
      </div>
      <div className={css.queueList} aria-busy={q.isFetching || undefined}>
        {q.isPending ? (
          <div className={css.queueSkeleton} role="status" aria-label="加载中">
            {Array.from({ length: 6 }, (_, i) => (
              <Skeleton key={i} height={58} />
            ))}
          </div>
        ) : q.isError && tickets.length === 0 ? (
          <Empty
            bare
            title="工单队列读取失败"
            description={q.error.message}
            action={
              <Button size="sm" onClick={() => void q.refetch()}>
                重试
              </Button>
            }
          />
        ) : tickets.length === 0 ? (
          <Empty bare title={params.q ? '没有匹配的工单' : '此分类下没有工单'} description={params.q ? '换个关键词，或切到「全部」再搜。' : '用户提交工单后会出现在这里。'} />
        ) : (
          <>
            <ul className={css.rows} aria-label={`工单列表，共 ${total} 条`}>
              {tickets.map((t) => (
                <li key={t.id}>
                  <Row t={t} on={t.id === selected} now={now} link={{ f: filter === 'active' ? undefined : filter, q: query }} />
                </li>
              ))}
            </ul>
            {q.hasNextPage && (
              <div className={css.more}>
                <Button size="xs" busy={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
                  加载更多（还有 {total - tickets.length} 条）
                </Button>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  )
}

function Row({ t, on, now, link }: { t: Ticket; on: boolean; now: Date; link: { f?: string; q: string } }) {
  const st = statusView(t.status)
  const pri = PRIORITY_VIEW[t.priority]
  const wait = waitLabel(t, now)
  // R60：最后一条人发的公开消息（不算内部备注与系统消息）是用户发的 = 等客服看，加粗（代替已读表）；已解决、已关闭的不算
  const unread = t.last_message_author_kind === 'user' && t.status !== 'resolved' && t.status !== 'closed'
  return (
    <a className={on ? `${css.row} ${css.rowOn}` : css.row} href={href(`/tickets/${encodeURIComponent(t.id)}`, link)} aria-current={on ? 'true' : undefined}>
      <span className={css.rowTop}>
        <span className={`${css.pri} ${css[`pri_${pri.dot}`]}`} title={`优先级：${pri.label}`} aria-label={`优先级${pri.label}`} />
        <span className={css.no}>{t.ticket_no}</span>
        <Tag tone={st.tone}>{st.label}</Tag>
        {t.escalated_at && <span className={css.escalatedMark}>已升级</span>}
      </span>
      <span className={unread ? `${css.subject} ${css.unread}` : css.subject}>{t.subject}</span>
      <span className={css.rowBottom}>
        <span className={css.meta}>
          {t.user_email ?? '—'} · {CATEGORY_LABELS[t.category]}
          {t.assignee_email ? ` · ${t.assignee_email}` : ''}
        </span>
        <span className={wait.late ? `${css.wait} ${css.late}` : css.wait}>{wait.text}</span>
      </span>
    </a>
  )
}

/** 等待时长每分钟走一次，不必跟着查询刷新 */
export function useNow(intervalMs = 60_000): Date {
  const [now, setNow] = useState(() => new Date())
  useEffect(() => {
    const timer = setInterval(() => setNow(new Date()), intervalMs)
    return () => clearInterval(timer)
  }, [intervalMs])
  return now
}
