import { useQuery } from '@tanstack/react-query'
import { isApiError } from '../../../core/api'
import { periodMonths, periodOf } from './catalog'
import type { Subscription } from './subscriptions'
import { useApi } from '../../../shell/runtime'

// ---------------------------------------------------------------------------
// POST v1/me/checkout/quote（购买模型设计稿 2.2）：金额与默认值一律由服务端算，前端只在
// with_balance / without_balance 两组数之间切换，不做任何金额运算（旧的 buildQuote 退役）。
// 字段与 Go 的 billing.QuoteOutput / Quote / purchase.Balance 一一对应，都没有 omitempty：
// 指针字段（各 id、优惠码、明细、周期）为 null 而不缺席。
// ---------------------------------------------------------------------------
export { balanceSplitSchema, creditDetailSchema, quoteRowSchema, quoteSchema, type BalanceSplit, type CreditDetail, type Quote, type QuoteRow } from './quote-schema'
import { quoteSchema, type BalanceSplit, type Quote, type QuoteRow } from './quote-schema'

export interface QuoteRequest {
  action: 'renew' | 'change' | 'new' | 'pack'
  subscription_id?: string
  plan_id?: string
  pack_id?: string
  coupon_code?: string
  new_copy?: boolean
}

/** 报价只读、不幂等；as_of 只在 10 分钟内有效，5 分钟重报一次，订单或订阅变了也重报 */
export const QUOTE_REFRESH_MS = 5 * 60_000

export function useQuote(req: QuoteRequest | null) {
  const api = useApi()
  return useQuery({
    queryKey: ['portal', 'quote', req],
    queryFn: ({ signal }) => api.post('v1/me/checkout/quote', quoteSchema, { body: req, signal }),
    enabled: req !== null,
    refetchInterval: QUOTE_REFRESH_MS,
    retry: false,
    meta: { topics: ['orders.changed', 'subscriptions.changed'] },
  })
}

/** 余额开关在两组数之间切换（Forced 时只能用余额） */
export const balanceSplit = (row: QuoteRow, useBalance: boolean): BalanceSplit => (useBalance || row.with_balance.forced ? row.with_balance : row.without_balance)

/** 建单时带回的报价：as_of 与 expect，服务端按同一套函数重算，任一项不等回 409 quote_changed */
export function expectation(quote: Quote, row: QuoteRow, split: BalanceSplit) {
  return { as_of: quote.as_of, expect: { total: row.total, balance_applied: split.applied, payable: split.payable } }
}

/** 换套餐「今天付 ¥x」还是「退 ¥y」：按同一行的 total / refund */
export const changeOutcome = (row: Pick<QuoteRow, 'total' | 'refund'>): { pay: number; refund: number } => ({ pay: row.total, refund: row.refund })

/** 建单被拒的两种购买特有情形（设计稿 2.2 / 2.4，httpx.CodeQuoteChanged / CodeOrderPending，都是 409），按码认 */
export function purchaseRefusal(error: unknown): 'quote_changed' | 'order_pending' | null {
  if (isApiError(error, 'quote_changed')) return 'quote_changed'
  if (isApiError(error, 'order_pending')) return 'order_pending'
  return null
}

// ---------------------------------------------------------------------------
// 价格档：报价按（订阅，套餐）展开时每个价格档各一条（A 路实现）。列表里每个组合只显示一档：
// 与这一份现在同样长的那档；确认页「买多久」才把三档都摆出来。
// ---------------------------------------------------------------------------
/** 「买多久」按时长排：1 个月、3 个月、1 年，其余排后 */
const rowMonths = (r: Pick<QuoteRow, 'interval' | 'interval_count'>) => periodMonths(periodOf({ billing_interval: r.interval as never, interval_count: r.interval_count })) ?? 999
export const sortTiers = (rows: readonly QuoteRow[]) => [...rows].sort((a, b) => rowMonths(a) - rowMonths(b) || a.subtotal - b.subtotal)

/** 默认的一档：地址点名的 → 续费沿用原价格 → 与这一份现在同样长的 → 第一档 */
export function defaultTier(rows: readonly QuoteRow[], sub: Subscription | undefined, requested: string | null): QuoteRow | null {
  const sorted = sortTiers(rows)
  const hit = (pred: (r: QuoteRow) => boolean) => sorted.find(pred) ?? null
  const rp = sub?.renewal_price
  return (
    (requested ? hit((r) => r.price_id === requested) : null) ??
    (rp?.available ? hit((r) => r.price_id === rp.id) : null) ??
    (rp ? hit((r) => r.interval === rp.billing_interval && r.interval_count === rp.interval_count) : null) ??
    sorted[0] ??
    null
  )
}


/** 某个（订阅，套餐）组合在列表里显示的那一档 */
export function tierOf(rows: readonly QuoteRow[], sub: Subscription | undefined, match: (r: QuoteRow) => boolean): QuoteRow | undefined {
  return defaultTier(rows.filter(match), sub, null) ?? undefined
}
