import { useQuery } from '@tanstack/react-query'
import { isApiError } from '../../../core/api'
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

/**
 * 建单被拒的两种购买特有情形（设计稿 2.2 / 2.4，httpx.CodeQuoteChanged / CodeOrderPending，都是 409）。
 * core/api.ts 的 SERVER_ERROR_CODES 还没登记这两个码，信封里的码会被归成 conflict：在 core 补登记之前
 * 先按服务端的固定文案认，登记后按码认（两条都留着，码优先）。
 */
export function purchaseRefusal(error: unknown): 'quote_changed' | 'order_pending' | null {
  if (!isApiError(error) || error.status !== 409) return null
  const code: string = error.code
  if (code === 'quote_changed' || error.message.startsWith('金额刚变了')) return 'quote_changed'
  if (code === 'order_pending' || error.message.includes('还没付款')) return 'order_pending'
  return null
}
