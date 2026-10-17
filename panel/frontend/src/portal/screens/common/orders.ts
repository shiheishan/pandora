import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { z } from 'zod'
import { formatDateTime, formatMoney } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { ordersPageSchema, orderRowSchema, PENDING_ORDERS_KEY, type OrderRow } from '../../queries'
import { endsIntent } from './intent'

// 订单行与列表页的 schema、待支付查询键归外框 portal/queries.ts（侧栏待支付数与这里同一个键，只留一份定义），这里转出给页面用
export { ORDER_STATUSES, orderRowSchema, ordersPageSchema, PENDING_ORDERS_KEY, type OrderRow } from '../../queries'

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

/** 契约门户-04 的标题映射；plan_name 缺席时为空串，商品名 Go 无 omitempty 恒在。 */
export function orderTitle(o: Pick<OrderRow, 'kind' | 'plan_name' | 'item_name' | 'interval' | 'interval_count'>): string {
  const plan = o.plan_name ?? ''
  const period = intervalLabel(o.interval, o.interval_count)
  switch (o.kind) {
    case 'topup':
      return '余额充值'
    case 'traffic_reset':
      return o.item_name
    case 'upgrade':
      return `${plan} · 变更套餐`
    case 'renewal':
      return `${plan} · ${period ? `${period}续费` : '续费'}`
    case 'stack':
      return `叠加 · ${plan || o.item_name}`
    default:
      return period ? `${plan} · ${period}` : plan
  }
}

/** 待支付卡的倒计时（样稿「18 分钟后自动取消」）：后端 30 分钟过期，按 expires_at 倒数，页面每 30 秒重算一次。 */
export function expiryNote(expiresAt: string | undefined, now: Date = new Date()): string {
  if (!expiresAt) return '请尽快完成支付'
  const minutes = Math.ceil((new Date(expiresAt).getTime() - now.getTime()) / 60_000)
  return minutes > 0 ? `${minutes} 分钟后自动取消` : '即将自动取消'
}

/** 列表行与收据里的短时间「10-13 17:03」（浏览器本地时区，与 formatDateTime 同口径） */
export const shortDateTime = (at: string) => formatDateTime(at).slice(5)

// ---------------------------------------------------------------------------
// GET v1/orders/{id}（修订 R69 的明细字段）：支付回跳后的结果确认与订单页的明细共用
// ---------------------------------------------------------------------------
export const orderDetailSchema = z.object({
  order: orderRowSchema.extend({
    // plan_id、price_id：「改用余额付」按同样的套餐与价格档重新下单；充值单为 null
    items: z.array(
      z.object({
        name: z.string(),
        quantity: z.number().int(),
        unit_amount: z.number().int(),
        line_amount: z.number().int(),
        plan_id: z.string().nullable(),
        price_id: z.string().nullable(),
      }),
    ),
    payments: z.array(
      z.object({
        status: z.string(),
        amount: z.number().int(),
        created_at: z.string(),
        method: z.string().optional(),
        provider_name: z.string(),
      }),
    ),
    coupon_code: z.string().optional(),
    subscription_period_end: z.string().optional(),
    // 落到的那一份：续费、换套餐、叠加、重置单建单时就有（原来那份）；新购履约后才有（新开的那份），之前为 null
    subscription_id: z.string().nullable(),
    // 套餐归档转余额（cancel_reason=plan_archived）时扣下不退的支付手续费（分），其余订单为 0
    surcharge_kept: z.number().int().optional(),
  }),
})
export type OrderDetail = z.output<typeof orderDetailSchema>['order']

const orderKey = (id: string) => ['portal', 'orders', 'detail', id] as const

/** 不会再变成已支付的终态 */
const SETTLED_STATUSES: ReadonlySet<string> = new Set(['paid', 'fulfilled', 'cancelled', 'expired'])

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
// 下单接口同形的 201 响应
// ---------------------------------------------------------------------------
export const orderCreatedSchema = z.object({
  order_id: z.string(),
  order_no: z.string(),
  total_amount: z.number().int(),
  discount_amount: z.number().int(),
  balance_applied: z.number().int(),
  payable_amount: z.number().int(),
  status: z.enum(['pending_payment', 'fulfilled']),
  // 变更套餐另带（修订 R36）
  proration_credit: z.number().int().optional(),
  balance_refund: z.number().int().optional(),
})

export const PAID_STATUSES: ReadonlySet<string> = new Set(['paid', 'fulfilled'])

