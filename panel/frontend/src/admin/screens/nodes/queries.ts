import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'
import { useApi } from '../../../shell/runtime'
import { effectiveRoutingSchema, globalRoutingSchema, groupRoutingSchema, identitySchema, metricsSchema, nodeDetailResponse, nodeRoutingSchema, nodesResponse, poolsResponse, protocolSchemasResponse, routeGroupsResponse, serverNodesResponse, serverSchema, serversResponse } from './schemas'

export { endsIntent, useCan, useFailure, useIntentKey } from '../../actions'

export const NK = ['admin', 'nodes'] as const

/** 一页的节点数：后端上限。节点总数不超过它时整页在本地筛选与搜索 */
export const NODE_PAGE = 1000

/**
 * 在线人数、负载、24h 流量这些实时指标的刷新间隔。心跳不再推 nodes.changed（迁移 00110），
 * 指标靠定时重拉；名称、状态、池这类真正的变更仍由 nodes.changed 立刻失效。
 * 页面不可见时 react-query 自动暂停定时重拉
 */
export const NODE_METRICS_REFRESH_MS = 30_000

/**
 * 节点列表：一次取到后端上限 1000 条，并且总带 include_retired=1——
 * 「全部」里藏掉已退役是前端筛选的事；刚退役的节点抽屉还开着，要能继续删除它。
 * 服务器卡片与路由组成员也用这一份（超过 1000 个节点时只覆盖前 1000 个）
 */
export function useNodes() {
  const api = useApi()
  return useQuery({
    queryKey: [...NK, 'list'],
    queryFn: ({ signal }) => api.get('v1/nodes', nodesResponse, { signal, query: { limit: NODE_PAGE, include_retired: '1' } }),
    meta: { topics: ['nodes.changed'] },
    placeholderData: (prev) => prev,
    refetchInterval: NODE_METRICS_REFRESH_MS,
  })
}

/**
 * 节点超过一页时改由服务端筛选、搜索、分页（state 与前端 nodeState 同一映射，q 与 filterNodes 同一组字段）。
 * enabled=false 时不发请求
 */
export function useNodePage(p: { state: string; q: string; offset: number }, enabled: boolean) {
  const api = useApi()
  return useQuery({
    queryKey: [...NK, 'page', p.state, p.q, p.offset],
    queryFn: ({ signal }) => api.get('v1/nodes', nodesResponse, { signal, query: { limit: NODE_PAGE, offset: p.offset, state: p.state, ...(p.q ? { q: p.q } : {}) } }),
    meta: { topics: ['nodes.changed'] },
    placeholderData: (prev) => prev,
    refetchInterval: NODE_METRICS_REFRESH_MS,
    enabled,
  })
}

/**
 * 单个节点连同编辑字段（protocol_config 等，GET v1/nodes?id=）：抽屉的协议表单用它，
 * 也兜住列表里没加载到的节点（地址直接指到第 1001 个以后）。不存在时为 null
 */
export function useNodeDetail(id: string | null) {
  const api = useApi()
  return useQuery({
    queryKey: [...NK, 'detail', id],
    queryFn: ({ signal }) => api.get('v1/nodes', nodeDetailResponse, { signal, query: { id: id!, include_retired: '1' } }).then((r) => r.nodes[0] ?? null),
    meta: { topics: ['nodes.changed'] },
    enabled: id !== null,
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

/**
 * 服务器与节点池：各自一条查询，节点表单的下拉与服务器、节点池两个标签共用。
 * 它们的计数（下属节点、在役数、组内成员）跟着节点变，所以也挂 nodes.changed；
 * 服务器与节点池本身没有表变更通知，写后按 NK 前缀失效
 */
export function useServers() {
  const api = useApi()
  return useQuery({
    queryKey: [...NK, 'servers'],
    queryFn: ({ signal }) => api.get('v1/servers', serversResponse, { signal }).then((r) => r.servers),
    meta: { topics: ['nodes.changed'] },
    staleTime: 30_000,
  })
}

export function useServer(id: string) {
  const api = useApi()
  return useQuery({ queryKey: [...NK, 'server', id], queryFn: ({ signal }) => api.get(`v1/servers/${id}`, serverSchema, { signal }), meta: { topics: ['nodes.changed'] } })
}

export function useServerNodes(id: string) {
  const api = useApi()
  return useQuery({
    queryKey: [...NK, 'server-nodes', id],
    queryFn: ({ signal }) => api.get(`v1/servers/${id}/nodes`, serverNodesResponse, { signal }).then((r) => r.nodes),
    meta: { topics: ['nodes.changed'] },
  })
}

export function usePools() {
  const api = useApi()
  return useQuery({
    queryKey: [...NK, 'pools'],
    queryFn: ({ signal }) => api.get('v1/node-pools', poolsResponse, { signal }).then((r) => r.pools),
    meta: { topics: ['nodes.changed'] },
    staleTime: 30_000,
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

/** 全局出站与分流：路由标签编辑它，单节点规则可以指向其中的出站（R26）；online_nodes 跟着心跳变，不设 staleTime */
export function useGlobalRouting() {
  const api = useApi()
  return useQuery({ queryKey: [...NK, 'global-routing'], queryFn: ({ signal }) => api.get('v1/nodes/routing', globalRoutingSchema, { signal }) })
}

/** 路由组列表（00096）：路由标签的组切换条与节点抽屉的「所属路由组」都用它；成员名跟着节点变，挂 nodes.changed */
export function useRouteGroups() {
  const api = useApi()
  return useQuery({
    queryKey: [...NK, 'route-groups'],
    queryFn: ({ signal }) => api.get('v1/route-groups', routeGroupsResponse, { signal }).then((r) => r.groups),
    meta: { topics: ['nodes.changed'] },
  })
}

export function useGroupRouting(id: string) {
  const api = useApi()
  return useQuery({ queryKey: [...NK, 'group-routing', id], queryFn: ({ signal }) => api.get(`v1/route-groups/${id}/routing`, groupRoutingSchema, { signal }) })
}

/** 节点生效路由的只读预览：节点私有 → 所在各组 → 全局合并后的结果，与下发给节点的同一口径 */
export function useEffectiveRouting(id: string) {
  const api = useApi()
  return useQuery({ queryKey: [...NK, 'effective-routing', id], queryFn: ({ signal }) => api.get(`v1/nodes/${id}/routing/effective`, effectiveRoutingSchema, { signal }) })
}

export function useInvalidateNodes() {
  const client = useQueryClient()
  return useCallback(() => client.invalidateQueries({ queryKey: NK }), [client])
}
