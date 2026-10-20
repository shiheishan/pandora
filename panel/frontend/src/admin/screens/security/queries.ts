import { useIsFetching, useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'
import { useApi } from '../../../shell/runtime'
import { accessQuery, auditQuery, TAIL_MS, type AccessFilter, type AuditFilter } from './logic'
import { accessResponse, auditResponse, clustersResponse, switchesResponse } from './schemas'
import {
  traceAccountsResponse,
  traceIPsResponse,
  traceQuery,
  traceRevealResponse,
  traceSubscribersResponse,
  type AccSort,
  type SubSort,
  type TraceReveal,
  type TraceWindow,
} from './trace'

export { useFailure, useIntentKey } from '../../actions'

export const SK = ['admin', 'security'] as const

export function useAudit(filter: AuditFilter, offset: number) {
  const api = useApi()
  const query = auditQuery(filter, offset)
  return useQuery({
    queryKey: [...SK, 'audit', query],
    queryFn: ({ signal }) => api.get('v1/audit', auditResponse, { query, signal }),
    placeholderData: (prev) => prev,
  })
}

/** tail = 实时监控：只在第一页、未暂停时每 5 秒重拉（页面在后台时 react-query 自己停） */
export function useAccessLog(filter: AccessFilter, offset: number, tail: boolean) {
  const api = useApi()
  const query = accessQuery(filter, offset)
  return useQuery({
    queryKey: [...SK, 'access', query],
    queryFn: ({ signal }) => api.get('v1/access-log', accessResponse, { query, signal }).then((r) => r.items),
    placeholderData: (prev) => prev,
    refetchInterval: tail && offset === 0 ? TAIL_MS : false,
  })
}

export function useClusters(includeReviewed: boolean) {
  const api = useApi()
  return useQuery({
    queryKey: [...SK, 'clusters', includeReviewed],
    queryFn: ({ signal }) => api.get('v1/ip-clusters', clustersResponse, { query: includeReviewed ? { include_reviewed: 1 } : undefined, signal }).then((r) => r.clusters),
  })
}

export function useSwitches() {
  const api = useApi()
  return useQuery({
    queryKey: [...SK, 'switches'],
    meta: { topics: ['switches.changed'] },
    queryFn: ({ signal }) => api.get('v1/switches', switchesResponse, { signal }).then((r) => r.switches),
  })
}

export function useInvalidateSecurity() {
  const client = useQueryClient()
  return useCallback((...scope: string[]) => client.invalidateQueries({ queryKey: [...SK, ...scope] }), [client])
}

// ---------------------------------------------------------------------------
// 溯源总览：三张排行都不在 SSE 里，不挂 topics、不轮询；换 window 时保留上一份数据。
// 每次都是整窗聚合（30 天要扫整张拉取日志），所以 5 分钟内不自动重拉、回到窗口也不重拉，页面上手动「刷新」。
// ---------------------------------------------------------------------------

/** 三张排行共用的缓存策略 */
const TRACE_CACHE = { staleTime: 5 * 60_000, refetchOnWindowFocus: false } as const

/** 溯源总览查询键的公共前缀（手动刷新时整组作废） */
export const TRACE_KEY = [...SK, 'trace'] as const

export function useTraceIPs(window: TraceWindow) {
  const api = useApi()
  return useQuery({
    queryKey: [...TRACE_KEY, 'ips', window],
    queryFn: ({ signal }) => api.get('v1/trace/ips', traceIPsResponse, { query: traceQuery(window), signal }),
    placeholderData: (prev) => prev,
    ...TRACE_CACHE,
  })
}

export function useTraceSubscribers(window: TraceWindow, sort: SubSort) {
  const api = useApi()
  return useQuery({
    queryKey: [...TRACE_KEY, 'subscribers', window, sort],
    queryFn: ({ signal }) => api.get('v1/trace/subscribers', traceSubscribersResponse, { query: traceQuery(window, sort), signal }),
    placeholderData: (prev) => prev,
    ...TRACE_CACHE,
  })
}

export function useTraceAccounts(window: TraceWindow, sort: AccSort) {
  const api = useApi()
  return useQuery({
    queryKey: [...TRACE_KEY, 'accounts', window, sort],
    queryFn: ({ signal }) => api.get('v1/trace/accounts', traceAccountsResponse, { query: traceQuery(window, sort), signal }),
    placeholderData: (prev) => prev,
    ...TRACE_CACHE,
  })
}

/** 「显示」完整 IP：不缓存，每次都写入审计；返回一个按令牌取地址的函数 */
export function useTraceReveal(): (token: string) => Promise<TraceReveal> {
  const api = useApi()
  return useCallback((token: string) => api.get('v1/trace/reveal', traceRevealResponse, { query: { token } }), [api])
}

/** 手动「刷新」：作废溯源总览的全部查询（当前显示的立刻重拉）；busy 为有查询在途 */
export function useRefreshTrace(): { refresh: () => void; busy: boolean } {
  const client = useQueryClient()
  const busy = useIsFetching({ queryKey: TRACE_KEY }) > 0
  const refresh = useCallback(() => void client.invalidateQueries({ queryKey: TRACE_KEY }), [client])
  return { refresh, busy }
}
