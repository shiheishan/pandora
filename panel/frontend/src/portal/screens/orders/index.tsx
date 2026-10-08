import { useState } from 'react'
import { formatDateTime, formatMoney } from '../../../core/format'
import { href, navigate, useHashLocation } from '../../../core/router'
import { Button, ConfirmModal, Empty, QueryView, Segmented, Skeleton, Tag, useToast } from '../../../ui'
import { LoadError } from '../common/Blocks'
import {
  expiryNote,
  groupByMonth,
  orderFacts,
  orderTitle,
  statusBadge,
  useCancelOrder,
  useOpenOrders,
  useOrder,
  useOrderPages,
  type OrderFilter,
  type OrderRow,
} from '../common/orders'
import { queryFailure, queryOutcome, useQueryOrderPayment, type QueryOutcome } from '../common/order-query'
import { PaymentModal, type PayState } from '../common/PayFlow'
import type { PortalScreenProps } from '../index'
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
      <OpenOrders onPay={setPay} />
      <OrderList filter={filter} onFilter={setFilter} selected={selected} />
      <PaymentModal state={state} onClose={closePay} />
    </div>
  )
}

// ---------------------------------------------------------------------------
// 待支付卡片：订单 30 分钟过期；processing 是支付已发起、等回调，不能取消也不能再付
// ---------------------------------------------------------------------------
function OpenOrders({ onPay }: { onPay: (s: PayState) => void }) {
  const open = useOpenOrders()
  const cancel = useCancelOrder()
  const toast = useToast()
  const [confirming, setConfirming] = useState<OrderRow | null>(null)
  const [outcomes, setOutcomes] = useState<Record<string, QueryOutcome>>({})

  if (open.isPending) return null
  if (open.isError) return <LoadError error={open.error} onRetry={() => void open.refetch()} what="待支付订单" />

  async function doCancel(o: OrderRow) {
    try {
      const r = await cancel.mutateAsync(o.id)
      toast(r.already_terminal ? '订单已经取消了' : '订单已取消')
    } catch (e) {
      toast(e instanceof Error ? e.message : '取消失败', 'danger')
    } finally {
      setConfirming(null)
    }
  }

  return (
    <>
      {open.data.map((o) => {
        const processing = o.status === 'processing'
        return (
          <section key={o.id} className={css.pending}>
            <div className={css.pendingMain}>
              <div className={css.pendingHead}>
                <Tag tone="warn">{processing ? '处理中' : '待支付'}</Tag>
                <span className={css.pendingWhat}>{orderTitle(o)}</span>
              </div>
              <div className={css.pendingMeta}>
                <span className={css.mono}>{o.order_no}</span> · {formatDateTime(o.created_at)} 下单 · {processing ? '支付结果确认中' : expiryNote(o.expires_at)}
              </div>
            </div>
            <div className={css.pendingAmount}>{formatMoney(o.payable_amount, o.currency)}</div>
            <div className={css.pendingActions}>
              {!processing && o.cancellable && (
                <button type="button" className={css.quiet} onClick={() => setConfirming(o)}>
                  取消订单
                </button>
              )}
              {o.has_payment_intent && <PaymentRefresh orderId={o.id} onOutcome={(r) => setOutcomes((m) => ({ ...m, [o.id]: r }))} />}
              {!processing && (
                <Button variant="primary" size="sm" onClick={() => onPay({ phase: 'choose', orderId: o.id, orderNo: o.order_no, amount: o.payable_amount, currency: o.currency })}>
                  去支付
                </Button>
              )}
            </div>
            <RefreshNote outcome={outcomes[o.id]} />
          </section>
        )
      })}
      <ConfirmModal
        open={confirming !== null}
        title="取消这笔订单？"
        body={confirming ? `${orderTitle(confirming)} · ${formatMoney(confirming.payable_amount, confirming.currency)}。取消后优惠码和已冻结的余额会立即退回。` : ''}
        confirmLabel="取消订单"
        cancelLabel="再想想"
        tone="danger"
        onConfirm={() => (confirming ? doCancel(confirming) : undefined)}
        onCancel={() => setConfirming(null)}
      />
    </>
  )
}

