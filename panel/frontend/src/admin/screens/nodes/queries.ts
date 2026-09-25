/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useQuery / useQueryClient，依赖 react 的 useCallback，依赖 ../../../shell/runtime 的 useApi，依赖 ../marketing/queries 的 useCan / useFailure / useIntentKey（后台前端一把它们提升到 admin/actions.ts 并合入后改为从那里引用），依赖 ./schemas
 * [OUTPUT]: 对外提供 NK 查询键前缀、节点页各读 hook（节点列表、协议 schema、服务器与节点池选项、身份、探针、单节点与全局路由）、useInvalidateNodes，并转出 useCan / useFailure / useIntentKey
 * [POS]: admin/screens/nodes 的数据层：读只经 react-query + core/api；节点列表挂 nodes.changed（nodes 表有变更通知），其余读接口没有对应表通知，写后按前缀整体失效
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'
import { useApi } from '../../../shell/runtime'
import { globalRoutingSchema, identitySchema, metricsSchema, nodeRoutingSchema, nodesResponse, poolsResponse, protocolSchemasResponse, serversResponse } from './schemas'

export { useCan, useFailure, useIntentKey } from '../marketing/queries'

export const NK = ['admin', 'nodes'] as const

/**
 * 节点列表：一次取到后端上限 1000 条，并且总带 include_retired=1——
 * 「全部」里藏掉已退役是前端筛选的事；刚退役的节点抽屉还开着，要能继续删除它
 */
export function useNodes() {
  const api = useApi()
  return useQuery({
    queryKey: [...NK, 'list'],
    queryFn: ({ signal }) => api.get('v1/nodes', nodesResponse, { signal, query: { limit: 1000, include_retired: '1' } }),
    meta: { topics: ['nodes.changed'] },
    placeholderData: (prev) => prev,
  })
}

export function useProtocolSchemas() {
  const api = useApi()
  return useQuery({
    queryKey: [...NK, 'schemas'],
    queryFn: ({ signal }) => api.get('v1/node-protocol-schemas', protocolSchemasResponse, { signal }).then((r) => r.schemas),
    staleTime: 10 * 60_000,
  })
}

export function useServerOptions(enabled = true) {
  const api = useApi()
  return useQuery({
    queryKey: [...NK, 'server-options'],
    queryFn: ({ signal }) => api.get('v1/servers', serversResponse, { signal }).then((r) => r.servers),
    enabled,
    staleTime: 60_000,
  })
}

export function usePoolOptions(enabled = true) {
  const api = useApi()
  return useQuery({
    queryKey: [...NK, 'pool-options'],
    queryFn: ({ signal }) => api.get('v1/node-pools', poolsResponse, { signal }).then((r) => r.pools),
    enabled,
    staleTime: 60_000,
  })
}

export function useNodeIdentity(id: string) {
  const api = useApi()
  return useQuery({ queryKey: [...NK, 'identity', id], queryFn: ({ signal }) => api.get(`v1/nodes/${id}/identity`, identitySchema, { signal }) })
}

/** 近 24 小时（1440 分钟）探针；节点不存在后端回 200 空 points，不是 404 */
export function useNodeMetrics(id: string) {
  const api = useApi()
  return useQuery({
    queryKey: [...NK, 'metrics', id],
    queryFn: ({ signal }) => api.get(`v1/nodes/${id}/metrics`, metricsSchema, { signal, query: { minutes: 1440 } }),
    refetchInterval: 60_000,
  })
}

export function useNodeRouting(id: string) {
  const api = useApi()
  return useQuery({ queryKey: [...NK, 'routing', id], queryFn: ({ signal }) => api.get(`v1/nodes/${id}/routing`, nodeRoutingSchema, { signal }) })
}

/** 全局出站：单节点规则可以指向它们（R26） */
export function useGlobalRouting() {
  const api = useApi()
  return useQuery({ queryKey: [...NK, 'global-routing'], queryFn: ({ signal }) => api.get('v1/nodes/routing', globalRoutingSchema, { signal }), staleTime: 60_000 })
}

export function useInvalidateNodes() {
  const client = useQueryClient()
  return useCallback(() => client.invalidateQueries({ queryKey: NK }), [client])
}
