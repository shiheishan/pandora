/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useQuery / useQueryClient，依赖 react 的 useCallback，依赖 ../../../shell/runtime 的 useApi，依赖 ./schemas 的 schema
 * [OUTPUT]: 对外提供读 hook（usePlans、usePlan、usePlanPools、usePoolOptions、useTrafficPacks）、PK 查询键前缀与 useInvalidatePlans，并转出 ./schemas 的全部 schema 与类型
 * [POS]: admin/screens/plans 的数据层：读只经 react-query + core/api，目录相关查询挂 plans.changed（plans / plan_versions / prices，R33 起含流量包），写后按 PK 前缀整体失效；页面只从这里取 schema 与 hook
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'
import { useApi } from '../../../shell/runtime'
import { packsSchema, planPoolsSchema, planResponseSchema, plansSchema, poolOptionsSchema, type PackStatus } from './schemas'

export * from './schemas'

// ---------------------------------------------------------------------------
// 查询：目录变化推 plans.changed（plans / plan_versions / prices，R33 起含流量包）
// ---------------------------------------------------------------------------
export const PK = ['admin', 'plans'] as const
const topics = ['plans.changed'] as const

export function usePlans() {
  const api = useApi()
  return useQuery({
    queryKey: [...PK, 'list'],
    queryFn: ({ signal }) => api.get('v1/plans', plansSchema, { signal }).then((r) => r.plans),
    meta: { topics },
  })
}

export function usePlan(id: string | null) {
  const api = useApi()
  return useQuery({
    queryKey: [...PK, 'detail', id],
    queryFn: ({ signal }) => api.get(`v1/plans/${encodeURIComponent(id!)}`, planResponseSchema, { signal }).then((r) => r.plan),
    enabled: id !== null,
    meta: { topics },
  })
}

/** 向导新建时没有套餐 id：传 null 不取 */
export function usePlanPools(id: string | null) {
  const api = useApi()
  return useQuery({
    queryKey: [...PK, 'pools', id],
    queryFn: ({ signal }) => api.get(`v1/plans/${encodeURIComponent(id!)}/pools`, planPoolsSchema, { signal }),
    enabled: id !== null,
    meta: { topics },
  })
}

/** 只在新建向导的「可用线路」一步、且有 node.read 时才取 */
export function usePoolOptions(enabled: boolean) {
  const api = useApi()
  return useQuery({
    queryKey: [...PK, 'pool-options'],
    queryFn: ({ signal }) => api.get('v1/node-pools', poolOptionsSchema, { signal }).then((r) => r.pools.filter((p) => p.status !== 'disabled')),
    enabled,
    staleTime: 60_000,
  })
}

export function useTrafficPacks(status: PackStatus | '') {
  const api = useApi()
  return useQuery({
    queryKey: [...PK, 'packs', status],
    queryFn: ({ signal }) => api.get('v1/traffic-packs', packsSchema, { signal, query: { status: status || undefined } }).then((r) => r.packs),
    meta: { topics },
  })
}

/** 写成功后整个前缀重拉：列表卡片的起价、版本号、节点数都可能跟着变 */
export function useInvalidatePlans() {
  const client = useQueryClient()
  return useCallback(() => client.invalidateQueries({ queryKey: PK }), [client])
}