// ---------------------------------------------------------------------------
// 「我已支付，刷新状态」：付完钱订单还挂着（回调丢了或还没到）时，让后端去渠道查一次。
// 结果分三种显示在按钮旁的结果条里：已到账、渠道尚未确认、查询失败。
// ---------------------------------------------------------------------------
function PaymentRefresh({ orderId, onOutcome }: { orderId: string; onOutcome: (o: QueryOutcome) => void }) {
  const query = useQueryOrderPayment()
  const toast = useToast()
  const run = async () => {
    try {
      const outcome = queryOutcome(await query.mutateAsync(orderId))
      onOutcome(outcome)
      // 到账后订单离开待支付，卡片与按钮随之消失，结果条可能来不及被看到：再弹一条
      if (outcome.kind === 'settled') toast(outcome.text)
    } catch (e) {
      onOutcome(queryFailure(e))
    }
  }
  return (
    <button type="button" className={css.quiet} disabled={query.isPending} onClick={() => void run()}>
      {query.isPending ? '正在向支付渠道查询…' : '我已支付，刷新状态'}
    </button>
  )
}

function RefreshNote({ outcome: o }: { outcome: QueryOutcome | undefined }) {
  if (!o) return null
  return (
    <div className={css.queryNote} data-kind={o.kind} role="status">
      {o.text}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 已结束的订单：筛选 + 按月分组 + 显示更早
// ---------------------------------------------------------------------------
const FILTER_LABELS: Record<OrderFilter, string> = { all: '全部', paid: '已支付', cancelled: '已取消', refunded: '已退款' }
const FILTER_EMPTY: Record<OrderFilter, string> = { all: '没有已结束的订单', paid: '没有已支付的订单', cancelled: '没有已取消的订单', refunded: '没有已退款的订单' }

function OrderList({ filter, onFilter, selected }: { filter: OrderFilter; onFilter: (f: OrderFilter) => void; selected: string | undefined }) {
  const pages = useOrderPages(filter)
  const rows = pages.data?.pages.flatMap((p) => p.orders) ?? []
  const counts = pages.data?.pages[0]?.counts
  const total = pages.data?.pages[0]?.total ?? 0
  const selectedLoaded = selected !== undefined && rows.some((r) => r.id === selected)

  const count = (f: OrderFilter) => (!counts ? null : f === 'all' ? counts.paid + counts.closed + counts.refunded : f === 'paid' ? counts.paid : f === 'cancelled' ? counts.closed : counts.refunded)
  const filters = (Object.keys(FILTER_LABELS) as OrderFilter[]).filter((f) => f !== 'refunded' || (counts?.refunded ?? 0) > 0 || filter === 'refunded')
  const neverOrdered = filter === 'all' && pages.isSuccess && total === 0 && (!counts || counts.open === 0)

  return (
    <>
      {!neverOrdered && (
        <div className={css.toolbar}>
          <Segmented
            label="订单状态"
            size="sm"
            value={filter}
            onChange={onFilter}
            options={filters.map((f) => {
              const n = count(f)
              return { value: f, label: n === null ? FILTER_LABELS[f] : `${FILTER_LABELS[f]} ${n}` }
            })}
          />
          <span className={css.toolbarHint}>点击订单查看详情</span>
        </div>
      )}
      {selected !== undefined && pages.isSuccess && !selectedLoaded && <StandaloneDetail id={selected} />}
      <section className={css.list}>
        <QueryView
          query={pages}
          rows={4}
          isEmpty={() => rows.length === 0}
          empty={
            neverOrdered ? (
              <Empty
                bare
                title="还没有订单"
                description="买套餐、流量包或充值余额后，记录都会出现在这里。"
                action={
                  <a className={css.linkButton} href={href('/plans')}>
                    去选购套餐
                  </a>
                }
              />
            ) : (
              <div className={css.filterEmpty}>{FILTER_EMPTY[filter]}</div>
            )
          }
        >
          {() => (
            <>
              {groupByMonth(rows).map((g) => (
                <div key={g.key}>
                  <div className={css.groupHead}>
                    <span className={css.groupLabel}>{g.label}</span>
                    <span className={css.groupSum}>{g.paid > 0 ? `已支付 ${formatMoney(g.paid, g.rows[0]?.currency)}` : '无支付'}</span>
                  </div>
                  {g.rows.map((o) => (
                    <OrderLine key={o.id} order={o} open={o.id === selected} />
                  ))}
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
      </section>
    </>
  )
}

function OrderLine({ order, open }: { order: OrderRow; open: boolean }) {
  const badge = statusBadge(order.status)
  const muted = badge.tone === 'neutral'
  return (
    <div className={css.row}>
      <button
        type="button"
        className={css.rowButton}
        aria-expanded={open}
        data-open={open ? '' : undefined}
        onClick={() => navigate(open ? '/orders' : `/orders/${encodeURIComponent(order.id)}`, { replace: true })}
      >
        <span className={css.rowMain}>
          <span className={muted ? css.rowTitleMuted : css.rowTitle}>{orderTitle(order)}</span>
          <span className={css.rowMeta}>
            <span className={css.mono}>{order.order_no}</span> · {formatDateTime(order.created_at)}
          </span>
        </span>
        <span className={muted ? css.rowAmountMuted : css.rowAmount}>{formatMoney(order.total_amount, order.currency)}</span>
        <span className={css.rowBadge}>
          <Tag tone={badge.tone}>{badge.label}</Tag>
        </span>
      </button>
      {open && <Facts id={order.id} />}
    </div>
  )
}

function Facts({ id }: { id: string }) {
  const detail = useOrder(id)
  const [outcome, setOutcome] = useState<QueryOutcome>()
  return (
    <div className={css.facts}>
      {detail.isPending ? (
        <Skeleton height={60} />
      ) : detail.isError ? (
        <LoadError error={detail.error} onRetry={() => void detail.refetch()} what="订单明细" />
      ) : (
        <>
          <dl className={css.factList}>
            {orderFacts(detail.data).map((f, i) => (
              <div key={i} className={css.fact}>
                <dt>{f.k}</dt>
                <dd>{f.v}</dd>
              </div>
            ))}
          </dl>
          {((detail.data.has_payment_intent && (detail.data.status === 'pending_payment' || detail.data.status === 'processing')) || outcome) && (
            <div className={css.factActions}>
              {detail.data.has_payment_intent && (detail.data.status === 'pending_payment' || detail.data.status === 'processing') && <PaymentRefresh orderId={id} onOutcome={setOutcome} />}
              <RefreshNote outcome={outcome} />
            </div>
          )}
          {/* 契约门户-07：订单页带 order_id 预填进新建工单 */}
          <a className={css.askLink} href={href('/tickets/new', { order: id })}>
            对这笔订单有疑问？提交工单 →
          </a>
        </>
      )}
    </div>
  )
}

/** #/orders/<id> 指向的订单不在当前已加载的列表里（更早、或筛选不含它）：单独成卡 */
function StandaloneDetail({ id }: { id: string }) {
  const detail = useOrder(id)
  if (detail.isError) {
    return (
      <section className={css.list}>
        <Empty bare title="无权限或不存在" description="这个订单不存在，或不属于当前账号。" />
      </section>
    )
  }
  return (
    <section className={css.list}>
      {detail.data ? (
        <OrderLine order={detail.data} open />
      ) : (
        <div className={css.facts}>
          <Skeleton height={48} />
        </div>
      )}
    </section>
  )
}
