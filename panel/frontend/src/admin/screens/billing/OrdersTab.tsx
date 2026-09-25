/**
 * [INPUT]: 依赖 react 的 useCallback / useEffect / useState，依赖 ../../../core/format 的 formatMoney / relativeTime，依赖 ../../../core/router 的 href / navigate / useHashLocation / RouteQuery，依赖 ../../../ui 的 Button / Empty / IconClose / Pager / QueryView / Segmented / Table / Tag，依赖 ../../actions 的 useCan，依赖 ./api 的 useOrders / ORDERS_PAGE / OrderRow，依赖 ./model，依赖 ./OrderDrawer，依赖 ./ManualOrder，依赖 ./Billing.module.css
 * [OUTPUT]: 对外提供 OrdersTab
 * [POS]: 订单与收款「订单」标签（后台-05）：搜索（订单号 / 邮箱，防抖）、状态分段（R63 多值）、按用户筛选的标签（从用户抽屉 ?user_id= 进来）、「人工开单」、七列表格（行点击或 Enter 打开抽屉）与分页；打开的订单在地址第三段 #/billing/orders/<id>，人工开单弹窗是 ?new=1 或 ?new=<用户 id>（用户抽屉「为其开单」），刷新与分享都落在同一处
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useCallback, useEffect, useState } from 'react'
import { formatMoney, relativeTime } from '../../../core/format'
import { href, navigate, useHashLocation, type RouteQuery } from '../../../core/router'
import { Button, Empty, IconClose, Pager, QueryView, Segmented, Table, Tag, type TableColumn } from '../../../ui'
import { useCan } from '../../actions'
import { ORDERS_PAGE, useOrders, type OrderRow } from './api'
import css from './Billing.module.css'
import { ManualOrder } from './ManualOrder'
import { channelLabel, filterStatuses, isOrderFilter, ORDER_FILTERS, ORDER_STATUS_VIEW, orderWhat, type OrderFilter } from './model'
import { OrderDrawer } from './OrderDrawer'

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

interface ListState {
  filter: OrderFilter
  q: string
  offset: number
  userId: string
}

/** 查询串：默认值不写进地址 */
function query(st: ListState, extra: RouteQuery = {}): RouteQuery {
  return { s: st.filter === 'all' ? undefined : st.filter, q: st.q || undefined, o: st.offset || undefined, user_id: st.userId || undefined, ...extra }
}
const path = (id: string | null) => (id ? `/billing/orders/${encodeURIComponent(id)}` : '/billing/orders')

