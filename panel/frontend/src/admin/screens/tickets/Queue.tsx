/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useMutation，依赖 react 的 useEffect / useState，依赖 ../../../core/router 的 href，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 Button / Empty / Segmented / Skeleton / Tag / useToast，依赖 ../../actions 的 useFailure / useIntentKey，依赖 ./api、./model，依赖 ./Tickets.module.css
 * [OUTPUT]: 对外提供 Queue
 * [POS]: 工单页左栏：分段筛选（未解决 / 待处理 / 我的 / 超时 / 全部）、搜索（防抖后交给后端 q）、「检查 SLA 超时」（POST v1/tickets/escalate，待补·前端）、工单列表与「加载更多」；选中项由地址 #/tickets/<id> 决定，列表只负责拼链接
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { href } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Empty, Segmented, Skeleton, Tag, useToast } from '../../../ui'
import { useFailure, useIntentKey } from '../../actions'
import { escalatedSchema, useInvalidateTickets, useTicketQueue, type QueueParams, type Ticket } from './api'
import { CATEGORY_LABELS, FILTERS, PRIORITY_VIEW, statusView, waitLabel, type QueueFilter } from './model'
import css from './Tickets.module.css'

export function Queue({
  filter,
  query,
  params,
  selected,
  canWrite,
  onFilter,
  onQuery,
}: {
  filter: QueueFilter
  query: string
  params: QueueParams
  selected: string | null
  canWrite: boolean
  onFilter: (f: QueueFilter) => void
  onQuery: (q: string) => void
}) {
  const q = useTicketQueue(params)
  const tickets = q.data?.pages.flatMap((p) => p.tickets) ?? []
  const total = q.data?.pages[0]?.total ?? 0
  const now = useNow()

  return (
    <div className={css.queue}>
      <div className={css.queueHead}>
        <Segmented size="sm" label="工单筛选" className={css.filters} options={FILTERS.map(([value, label]) => ({ value, label }))} value={filter} onChange={onFilter} />
        <div className={css.searchRow}>
          <SearchBox value={query} onChange={onQuery} />
          {canWrite && <EscalateButton />}
        </div>
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
  // R60：最后一条非内部备注是用户发的 = 等客服看，加粗（代替已读表）
  const unread = t.last_message_author_kind === 'user'
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

/** 搜索框：输入 300ms 后才回写地址（后端 ILIKE，别每个字都打一次） */
function SearchBox({ value, onChange }: { value: string; onChange: (q: string) => void }) {
  const [text, setText] = useState(value)
  const [synced, setSynced] = useState(value)
  if (value !== synced) {
    // 地址被外部改了（切筛选、前进后退）：跟着地址走
    setSynced(value)
    setText(value)
  }
  useEffect(() => {
    if (text === value) return
    const timer = setTimeout(() => onChange(text), 300)
    return () => clearTimeout(timer)
  }, [text, value, onChange])
  return <input className={css.search} type="search" value={text} onChange={(e) => setText(e.target.value)} placeholder="搜索标题、用户或编号" aria-label="搜索工单" />
}

/** 「检查 SLA 超时」：租户级批量扫描，不是单张升级（契约）；后台每 5 分钟也会自动跑 */
function EscalateButton() {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateTickets()
  const intent = useIntentKey()
  const run = useMutation({
    mutationFn: () => api.post('v1/tickets/escalate', escalatedSchema, { idempotencyKey: intent.keyFor('escalate') }),
    onSuccess: (r) => {
      intent.reset()
      void invalidate()
      toast(r.escalated > 0 ? `已把 ${r.escalated} 张超时工单升级` : '没有新的超时工单')
    },
    onError: (e) => fail(e, { intent }),
  })
  return (
    <Button size="sm" busy={run.isPending} onClick={() => run.mutate()} title="立即扫描首次响应已超时的工单并升级（后台每 5 分钟也会自动扫一次）">
      检查 SLA 超时
    </Button>
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
