import { useState } from 'react'
import { formatMoney } from '../../../core/format'
import { href, navigate, useHashLocation } from '../../../core/router'
import { Empty, QueryView, Segmented, Skeleton, Tag } from '../../../ui'
import { Btn, cx, GroupHeader, Icon, IconDisc, type IconName, Num, Rows } from '../../kit'
import {
  groupByMonth,
  orderTitle,
  shortDateTime,
  statusBadge,
  useOrder,
  useOrderPages,
  type OrderFilter,
  type OrderRow,
} from '../common/orders'
import { PaymentModal, type PayState } from '../common/PayFlow'
import type { PortalScreenProps } from '../index'
import { PendingOrders } from './Pending'
import { Receipt } from './Receipt'
import css from './Orders.module.css'

export default function Orders({ rest }: PortalScreenProps) {
  const { query } = useHashLocation()
  const selected = rest[0]
  const returning = selected !== undefined && query.get('paid') === '1'
  const [pay, setPay] = useState<PayState | null>(null)
  const [filter, setFilter] = useState<OrderFilter>('all')

  const state: PayState | null = returning ? { phase: 'confirm', orderId: selected } : pay
  const closePay = () => {
    setPay(null)
    if (returning) navigate(`/orders/${encodeURIComponent(selected)}`, { replace: true })
  }

  return (
    <div className={css.page}>
      <PendingOrders onPay={setPay} />
      <OrderList filter={filter} onFilter={setFilter} selected={selected} />
      <PaymentModal state={state} onClose={closePay} />
    </div>
  )
}

const FILTER_LABELS: Record<OrderFilter, string> = { all: '全部', paid: '已支付', cancelled: '已取消' }
const FILTER_EMPTY: Record<OrderFilter, string> = {
  all: '没有已结束的订单',
  paid: '没有已支付的订单',
  cancelled: '没有已取消的订单',
}

const KIND_ICON: Record<OrderRow['kind'], IconName> = {
  new: 'plans',
  renewal: 'renew',
  topup: 'in',
  upgrade: 'swap',
  stack: 'plus',
  traffic_reset: 'reset',
}

function OrderList({
  filter,
  onFilter,
  selected,
}: {
  filter: OrderFilter
  onFilter: (f: OrderFilter) => void
  selected: string | undefined
}) {
  const pages = useOrderPages(filter)
  const rows = pages.data?.pages.flatMap((p) => p.orders) ?? []
  const counts = pages.data?.pages[0]?.counts
  const total = pages.data?.pages[0]?.total ?? 0
  const selectedLoaded = selected !== undefined && rows.some((r) => r.id === selected)

  const count = (f: OrderFilter) => (!counts ? null : f === 'all' ? counts.paid + counts.closed : f === 'paid' ? counts.paid : counts.closed)
  const filters = Object.keys(FILTER_LABELS) as OrderFilter[]
  const neverOrdered = filter === 'all' && pages.isSuccess && total === 0 && (!counts || counts.open === 0)

  return (
    <>
      {neverOrdered ? (
        <div className={css.empty}>
          <span className={css.emptyIcon}>
            <Icon name="orders" size={24} />
          </span>
          <b>还没有订单</b>
          <p>买套餐或充值余额后，记录都会出现在这里。</p>
          <Btn href={href('/plans')}>去选购套餐</Btn>
        </div>
      ) : (
        <>
          <div className={css.tools}>
            <Segmented
              className={css.seg}
              label="订单状态"
              size="sm"
              value={filter}
              onChange={onFilter}
              options={filters.map((f) => {
                const n = count(f)
                return {
                  value: f,
                  label:
                    n === null ? (
                      FILTER_LABELS[f]
                    ) : (
                      <>
                        {FILTER_LABELS[f]}
                        <em className={css.segCount}>{n}</em>
                      </>
                    ),
                }
              })}
            />
            <span className={css.toolsHint}>点一笔看明细</span>
          </div>
          {selected !== undefined && pages.isSuccess && !selectedLoaded && <StandaloneDetail id={selected} />}
          <QueryView
            query={pages}
            rows={4}
            isEmpty={() => rows.length === 0}
            empty={<div className={css.filterEmpty}>{FILTER_EMPTY[filter]}</div>}
          >
            {() => (
              <>
                {groupByMonth(rows).map((g) => (
                  <div key={g.key}>
                    <GroupHeader aside={<span className={css.sum}>{g.paid > 0 ? `已支付 ${formatMoney(g.paid)}` : '无支付'}</span>}>
                      {g.label}
                    </GroupHeader>
                    <Rows>
                      {g.rows.map((o) => (
                        <OrderLine key={o.id} order={o} open={o.id === selected} />
                      ))}
                    </Rows>
                  </div>
                ))}
                {pages.hasNextPage && (
                  <button type="button" className={css.more} disabled={pages.isFetchingNextPage} onClick={() => void pages.fetchNextPage()}>
                    {pages.isFetchingNextPage ? '正在加载…' : `显示更早的订单（还有 ${total - rows.length} 笔）`}
                  </button>
                )}
              </>
            )}
          </QueryView>
        </>
      )}
    </>
  )
}

function OrderLine({ order, open }: { order: OrderRow; open: boolean }) {
  const badge = statusBadge(order.status)
  const muted = badge.tone === 'neutral'
  return (
    <div className={cx(css.od, muted && css.off)}>
      <button
        type="button"
        className={css.odRow}
        aria-expanded={open}
        onClick={() => navigate(open ? '/orders' : `/orders/${encodeURIComponent(order.id)}`, { replace: true })}
      >
        <IconDisc name={KIND_ICON[order.kind]} tone={muted ? 'muted' : 'brand'} className={css.odIco} />
        <span className={css.rt}>
          <b>{orderTitle(order)}</b>
          <small>
            <Num className={css.mono}>{order.order_no}</Num> · {shortDateTime(order.created_at)}
          </small>
        </span>
        <span className={css.oa}>
          <Num>{formatMoney(order.total_amount)}</Num>
          <Tag tone={badge.tone}>{badge.label}</Tag>
        </span>
        <Icon name="chev" size={18} className={css.chev} />
      </button>
      {open && <Receipt id={order.id} />}
    </div>
  )
}

function StandaloneDetail({ id }: { id: string }) {
  const detail = useOrder(id)
  if (detail.isError) {
    return <Empty bare title="无权限或不存在" description="这个订单不存在，或不属于当前账号。" />
  }
  return detail.data ? (
    <Rows className={css.standalone}>
      <OrderLine order={detail.data} open />
    </Rows>
  ) : (
    <Skeleton height={64} />
  )
}
