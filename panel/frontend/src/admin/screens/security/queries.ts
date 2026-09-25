/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useQuery / useQueryClient，依赖 react 的 useCallback，依赖 ../../../shell/runtime 的 useApi，依赖 ../../actions 的 useCan / useFailure / useIntentKey（转出），依赖 ./logic 的查询串与常量，依赖 ./schemas
 * [OUTPUT]: 对外提供 SK 查询键前缀、安全与运维页各读 hook（审计分页、访问日志含实时尾随、IP 聚类、降级开关）、useInvalidateSecurity，并转出 useCan / useFailure / useIntentKey
 * [POS]: admin/screens/security 的数据层：读只经 react-query + core/api。这几张表都不在 SSE 监听里：访问日志的「实时尾随」按契约 5 秒轮询首页；降级开关的 switches.changed 广播还没登记进 core/query 的 REALTIME_TOPICS（报告协调会话），先靠写后失效与窗口聚焦重拉
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'
import { useApi } from '../../../shell/runtime'
import { accessQuery, auditQuery, TAIL_MS, type AccessFilter, type AuditFilter } from './logic'
import { accessResponse, auditResponse, clustersResponse, switchesResponse } from './schemas'

export { useCan, useFailure, useIntentKey } from '../../actions'

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

/** tail = 实时尾随：只在第一页、未暂停时每 5 秒重拉（页面在后台时 react-query 自己停） */
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
    queryFn: ({ signal }) => api.get('v1/switches', switchesResponse, { signal }).then((r) => r.switches),
  })
}

export function useInvalidateSecurity() {
  const client = useQueryClient()
  return useCallback((...scope: string[]) => client.invalidateQueries({ queryKey: [...SK, ...scope] }), [client])
}
