import { useMutation } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { Clock01Icon, MoreHorizontalIcon, Settings01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { href } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Empty, Menu, SearchField, Skeleton, Tabs, Tag, useToast } from '../../../ui'
import { useFailure, useIntentKey } from '../../actions'
import { pageHelp } from '../../page-help'
import { PageHelpTip } from '../../PageHelp'
import { escalatedSchema, useInvalidateTickets, useTicketQueue, type QueueParams, type Ticket } from './api'
import { CATEGORY_LABELS, FILTERS, PRIORITY_LABELS, shortNo, statusView, waitLabel, type QueueFilter } from './model'
import css from './Tickets.module.css'

export function Queue({
  filter,
  query,
  params,
  selected,
  onFilter,
  onSearch,
  onOpenSettings,
}: {
  filter: QueueFilter
  query: string
  params: QueueParams
  selected: string | null
  onFilter: (f: QueueFilter) => void
  onSearch: (q: string) => void
  onOpenSettings: () => void
}) {
  const q = useTicketQueue(params)
  const tickets = q.data?.pages.flatMap((p) => p.tickets) ?? []
  const total = q.data?.pages[0]?.total ?? 0
  const now = useNow()

  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateTickets()
  const intent = useIntentKey()
  const scan = useMutation({
    mutationFn: () => api.post('v1/tickets/escalate', escalatedSchema, { idempotencyKey: intent.keyFor('escalate') }),
    onSuccess: (r) => {
      intent.reset()
      void invalidate()
      toast(r.escalated > 0 ? `已把 ${r.escalated} 张超时工单升级` : '没有新的超时工单')
    },
    onError: (e) => fail(e, { intent }),
  })

  const help = pageHelp('tickets', null)

  return (
    <aside className={css.queue} aria-label="工单列表">
      <div className={css.head}>
        <h1>工单</h1>
        {!q.isPending && <span className={css.count}>{total}</span>}
        <span className={css.sp} />
        <Menu
          label="工单操作"
          triggerLabel="更多操作"
          trigger={<HugeiconsIcon icon={MoreHorizontalIcon} size={16} strokeWidth={1.6} aria-hidden="true" />}
          triggerClassName={css.moreBtn}
          align="end"
          entries={[
            {
              key: 'sla',
              label: '立即检查 SLA 超时',
              icon: <HugeiconsIcon icon={Clock01Icon} size={16} aria-hidden="true" />,
              onSelect: () => scan.mutate(),
              disabled: scan.isPending,
            },
            {
              key: 'settings',
              label: '工单设置',
              icon: <HugeiconsIcon icon={Settings01Icon} size={16} aria-hidden="true" />,
              onSelect: onOpenSettings,
            },
          ]}
        />
        {help && (
          <span className={css.help}>
            <PageHelpTip help={help} />
          </span>
        )}
      </div>

      <div className={css.search}>
        <SearchBox value={query} onChange={onSearch} />
      </div>

      <Tabs
        label="工单筛选"
        className={css.filters}
        items={FILTERS.map(([value, label]) => ({ value, label }))}
        value={filter}
        onChange={onFilter}
      />

      <div className={css.list} aria-busy={q.isFetching || undefined}>
        {q.isPending ? (
          <div className={css.skeleton} role="status" aria-label="加载中">
            {Array.from({ length: 6 }, (_, i) => (
              <Skeleton key={i} height={68} />
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
          <Empty
            bare
            title={params.q ? '没有匹配的工单' : '此分类下没有工单'}
            description={params.q ? '换个关键词，或切到「全部」再搜。' : '用户提交工单后会出现在这里。'}
          />
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
    </aside>
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
  return (
    <SearchField
      value={text}
      onChange={(e) => setText(e.target.value)}
      placeholder="标题、用户或编号"
      aria-label="搜索工单"
      width="100%"
    />
  )
}

function Row({ t, on, now, link }: { t: Ticket; on: boolean; now: Date; link: { f?: string; q: string } }) {
  const st = statusView(t.status)
  const wait = waitLabel(t, now)
  // R60：最后一条人发的公开消息（不算内部备注与系统消息）是用户发的 = 等客服看，加粗（代替已读表）；已解决、已关闭的不算
  const unread = t.last_message_author_kind === 'user' && t.status !== 'resolved' && t.status !== 'closed'
  const rowClasses = [css.row, on && css.rowOn, unread && css.rowUnread].filter(Boolean).join(' ')

  return (
    <a className={rowClasses} href={href(`/tickets/${encodeURIComponent(t.id)}`, link)} aria-current={on ? 'true' : undefined}>
      <div className={css.r1}>
        <span
          className={`${css.pd} ${css[`pd_${t.priority}`]}`}
          title={`优先级：${PRIORITY_LABELS[t.priority]}`}
          aria-label={`优先级${PRIORITY_LABELS[t.priority]}`}
          role="img"
        />
        <b title={t.subject}>{t.subject}</b>
        <span className={css.tm}>{wait.text}</span>
      </div>
      <div className={css.r2}>
        {t.user_email ?? '—'} · {CATEGORY_LABELS[t.category]}
        {t.assignee_email ? ` · ${t.assignee_email}` : ''}
      </div>
      <div className={css.r3}>
        <Tag tone={st.tone}>{st.label}</Tag>
        {t.escalated_at && <Tag tone="warn">已升级</Tag>}
        <span className={css.mono} title={t.ticket_no}>
          {shortNo(t.ticket_no)}
        </span>
        <span className={css.sp} />
        {wait.late && <span className={css.late}>SLA 超时</span>}
      </div>
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