/** 发起支付只收 pending_payment（契约门户-03 支付条目） */
const PAYABLE_STATUSES: ReadonlySet<string> = new Set(['pending_payment'])

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
// 订单页（契约门户-04 与修订 R69：status 可逗号多值、counts 三类计数）
// 设计的「全部」= 除待支付外的所有状态；待支付在顶部单独成卡
// ---------------------------------------------------------------------------
export const ORDER_FILTERS = {
  all: ['paid', 'fulfilled', 'cancelled', 'expired'],
  paid: ['paid', 'fulfilled'],
  cancelled: ['cancelled', 'expired'],
} as const
export type OrderFilter = keyof typeof ORDER_FILTERS

const ORDER_PAGE_SIZE = 6

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

/**
 * 顶部待支付卡片：与外框侧栏的待支付数同一个查询（同键、同请求，queries.ts 的 PENDING_ORDERS_KEY），
 * 不带 limit（后端默认 20 条）；count 用 counts.open（超过 20 张时也准）
 */
export function useOpenOrders() {
  const api = useApi()
  return useQuery({
    queryKey: PENDING_ORDERS_KEY,
    queryFn: ({ signal }) => api.get('v1/orders', ordersPageSchema, { query: { status: 'pending_payment' }, signal }),
    select: (d) => ({ orders: d.orders, count: d.counts.open }),
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
interface StatusBadge {
  label: string
  tone: 'ok' | 'warn' | 'neutral' | 'info'
}

export function statusBadge(status: OrderRow['status']): StatusBadge {
  switch (status) {
    case 'paid':
    case 'fulfilled':
      return { label: '已支付', tone: 'ok' }
    case 'pending_payment':
      return { label: '待支付', tone: 'warn' }
    default:
      return { label: '已取消', tone: 'neutral' }
  }
}

interface MonthGroup<T> {
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

const METHOD_NAMES: Readonly<Record<string, string>> = { alipay: '支付宝', wxpay: '微信支付', wechat: '微信支付', qqpay: 'QQ 钱包', offline: '线下' }

/**
 * 付款方式的用户叫法（支付宝、微信支付）：按 payments.method 认，不用渠道的商户名；
 * 几笔方式不同或认不出时说「在线」
 */
export function payMethodName(payments: ReadonlyArray<Pick<OrderDetail['payments'][number], 'method'>>): string {
  const names = new Set(payments.map((p) => (p.method && METHOD_NAMES[p.method]) || '在线'))
  return names.size === 1 ? [...names][0]! : '在线'
}

/** 套餐归档后才付款（10-16）：订单取消、不开通，钱转进钱包余额 */
export const isPlanArchivedRefund = (o: { status: string; cancel_reason?: string }) => o.status === 'cancelled' && o.cancel_reason === 'plan_archived'

/**
 * 归档转余额的去向说明（10-16 定「手续费不退」）：扣了手续费写「应付金额」并注明不退；没扣写「付的钱」。
 * surchargeKept 不知道（查单结果里没有）时用统一说法。
 */
export function archivedRefundText(surchargeKept: number | undefined): string {
  if (surchargeKept === undefined) return '应付金额已转入钱包余额，支付手续费不退'
  return surchargeKept > 0 ? '应付金额已转入钱包余额（支付手续费不退）' : '付的钱已转入钱包余额'
}

/** 后端的取消原因：已知的码换成话，认不出的英文码（形如 user_cancelled）不露给用户，写「已取消」；后台填的中文原因照写 */
function cancelText(reason: string | undefined): string {
  if (!reason || reason === 'user_cancelled') return '已由您取消'
  if (reason === 'expired') return '超时未支付，已自动取消'
  return /^[a-z0-9_]+$/.test(reason) ? '已取消' : reason
}

/** 展开区第一行「结果」（样稿 .rc-res：「已续上，有效期至 2026-11-13」） */
export function orderResult(o: Pick<OrderDetail, 'status' | 'kind' | 'cancel_reason' | 'subscription_period_end' | 'expires_at' | 'surcharge_kept'>): string {
  const until = o.subscription_period_end ? `，有效期至 ${formatDateTime(o.subscription_period_end).slice(0, 10)}` : ''
  switch (o.status) {
    case 'fulfilled':
    case 'paid':
      if (o.kind === 'topup') return '余额已到账'
      if (o.kind === 'stack') return '已叠加，总流量加上了'
      if (o.kind === 'traffic_reset') return '本期流量已清零'
      if (o.kind === 'upgrade') return '已换好套餐，链接不变'
      if (o.kind === 'renewal') return `已续上${until}`
      return `已开通${until}`
    case 'expired':
      return '超时未支付，已自动取消'
    case 'cancelled':
      if (o.cancel_reason === 'plan_archived') return `套餐已停售，没有开通；${archivedRefundText(o.surcharge_kept)}`
      return cancelText(o.cancel_reason)
    default:
      return o.expires_at ? `待支付，${formatDateTime(o.expires_at)} 前有效` : '待支付'
  }
}

// 收款记录库里只有 succeeded（00012_payment.sql）；认不出的原样显示
const PAYMENT_STATUS: Readonly<Record<string, string>> = { succeeded: '成功' }

/** 收据金额行（样稿 .rc-ln）：minus 为真时金额前写「−」；total 是底下加粗的合计行；code 是优惠码小标签 */
export interface ReceiptLine {
  k: string
  v: string
  minus?: boolean
  total?: boolean
  code?: string
}

/** 支付记录一行（样稿 .rc-pay）：method 给图标用（alipay / wxpay / 其他） */
export interface ReceiptPayment {
  name: string
  method: string
  meta: string
  amount: string
}

export interface Receipt {
  /** ok：已支付（✓）；否则是灰色的「–」 */
  result: { text: string; ok: boolean }
  lines: ReceiptLine[]
  payments: ReceiptPayment[]
}

/**
 * 展开收据（样稿 .rc）：
 * - 「实付」是渠道实收（payments 合计，与完成页 paidText 同口径），不是 paid_amount（库里恒等于订单总额、含余额那部分）；
 *   全部用余额付的写「全部用余额」（审计 #6）。
 * - 换套餐单（upgrade）的总额里扣了「原套餐没用完的部分」，详情里没有这一项，前端不用差值去算：
 *   不写「原价」，改写「订单金额」= total_amount，优惠只作说明（已含在订单金额里），再列余额与实付（审计 #7）。
 * - 已取消 / 超时的单，冻结的余额已退回，写「用了余额（已退回）」；待支付的单底下写「还要付」。
 */
export function orderReceipt(o: OrderDetail): Receipt {
  const money = (m: number) => formatMoney(m)
  const paid = PAID_STATUSES.has(o.status)
  const pending = o.status === 'pending_payment'
  const closed = !paid && !pending
  const lines: ReceiptLine[] = []
  const code = o.coupon_code
  if (o.kind === 'upgrade') {
    lines.push({ k: '订单金额', v: money(o.total_amount) })
    if (o.discount_amount > 0) lines.push({ k: '其中优惠', v: money(o.discount_amount), ...(code ? { code } : {}) })
  } else {
    lines.push({ k: '原价', v: money(o.items.reduce((n, i) => n + i.line_amount, 0)) })
    if (o.discount_amount > 0) lines.push({ k: '优惠', v: money(o.discount_amount), minus: true, ...(code ? { code } : {}) })
  }
  if (o.balance_applied > 0) lines.push({ k: closed ? '用了余额（已退回）' : '用了余额', v: money(o.balance_applied), minus: true })
  if (paid) {
    const online = o.payments.reduce((n, p) => n + p.amount, 0)
    lines.push({ k: '实付', v: online > 0 ? money(online) : o.balance_applied > 0 ? '全部用余额' : money(0), total: true })
  } else if (pending) {
    lines.push({ k: '还要付', v: money(o.payable_amount), total: true })
  }
  return {
    result: { text: orderResult(o), ok: paid },
    lines,
    payments: o.payments.map((p) => ({
      name: payMethodName([p]),
      method: p.method ?? '',
      meta: `${shortDateTime(p.created_at)} · ${PAYMENT_STATUS[p.status] ?? p.status}`,
      amount: money(p.amount),
    })),
  }
}

/**
 * 待付单「改用余额付」：已下的单不能再补余额（下单时才冻结），做法是取消它、带着同样的套餐与价格档回确认页（#/checkout）重下，
 * 余额在确认页默认用上。能重下的给确认页的地址参数；充值单、缺套餐或订阅信息的给 null。
 */
export function rebuyQuery(o: Pick<OrderDetail, 'kind' | 'subscription_id' | 'items'>): Record<string, string> | null {
  const it = o.items[0]
  const plan = it?.plan_id ?? null
  const price = it?.price_id ?? null
  const sub = o.subscription_id
  switch (o.kind) {
    case 'new':
      return plan && price ? { new: plan, price } : null
    case 'renewal':
      return sub && price ? { renew: sub, price } : null
    case 'stack':
      return sub && price ? { stack: sub, price } : null
    case 'upgrade':
      return sub && plan && price ? { change: sub, plan, price } : null
    case 'traffic_reset':
      return sub ? { reset: sub } : null
    default:
      return null
  }
}
