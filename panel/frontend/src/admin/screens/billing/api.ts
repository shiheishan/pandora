/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useQuery / useQueryClient / keepPreviousData，依赖 react 的 useCallback，依赖 ../../../shell/runtime 的 useApi，依赖 ../users/api 的 usersSchema（人工开单选用户），依赖 ./schemas 的 schema
 * [OUTPUT]: 对外提供读 hook（useOrders、useOrder、useOrderPayments、useUserPick、useLatePayments、useProviders、useAdjustments）、分页常量 ORDERS_PAGE / LATE_PAGE、BK 查询键前缀与 useInvalidateBilling，并转出 ./schemas 的全部 schema 与类型
 * [POS]: admin/screens/billing 的数据层：读只经 react-query + core/api；订单相关查询挂 orders.changed（挂账、渠道、收入调整的表没有专门 topic，写后按 BK 前缀整体失效）；页面只从这里取 schema 与 hook
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { keepPreviousData, useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'
import { useApi } from '../../../shell/runtime'
import { usersSchema, type UserRow } from '../users/api'
import { adjustmentsSchema, latePaymentsSchema, orderResponseSchema, ordersSchema, paymentHistorySchema, providersSchema, type LateStatus } from './schemas'

export * from './schemas'

export const BK = ['admin', 'billing'] as const
export const ORDERS_PAGE = 25
export const LATE_PAGE = 25
const orderTopics = ['orders.changed'] as const

export interface OrdersParams {
  q?: string
  /** 逗号分隔的后端状态（R63 多值白名单） */
  status?: string
  user_id?: string
  offset: number
}

export function useOrders(params: OrdersParams) {
  const api = useApi()
  return useQuery({
    queryKey: [...BK, 'orders', params],
    queryFn: ({ signal }) => api.get('v1/orders', ordersSchema, { signal, query: { ...params, limit: ORDERS_PAGE } }),
    meta: { topics: orderTopics },
    placeholderData: keepPreviousData,
  })
}

export function useOrder(id: string | null) {
  const api = useApi()
  return useQuery({
    queryKey: [...BK, 'order', id],
    queryFn: ({ signal }) => api.get(`v1/orders/${encodeURIComponent(id!)}`, orderResponseSchema, { signal }).then((r) => r.order),
    enabled: id !== null,
    meta: { topics: orderTopics },
  })
}

/** 支付记录要 billing.payment.read：没有就不请求，抽屉里整块不画 */
export function useOrderPayments(id: string | null, enabled: boolean) {
  const api = useApi()
  return useQuery({
    queryKey: [...BK, 'payments', id],
    queryFn: ({ signal }) => api.get(`v1/orders/${encodeURIComponent(id!)}/payments`, paymentHistorySchema, { signal }),
    enabled: enabled && id !== null,
    meta: { topics: orderTopics },
  })
}

/** 人工开单的用户选择器：GET v1/users?q=（iam.user.read），只取前 8 个候选 */
export function useUserPick(q: string, enabled: boolean) {
  const api = useApi()
  const term = q.trim()
  return useQuery<UserRow[]>({
    queryKey: [...BK, 'user-pick', term],
    queryFn: ({ signal }) => api.get('v1/users', usersSchema, { signal, query: { q: term, limit: 8, offset: 0 } }).then((r) => r.users),
    enabled: enabled && term.length > 0,
    placeholderData: keepPreviousData,
    staleTime: 30_000,
  })
}

export function useLatePayments(status: LateStatus | '', offset: number) {
  const api = useApi()
  return useQuery({
    queryKey: [...BK, 'late', status, offset],
    queryFn: ({ signal }) => api.get('v1/late-payments', latePaymentsSchema, { signal, query: { status: status || undefined, limit: LATE_PAGE, offset } }),
    placeholderData: keepPreviousData,
  })
}

export function useProviders() {
  const api = useApi()
  return useQuery({
    queryKey: [...BK, 'providers'],
    queryFn: ({ signal }) => api.get('v1/payment-providers', providersSchema, { signal }).then((r) => r.providers),
  })
}

/** 最多 200 条、无分页（契约）；currency 空 = 全部 */
export function useAdjustments(currency: string) {
  const api = useApi()
  return useQuery({
    queryKey: [...BK, 'adjustments', currency],
    queryFn: ({ signal }) => api.get('v1/revenue/adjustments', adjustmentsSchema, { signal, query: { currency: currency || undefined } }).then((r) => r.adjustments),
  })
}

/** 写成功（或失败后状态可能已变）时整个前缀重拉：订单状态、挂账合计、渠道统计互相牵连 */
export function useInvalidateBilling() {
  const client = useQueryClient()
  return useCallback(() => client.invalidateQueries({ queryKey: BK }), [client])
}
