/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useQuery / useQueryClient，依赖 react 的 useCallback / useRef，依赖 ../../../core/api 的 isApiError / newIdempotencyKey，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 useToast，依赖 ../../me 的 useAdminMe，依赖 ./schemas
 * [OUTPUT]: 对外提供 MK 查询键前缀、useCan、useIntentKey、营销页各读接口的 hook（套餐目录、优惠券、兑换记录、礼品卡模板 / 统计 / 批次 / 卡码 / 使用记录、佣金总览、提现）、useInvalidateMarketing、useFailure（写操作失败的统一处理）
 * [POS]: admin/screens/marketing 的数据层：读只经 react-query + core/api；营销相关表没有变更通知，只有 orders.changed 与券的兑换、佣金的计提相关，挂在券列表与佣金总览上；写操作后按前缀整体失效
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useRef } from 'react'
import { isApiError, newIdempotencyKey } from '../../../core/api'
import { useApi } from '../../../shell/runtime'
import { useToast } from '../../../ui'
import { useAdminMe } from '../../me'
import {
  batchesResponse,
  codesResponse,
  couponsResponse,
  giftStatsSchema,
  overviewSchema,
  plansResponse,
  redemptionsResponse,
  templatesResponse,
  usagesResponse,
  withdrawalsResponse,
} from './schemas'

export const MK = ['admin', 'marketing'] as const

/** 当前管理员是否有某个权限码；me 未回来时一律 false（按钮先不画） */
export function useCan(): (code: string) => boolean {
  const me = useAdminMe()
  const perms = me.data?.permissions
  return useCallback((code: string) => perms?.includes(code) ?? false, [perms])
}

/** 套餐目录：券的适用套餐、套餐卡的套餐与价格；没有 catalog.read 时不请求，页面退回「N 个套餐」 */
export function usePlanCatalog() {
  const api = useApi()
  const can = useCan()
  return useQuery({
    queryKey: ['admin', 'plans', 'catalog-for-marketing'],
    queryFn: ({ signal }) => api.get('v1/plans', plansResponse, { signal }).then((r) => r.plans),
    enabled: can('catalog.read'),
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

export function useGiftCodes(params: { batch_id: string | null; limit: number; offset: number }) {
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

export function useWithdrawals() {
  const api = useApi()
  return useQuery({
    queryKey: [...MK, 'withdrawals'],
    queryFn: ({ signal }) => api.get('v1/withdrawals', withdrawalsResponse, { signal }).then((r) => r.withdrawals),
  })
}

/** 写成功后整个营销前缀失效：券 / 卡 / 提现之间有交叉的统计，逐个挑键容易漏 */
export function useInvalidateMarketing() {
  const client = useQueryClient()
  return useCallback(() => client.invalidateQueries({ queryKey: MK }), [client])
}

/**
 * 写操作失败的统一处理：reauth_required 是用户在框里点了取消，静默（R34）；
 * 422 带 fields 的交给 setFields 在表单里标红，其余（含 409 与网络错误）弹 Toast。
 * 返回 true 表示已由表单接住。
 */
export function useFailure() {
  const toast = useToast()
  return useCallback(
    (error: unknown, setFields?: (fields: Record<string, string>) => void): boolean => {
      if (isApiError(error, 'reauth_required')) return true
      if (isApiError(error) && setFields && Object.keys(error.fields).length > 0) {
        setFields({ ...error.fields })
        return true
      }
      toast(isApiError(error) ? error.message : '操作失败，请稍后重试', 'danger')
      return false
    },
    [toast],
  )
}

/**
 * 幂等键：一次用户意图一个 key——同一份请求体的重试与 reauth 重放复用它；
 * 用户改了表单再提交就是新的意图，换新 key（同 key 换请求体后端回 409 idempotency_key_reuse）。
 * 成功后调 reset，下一次提交必然是新 key。
 */
export function useIntentKey() {
  const slot = useRef<{ fingerprint: string; key: string } | null>(null)
  const keyFor = useCallback((body: unknown) => {
    const fingerprint = JSON.stringify(body)
    if (slot.current?.fingerprint !== fingerprint) slot.current = { fingerprint, key: newIdempotencyKey() }
    return slot.current.key
  }, [])
  const reset = useCallback(() => {
    slot.current = null
  }, [])
  return { keyFor, reset }
}
