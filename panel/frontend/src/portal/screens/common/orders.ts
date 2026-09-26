/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useQuery / useInfiniteQuery / useMutation / useQueryClient，依赖 zod，依赖 ../../../core/format 的 formatMoney / formatDateTime，依赖 ../../../shell/runtime 的 useApi，依赖 ./intent 的 endsIntent
 * [OUTPUT]: 对外提供 ORDER_KINDS、ORDER_STATUSES、OPEN_STATUSES、ORDER_FILTERS / OrderFilter、ORDER_PAGE_SIZE、useOrderPages、useOpenOrders、useCancelOrder、statusBadge、groupByMonth、orderResult、orderFacts、orderRowSchema、OrderRow、intervalLabel、orderTitle、expiryNote、usePendingOrders、orderDetailSchema / OrderDetail / orderKey / useOrder、orderCreatedSchema / OrderCreated、PAID_STATUSES / SETTLED_STATUSES、PAYABLE_STATUSES / isPayable / useOrderPayable
 * [POS]: portal/screens/common 的订单读模型（契约门户-04 GET v1/orders）：概览待支付条、支付结果确认、订单页与重开刚下的单前的可支付判定共用；标题与期限文案是纯函数、有单元测试
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { z } from 'zod'
import { formatDateTime, formatMoney } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { endsIntent } from './intent'

export const ORDER_KINDS = ['new', 'renewal', 'topup', 'addon', 'upgrade'] as const
export const ORDER_STATUSES = ['draft', 'pending_payment', 'processing', 'paid', 'fulfilled', 'cancelled', 'expired', 'partially_refunded', 'refunded'] as const

export const orderRowSchema = z.object({
  id: z.string(),
  order_no: z.string(),
  kind: z.enum(ORDER_KINDS),
  status: z.enum(ORDER_STATUSES),
  currency: z.string(),
  total_amount: z.number().int(),
  discount_amount: z.number().int(),
  balance_applied: z.number().int(),
  payable_amount: z.number().int(),
  paid_amount: z.number().int(),
  refunded_amount: z.number().int(),
  plan_name: z.string().optional(),
  cancellable: z.boolean(),
  created_at: z.string(),
  paid_at: z.string().optional(),
  cancelled_at: z.string().optional(),
  cancel_reason: z.string().optional(),
  expires_at: z.string().optional(),
  // 修订 R69 已上线；写成可选，旧后端（假后端 legacy 场景）缺席时标题退回 plan_name
  interval: z.string().optional(),
  interval_count: z.number().int().optional(),
  item_name: z.string().optional(),
})
export type OrderRow = z.output<typeof orderRowSchema>

export const ordersPageSchema = z.object({
  orders: z.array(orderRowSchema),
  total: z.number().int(),
  // 修订 R69
  counts: z.object({ open: z.number().int(), paid: z.number().int(), closed: z.number().int(), refunded: z.number().int() }).optional(),
})

/** 价格周期 → 「月付 / 季付 / 年付」；billing_interval 取值见 00003 的 CHECK。 */
const NAMED_PERIODS: Readonly<Record<string, string>> = {
  'month:1': '月付',
  'month:3': '季付',
  'quarter:1': '季付',
  'month:6': '半年付',
  'month:12': '年付',
  'year:1': '年付',
}
const PERIOD_UNITS: Readonly<Record<string, string>> = { day: '天', week: '周', month: '个月', quarter: '个季度', year: '年' }

export function intervalLabel(interval: string | undefined, count = 1): string {
  if (!interval) return ''
  if (interval === 'one_time') return '一次性'
  const named = NAMED_PERIODS[`${interval}:${count}`]
  if (named) return named
  const unit = PERIOD_UNITS[interval]
  return unit ? `${count} ${unit}` : ''
}

/** 契约门户-04 的标题映射；待补字段缺席时退回 plan_name（修订 R32：流量包订单的 plan_name 即流量包名）。 */
export function orderTitle(o: Pick<OrderRow, 'kind' | 'plan_name' | 'item_name' | 'interval' | 'interval_count'>): string {
  const plan = o.plan_name ?? ''
  const period = intervalLabel(o.interval, o.interval_count)
  switch (o.kind) {
    case 'topup':
      return '余额充值'
    case 'addon':
      return `流量包 · ${o.item_name ?? plan}`
    case 'upgrade':
      return `${plan} · 变更套餐`
    case 'renewal':
      return `${plan} · ${period ? `${period}续费` : '续费'}`
    default:
      return period ? `${plan} · ${period}` : plan
  }
}