export function OrdersTab({ rest, now }: { rest: string[]; now: Date }) {
  const location = useHashLocation()
  const can = useCan()
  const s = location.query.get('s')
  const userParam = (location.query.get('user_id') ?? '').trim()
  const state: ListState = {
    filter: isOrderFilter(s) ? s : 'all',
    q: location.query.get('q') ?? '',
    offset: Math.max(0, Number.parseInt(location.query.get('o') ?? '', 10) || 0),
    // 非法 uuid 后端回 400：不带上去，筛选标签也不画
    userId: UUID.test(userParam) ? userParam : '',
  }
  const newParam = location.query.get('new')
  const manualFor = newParam && UUID.test(newParam) ? newParam : null
  const manualOpen = newParam !== null && can('billing.order.write')
  const selected = rest[0] ?? null

  const change = useCallback(
    (next: Partial<ListState>) => navigate(path(selected), { replace: true, query: query({ ...state, ...next }) }),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- state 由地址派生，按其原始值比较
    [selected, state.filter, state.q, state.offset, state.userId],
  )

  const orders = useOrders({ q: state.q.trim() || undefined, status: filterStatuses(state.filter), user_id: state.userId || undefined, offset: state.offset })
  const total = orders.data?.total ?? 0
  const filtered = state.q !== '' || state.filter !== 'all' || state.userId !== ''
  // 标签上的邮箱取当前结果的第一行；翻到空页或还没回来时退回 uuid 前 8 位
  const userLabel = orders.data?.orders[0]?.user_email ?? `用户 #${state.userId.slice(0, 8)}`

  return (
    <div className={css.page}>
      <div className={css.toolbar}>
        <SearchBox value={state.q} onChange={(q) => change({ q, offset: 0 })} />
        <Segmented
          size="sm"
          label="订单状态"
          options={ORDER_FILTERS.map(([value, label]) => ({ value, label }))}
          value={state.filter}
          onChange={(filter) => change({ filter, offset: 0 })}
        />
        {state.userId && (
          <span className={css.chip}>
            只看 {userLabel}
            <button type="button" className={css.chipClose} aria-label="取消按用户筛选" onClick={() => change({ userId: '', offset: 0 })}>
              <IconClose />
            </button>
          </span>
        )}
        <span className={css.spacer} />
        {orders.data && <span className={css.small}>共 {total.toLocaleString('en-US')} 单</span>}
        {can('billing.order.write') && (
          <Button size="sm" variant="primary" onClick={() => navigate(path(selected), { query: query(state, { new: state.userId || '1' }) })}>
            人工开单
          </Button>
        )}
      </div>
      <div className={`${css.tableBox} ${css.wide}`}>
        <QueryView
          query={orders}
          rows={8}
          isEmpty={(d) => d.orders.length === 0}
          empty={
            <Empty
              bare
              title={filtered ? '没有符合条件的订单' : '还没有订单'}
              description={filtered ? '换个关键词或状态再看看。' : '用户在门户下单，或管理员人工开单后，订单会出现在这里。'}
            />
          }
        >
          {(d) => (
            <Table
              label="订单列表"
              columns={columns(now)}
              rows={d.orders}
              rowKey={(o) => o.id}
              onRowClick={(o) => window.location.assign(href(path(o.id), query(state)))}
            />
          )}
        </QueryView>
      </div>
      <Pager total={total} limit={ORDERS_PAGE} offset={state.offset} onChange={(offset) => change({ offset })} />
      <OrderDrawer id={selected} onClose={() => navigate(path(null), { query: query(state) })} now={now} />
      <ManualOrder
        open={manualOpen}
        userId={manualFor}
        onClose={(createdId) => navigate(path(createdId ?? selected), { replace: createdId === undefined, query: query(state) })}
      />
    </div>
  )
}

function columns(now: Date): TableColumn<OrderRow>[] {
  return [
    { key: 'no', header: '订单号', width: '160px', mono: true, render: (o) => o.order_no },
    { key: 'user', header: '用户', render: (o) => <span className={css.ellipsis}>{o.user_email}</span> },
    { key: 'what', header: '内容', render: (o) => <span className={`${css.muted} ${css.ellipsis}`}>{orderWhat(o)}</span> },
    { key: 'amount', header: '金额', width: '100px', align: 'right', mono: true, render: (o) => formatMoney(o.total_amount, o.currency) },
    { key: 'channel', header: '渠道', width: '104px', render: (o) => <span className={css.muted}>{channelLabel(o)}</span> },
    {
      key: 'status',
      header: '状态',
      width: '84px',
      render: (o) => {
        const v = ORDER_STATUS_VIEW[o.status]
        return <Tag tone={v.tone}>{v.label}</Tag>
      },
    },
    { key: 'at', header: '创建时间', width: '96px', align: 'right', render: (o) => <span className={css.small}>{relativeTime(o.created_at, now)}</span> },
  ]
}

/** 搜索框：输入 300ms 后才回写地址；地址被外部改了（前进后退）跟着地址走 */
function SearchBox({ value, onChange }: { value: string; onChange: (q: string) => void }) {
  const [text, setText] = useState(value)
  const [synced, setSynced] = useState(value)
  if (value !== synced) {
    setSynced(value)
    setText(value)
  }
  useEffect(() => {
    if (text === value) return
    const timer = setTimeout(() => onChange(text), 300)
    return () => clearTimeout(timer)
  }, [text, value, onChange])
  return <input className={css.search} type="search" value={text} onChange={(e) => setText(e.target.value)} placeholder="订单号或用户邮箱" aria-label="搜索订单" />
}
