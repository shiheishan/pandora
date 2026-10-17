import { href, useHashLocation } from '../../../core/router'
import { QueryView, Skeleton, Tag } from '../../../ui'
import { Btn, Icon, cx } from '../../kit'
import type { PortalScreenProps } from '../index'
import { useCategories, useTickets, type TicketRow } from './api'
import { listTime, ticketRoute, ticketStatus } from './model'
import { NewTicket } from './NewTicket'
import { TicketView } from './Thread'
import css from './Tickets.module.css'

export default function Tickets({ rest }: PortalScreenProps) {
  const { query } = useHashLocation()
  const route = ticketRoute(rest, query)
  const tickets = useTickets()
  const names = useCategoryNames()
  const first = tickets.data?.[0]
  const selected = route.kind === 'ticket' ? route.id : route.kind === 'list' ? first?.id : undefined

  return (
    <div className={css.tk} data-view={route.kind === 'list' ? 'list' : 'detail'}>
      <div className={css.side}>
        <Btn full href={href('/tickets/new')}>
          ＋ 提交新工单
        </Btn>
        <div className={css.list}>
          <QueryView
            query={tickets}
            rows={3}
            empty={
              <div className={css.empty}>
                <b>还没有工单</b>遇到连接、账单或账号问题，提交工单后客服会在这里回复你。
              </div>
            }
          >
            {(rows) => (
              <nav aria-label="我的工单">
                {rows.map((t) => (
                  <TicketItem key={t.id} ticket={t} category={names(t.category)} current={route.kind !== 'new' && t.id === selected} />
                ))}
              </nav>
            )}
          </QueryView>
        </div>
        <div className={css.help}>
          <Icon name="help" size={18} />
          连不上、App 怎么用，帮助中心多半有答案
          <a href={href('/help')}>去看看 ›</a>
        </div>
      </div>
      <div className={css.main}>
        {route.kind === 'new' ? (
          <NewTicket orderId={route.orderId} />
        ) : selected ? (
          <TicketView key={selected} id={selected} category={names} />
        ) : tickets.isSuccess ? (
          // 宽屏且一张工单都没有：右侧直接给新建表单
          <NewTicket orderId={null} />
        ) : (
          <Skeleton height={320} radius="var(--radius-lg)" />
        )}
      </div>
    </div>
  )
}

/** 分类 code → 名称；分类接口没回来时先显示 code */
function useCategoryNames(): (code: string) => string {
  const categories = useCategories()
  return (code) => categories.data?.find((c) => c.code === code)?.name ?? code
}

function TicketItem({ ticket, category, current }: { ticket: TicketRow; category: string; current: boolean }) {
  const status = ticketStatus(ticket)
  return (
    <a
      href={href(`/tickets/${ticket.id}`)}
      className={cx(css.item, current && css.itemOn, ticket.status === 'closed' && css.off)}
      aria-current={current || undefined}
    >
      <span className={css.meta}>
        <Tag className={css.tagSm} tone={status.tone}>
          {status.label}
        </Tag>
        {category}
        <time className={css.time} dateTime={ticket.last_reply_at}>
          {listTime(ticket.last_reply_at)}
        </time>
      </span>
      <b className={css.title}>{ticket.subject}</b>
      <small className={css.sub}>
        <span className={css.mono}>#{ticket.ticket_no}</span> · {ticket.message_count} 条消息
      </small>
    </a>
  )
}