/** 待支付条的期限：后端 30 分钟过期，按 expires_at 倒数。 */
export function expiryNote(expiresAt: string | undefined, now: Date = new Date()): string {
  if (!expiresAt) return '请尽快完成支付'
  const minutes = Math.ceil((new Date(expiresAt).getTime() - now.getTime()) / 60_000)
  return minutes > 0 ? `${minutes} 分钟内未支付将自动取消` : '即将自动取消'
}

/** 概览顶部待支付条：契约写的是 status=pending_payment。 */
export function usePendingOrders() {
  const api = useApi()
  return useQuery({
    queryKey: ['portal', 'orders', { status: 'pending_payment' }],
    queryFn: ({ signal }) => api.get('v1/orders', ordersPageSchema, { query: { status: 'pending_payment' }, signal }),
    select: (d) => d.orders,
    meta: { topics: ['orders.changed'] },
  })
}

// ---------------------------------------------------------------------------
// GET v1/orders/{id}（修订 R69 的明细字段）：支付回跳后的结果确认与第 ③ 步订单明细共用
// ---------------------------------------------------------------------------
export const orderDetailSchema = z.object({
  order: orderRowSchema.extend({
    items: z.array(z.object({ name: z.string(), quantity: z.number().int(), unit_amount: z.number().int(), line_amount: z.number().int() })),
    payments: z.array(
      z.object({
        status: z.string(),
        amount: z.number().int(),
        currency: z.string(),
        created_at: z.string(),
        method: z.string().optional(),
        provider_name: z.string().optional(),
      }),
    ),
    coupon_code: z.string().optional(),
    subscription_period_end: z.string().optional(),
  }),
})
export type OrderDetail = z.output<typeof orderDetailSchema>['order']

export const orderKey = (id: string) => ['portal', 'orders', 'detail', id] as const

/** 不会再变成已支付的终态 */
export const SETTLED_STATUSES: ReadonlySet<string> = new Set(['paid', 'fulfilled', 'cancelled', 'expired', 'partially_refunded', 'refunded'])

/** poll 给毫秒数时一直轮询到订单进入终态（支付回跳 3 秒一次）；false 不轮询 */
export function useOrder(id: string | undefined, poll: number | false = false) {
  const api = useApi()
  return useQuery({
    queryKey: orderKey(id ?? ''),
    queryFn: ({ signal }) => api.get(`v1/orders/${encodeURIComponent(id!)}`, orderDetailSchema, { signal }),
    select: (d) => d.order,
    enabled: id !== undefined && id !== '',
    refetchInterval: poll === false ? false : (query) => (SETTLED_STATUSES.has(query.state.data?.order.status ?? '') ? false : poll),
    meta: { topics: ['orders.changed'] },
  })
}

// ---------------------------------------------------------------------------
// 下单四个接口（新购、续费、变更套餐、流量包）同形的 201 响应
// ---------------------------------------------------------------------------
export const orderCreatedSchema = z.object({
  order_id: z.string(),
  order_no: z.string(),
  currency: z.string(),
  total_amount: z.number().int(),
  discount_amount: z.number().int(),
  balance_applied: z.number().int(),
  payable_amount: z.number().int(),
  status: z.enum(['pending_payment', 'fulfilled']),
  // 变更套餐另带（修订 R36）
  proration_credit: z.number().int().optional(),
  balance_refund: z.number().int().optional(),
})
export type OrderCreated = z.output<typeof orderCreatedSchema>

export const PAID_STATUSES: ReadonlySet<string> = new Set(['paid', 'fulfilled'])

/** 发起支付只收这两种状态，其余回 409「该订单当前状态不可支付」（契约门户-03 支付条目） */
export const PAYABLE_STATUSES: ReadonlySet<string> = new Set(['draft', 'pending_payment'])

/** 还能去付：状态可支付、且没到 expires_at（服务端过期扫描可能还没跑到） */
export function isPayable(o: Pick<OrderRow, 'status' | 'expires_at'>, now: number = Date.now()): boolean {
  return PAYABLE_STATUSES.has(o.status) && (o.expires_at === undefined || Date.parse(o.expires_at) > now)
}

