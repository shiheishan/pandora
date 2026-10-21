// 用户详情「订单与余额」页签里的余额流水（只读）：与用户端钱包是同一批分录，多了变动后余额与关联订单
import { useState, type ReactNode } from 'react'
import { formatDateTime, formatMoney } from '../../../core/format'
import { href } from '../../../core/router'
import { Empty, Pager, QueryView, Table } from '../../../ui'
import { SectionTitle } from '../../kit'
import { BALANCE_ENTRIES_PAGE, useBalanceEntries, type UserDetail } from './api'
import { balanceEntryNote } from './ledger'
import { balanceDeltaView, balanceKindLabel } from './model'
import css from './Subs.module.css'

export function BalanceEntries({ d }: { d: UserDetail }) {
  const [offset, setOffset] = useState(0)
  const q = useBalanceEntries(d.id, offset)

  return (
    <section>
      <SectionTitle hint="和用户端钱包里的余额记录是同一批">余额流水</SectionTitle>
      <QueryView
        query={q}
        isEmpty={(p) => p.total === 0}
        rows={3}
        empty={
          <Empty
            bare
            title="还没有余额变动"
            description="用户充值、用余额下单、兑换余额卡，或在这里调整余额后，会逐笔列在这里。"
          />
        }
      >
        {(page) => (
          <Table
            boxed
            label="余额流水"
            rowKey={(e) => e.id}
            rows={page.entries}
            columns={[
              {
                key: 'time',
                header: '时间',
                width: '136px',
                render: (e) => <span className={css.dim}>{formatDateTime(e.at)}</span>,
              },
              {
                key: 'kind',
                header: '类型',
                render: (e) => balanceKindLabel(e.kind, e.delta),
              },
              {
                key: 'delta',
                header: '金额',
                width: '100px',
                align: 'right',
                mono: true,
                render: (e) => {
                  const v = balanceDeltaView(e.delta)
                  return <span className={v.out ? undefined : css.deltaIn}>{v.text}</span>
                },
              },
              {
                key: 'after',
                header: '变动后余额',
                width: '110px',
                align: 'right',
                mono: true,
                hideOnMobile: true,
                render: (e) => formatMoney(e.balance_after),
              },
              {
                key: 'note',
                header: '原因 / 订单',
                hideOnMobile: true,
                render: (e) => {
                  const order = e.order_id ? d.recent_orders.find((o) => o.id === e.order_id) : undefined
                  const orderLink = e.order_id
                    ? (
                        <a href={href(`/billing/orders/${encodeURIComponent(e.order_id)}`)}>
                          {order?.order_no ?? '查看订单'}
                        </a>
                      )
                    : null
                  const memo = balanceEntryNote(e.kind, e.memo) || null
                  if (!orderLink && !memo) return <span className={css.dim}>—</span>
                  const parts: ReactNode[] = []
                  if (orderLink) parts.push(orderLink)
                  if (memo) parts.push(memo)
                  return (
                    <span className={css.entryNote}>
                      {parts.map((part, i) => (
                        <span key={i}>
                          {i > 0 ? ' · ' : null}
                          {part}
                        </span>
                      ))}
                    </span>
                  )
                },
              },
            ]}
            footer={
              page.total > BALANCE_ENTRIES_PAGE ? (
                <Pager inline total={page.total} limit={BALANCE_ENTRIES_PAGE} offset={offset} onChange={setOffset} />
              ) : (
                <span>共 {page.total} 笔</span>
              )
            }
          />
        )}
      </QueryView>
    </section>
  )
}
