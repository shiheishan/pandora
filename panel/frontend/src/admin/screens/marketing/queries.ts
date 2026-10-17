import { useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'
import { useApi } from '../../../shell/runtime'
import {
  batchesResponse,
  codesResponse,
  couponsResponse,
  giftStatsSchema,
  overviewSchema,
  plansResponse,
  redemptionsResponse,
  reviewEntriesResponse,
  templatesResponse,
  usagesResponse,
  overridesResponse,
  withdrawalsResponse,
} from './schemas'

// 写操作共用的三件在 admin/actions.ts，这里转出，模块内各文件照旧从 ./queries 取
export { useFailure, useIntentKey } from '../../actions'

const MK = ['admin', 'marketing'] as const

/** 套餐目录：券的适用套餐、套餐卡的套餐与价格 */
export function usePlanCatalog() {
  const api = useApi()
  return useQuery({
    queryKey: ['admin', 'plans', 'catalog-for-marketing'],
    queryFn: ({ signal }) => api.get('v1/plans', plansResponse, { signal }).then((r) => r.plans),
    meta: { topics: ['plans.changed'] },
    staleTime: 60_000,
  })
}

export function useCoupons(params: { status?: string; limit: number; offset: number }) {
  const api = useApi()
  return useQuery({
    queryKey: [...MK, 'coupons', params],
    queryFn: ({ signal }) => api.get('v1/coupons', couponsResponse, { signal, query: params }),
    meta: { topics: ['orders.changed'] },
    placeholderData: (prev) => prev,
  })
}

export function useRedemptions(couponId: string | null) {
  const api = useApi()
  return useQuery({
    queryKey: [...MK, 'redemptions', couponId],
    queryFn: ({ signal }) => api.get(`v1/coupons/${couponId}/redemptions`, redemptionsResponse, { signal }).then((r) => r.redemptions),
    enabled: couponId !== null,
    meta: { topics: ['orders.changed'] },
  })
}

export function useGiftTemplates() {
  const api = useApi()
  return useQuery({
    queryKey: [...MK, 'gift-templates'],
    queryFn: ({ signal }) => api.get('v1/gift-cards', templatesResponse, { signal }).then((r) => r.templates),
  })
}

export function useGiftStats() {
  const api = useApi()
  return useQuery({ queryKey: [...MK, 'gift-stats'], queryFn: ({ signal }) => api.get('v1/gift-cards/stats', giftStatsSchema, { signal }) })
}

export function useGiftBatches(params: { limit: number; offset: number }) {
  const api = useApi()
  return useQuery({
    queryKey: [...MK, 'gift-batches', params],
    queryFn: ({ signal }) => api.get('v1/gift-cards/batches', batchesResponse, { signal, query: params }),
    placeholderData: (prev) => prev,
  })
}

export function useGiftCodes(params: { batch_id: string | null; status?: string; limit: number; offset: number }) {
  const api = useApi()
  return useQuery({
    queryKey: [...MK, 'gift-codes', params],
    queryFn: ({ signal }) => api.get('v1/gift-cards/codes', codesResponse, { signal, query: params }),
    enabled: params.batch_id !== null,
    placeholderData: (prev) => prev,
  })
}

export function useGiftUsages(templateId: string) {
  const api = useApi()
  return useQuery({
    queryKey: [...MK, 'gift-usages', templateId],
    queryFn: ({ signal }) => api.get('v1/gift-cards/usages', usagesResponse, { signal, query: { template_id: templateId } }).then((r) => r.usages),
  })
}

export function useCommissionOverview() {
  const api = useApi()
  return useQuery({
    queryKey: [...MK, 'commission-overview'],
    queryFn: ({ signal }) => api.get('v1/commission/overview', overviewSchema, { signal }),
    meta: { topics: ['orders.changed'] },
  })
}

/**
 * 提现列表按「处理中 / 已完成 / 全部」筛：后端 status 收逗号分隔的多个状态
 * （处理中 = requested,approved；已完成 = paid,rejected；全部不带 status）
 */
export type WithdrawalFilter = 'open' | 'done' | 'all'
export const WITHDRAWAL_FILTER_STATUS: Readonly<Record<WithdrawalFilter, string | undefined>> = {
  open: 'requested,approved',
  done: 'paid,rejected',
  all: undefined,
}

export function useWithdrawals(filter: WithdrawalFilter) {
  const api = useApi()
  const status = WITHDRAWAL_FILTER_STATUS[filter]
  return useQuery({
    queryKey: [...MK, 'withdrawals', filter],
    queryFn: ({ signal }) => api.get('v1/withdrawals', withdrawalsResponse, { signal, query: status ? { status } : {} }).then((r) => r.withdrawals),
  })
}

const REVIEW_PAGE = 20
export function useReviewEntries() {
  const api = useApi()
  return useInfiniteQuery({
    queryKey: [...MK, 'review-entries'],
    queryFn: ({ signal, pageParam }) =>
      api.get('v1/commission/entries', reviewEntriesResponse, { signal, query: { review: 'required', limit: REVIEW_PAGE, cursor: pageParam || undefined } }),
    initialPageParam: '',
    getNextPageParam: (last) => last.next_cursor || undefined,
    meta: { topics: ['orders.changed'] },
  })
}

/** 写成功后整个营销前缀失效：券 / 卡 / 提现之间有交叉的统计，逐个挑键容易漏 */
export function useInvalidateMarketing() {
  const client = useQueryClient()
  return useCallback(() => client.invalidateQueries({ queryKey: MK }), [client])
}

export function useCommissionOverrides() {
  const api = useApi()
  return useQuery({
    queryKey: [...MK, 'commission-overrides'],
    queryFn: ({ signal }) => api.get('v1/commission/overrides', overridesResponse, { signal }).then((r) => r.overrides),
  })
}