/**
 * 重开刚下的单之前问一次后端它还能不能付（给 intent.ts 的 recallPayable 用）：取最新详情写进同一个缓存键；
 * 4xx（查不到）答「不能」，断网与 5xx 答「能」，让支付弹窗去报错与重试，不因一次查询失败多下一张单。
 */
export function useOrderPayable(): (orderId: string) => Promise<boolean> {
  const api = useApi()
  const client = useQueryClient()
  return async (orderId) => {
    try {
      const d = await client.fetchQuery({
        queryKey: orderKey(orderId),
        queryFn: ({ signal }) => api.get(`v1/orders/${encodeURIComponent(orderId)}`, orderDetailSchema, { signal }),
        staleTime: 0,
      })
      return isPayable(d.order)
    } catch (e) {
      return !endsIntent(e)
    }
  }
}

// ---------------------------------------------------------------------------
// 订单页（契约门户-04 与修订 R69：status 可逗号多值、counts 四类计数）
// 设计的「全部」= 除待支付外的所有状态；待支付在顶部单独成卡
// ---------------------------------------------------------------------------
export const OPEN_STATUSES = ['draft', 'pending_payment', 'processing'] as const
export const ORDER_FILTERS = {
  all: ['paid', 'fulfilled', 'cancelled', 'expired', 'partially_refunded', 'refunded'],
  paid: ['paid', 'fulfilled'],
  cancelled: ['cancelled', 'expired'],
  refunded: ['partially_refunded', 'refunded'],
} as const
export type OrderFilter = keyof typeof ORDER_FILTERS

export const ORDER_PAGE_SIZE = 6

/** 已结束订单的分段加载：「显示更早的订单」按 offset 往后取，每页 6 条 */
export function useOrderPages(filter: OrderFilter) {
  const api = useApi()
  return useInfiniteQuery({
    queryKey: ['portal', 'orders', 'list', filter],
    queryFn: ({ pageParam, signal }) =>
      api.get('v1/orders', ordersPageSchema, { query: { status: ORDER_FILTERS[filter].join(','), limit: ORDER_PAGE_SIZE, offset: pageParam }, signal }),
    initialPageParam: 0,
    getNextPageParam: (last, pages) => {
      const loaded = pages.reduce((n, p) => n + p.orders.length, 0)
      return loaded < last.total ? loaded : undefined
    },
    meta: { topics: ['orders.changed'] },
  })
}

/** 顶部待支付卡片：draft / pending_payment / processing */
export function useOpenOrders() {
  const api = useApi()
  return useQuery({
    queryKey: ['portal', 'orders', { status: OPEN_STATUSES.join(',') }],
    queryFn: ({ signal }) => api.get('v1/orders', ordersPageSchema, { query: { status: OPEN_STATUSES.join(','), limit: 20 }, signal }),
    select: (d) => d.orders,
    meta: { topics: ['orders.changed'] },
  })
}

const cancelSchema = z.object({
  order_id: z.string(),
  status: z.literal('cancelled'),
  state_version: z.number().int(),
  cancelled_at: z.string().optional(),
  cancel_reason: z.string().optional(),
  already_terminal: z.boolean(),
})

/** POST v1/orders/{id}/cancel：无 body、不幂等（重复取消回成功）；同事务退回优惠码与冻结的余额 */
export function useCancelOrder() {
  const api = useApi()
  const client = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.post(`v1/orders/${encodeURIComponent(id)}/cancel`, cancelSchema),
    onSuccess: () => void client.invalidateQueries({ queryKey: ['portal'] }),
  })
}

// ---------------------------------------------------------------------------
// 纯映射
// ---------------------------------------------------------------------------
export interface StatusBadge {
  label: string
  tone: 'ok' | 'warn' | 'neutral' | 'info'
}

export function statusBadge(status: OrderRow['status']): StatusBadge {
  switch (status) {
    case 'paid':
    case 'fulfilled':
      return { label: '已支付', tone: 'ok' }
    case 'processing':
      return { label: '处理中', tone: 'warn' }
    case 'draft':
    case 'pending_payment':
      return { label: '待支付', tone: 'warn' }
    case 'partially_refunded':
      return { label: '部分退款', tone: 'info' }
    case 'refunded':
      return { label: '已退款', tone: 'info' }
    default:
      return { label: '已取消', tone: 'neutral' }
  }
}

export interface MonthGroup<T> {
  key: string
  label: string
  rows: T[]
  /** 该月已加载行里已支付订单的 total_amount 合计（契约：只对已加载行求和） */
  paid: number
}

/** 按下单月份（浏览器本地时区）分组，保持列表原有顺序 */
export function groupByMonth<T extends Pick<OrderRow, 'created_at' | 'status' | 'total_amount'>>(rows: readonly T[]): MonthGroup<T>[] {
  const groups: MonthGroup<T>[] = []
  for (const row of rows) {
    const d = new Date(row.created_at)
    const key = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`
    let g = groups.find((x) => x.key === key)
    if (!g) {
      g = { key, label: `${d.getFullYear()} 年 ${d.getMonth() + 1} 月`, rows: [], paid: 0 }
      groups.push(g)
    }
    g.rows.push(row)
    if (PAID_STATUSES.has(row.status)) g.paid += row.total_amount
  }
  return groups
}

/** 展开区「结果」（契约门户-04 订单详情映射） */
export function orderResult(o: Pick<OrderDetail, 'status' | 'kind' | 'cancel_reason' | 'subscription_period_end' | 'payments' | 'expires_at' | 'refunded_amount' | 'currency'>): string {
  const via = o.payments.find((p) => p.provider_name || p.method)
  const method = via ? (via.provider_name ?? via.method ?? '') : ''
  switch (o.status) {
    case 'fulfilled':
    case 'paid':
      if (o.kind === 'topup') return '余额已到账'
      if (o.kind === 'addon') return '流量包已到账'
      if (o.kind === 'upgrade') return '已变更套餐，订阅地址不变'
      return [method, o.subscription_period_end ? `有效期至 ${formatDateTime(o.subscription_period_end).slice(0, 10)}` : '已开通'].filter(Boolean).join(' · ')
    case 'expired':
      return '超时未支付，已自动取消'
    case 'cancelled':
      return !o.cancel_reason || o.cancel_reason === 'user_cancelled' ? '已由您取消' : o.cancel_reason
    case 'partially_refunded':
    case 'refunded':
      return `已退款 ${formatMoney(o.refunded_amount, o.currency)}`
    case 'processing':
      return '支付处理中，稍后自动到账'
    default:
      return o.expires_at ? `待支付，${formatDateTime(o.expires_at)} 前有效` : '待支付'
  }
}

const PAYMENT_STATUS: Readonly<Record<string, string>> = { succeeded: '成功', success: '成功', paid: '成功', pending: '处理中', failed: '失败', refunded: '已退款', cancelled: '已取消' }

/** 展开区事实行（契约门户-04 待补·前端）：订单号、时间、结果、金额拆分、支付记录 */
export function orderFacts(o: OrderDetail): Array<{ k: string; v: string }> {
  const money = (m: number) => formatMoney(m, o.currency)
  const original = o.items.reduce((n, i) => n + i.line_amount, 0)
  const facts = [
    { k: '订单号', v: o.order_no },
    { k: '下单时间', v: formatDateTime(o.created_at) },
    { k: '结果', v: orderResult(o) },
    { k: '原价', v: money(original) },
  ]
  if (o.discount_amount > 0) facts.push({ k: '优惠', v: `−${money(o.discount_amount)}${o.coupon_code ? `（${o.coupon_code}）` : ''}` })
  if (o.balance_applied > 0) facts.push({ k: '余额抵扣', v: `−${money(o.balance_applied)}` })
  if (PAID_STATUSES.has(o.status) || o.status.endsWith('refunded')) facts.push({ k: '实付', v: money(o.paid_amount) })
  if (o.refunded_amount > 0) facts.push({ k: '退款', v: money(o.refunded_amount) })
  if (OPEN_STATUSES.includes(o.status as (typeof OPEN_STATUSES)[number]) && o.expires_at) facts.push({ k: '过期时间', v: formatDateTime(o.expires_at) })
  for (const p of o.payments) {
    facts.push({ k: '支付记录', v: [p.provider_name ?? p.method ?? '支付', formatMoney(p.amount, p.currency), PAYMENT_STATUS[p.status] ?? p.status, formatDateTime(p.created_at)].join(' · ') })
  }
  return facts
}
