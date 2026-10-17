import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'
import { useApi } from '../../../shell/runtime'
import { planPoolsSchema, planResponseSchema, plansSchema, poolOptionsSchema, type PlanDetail } from './schemas'

export * from './schemas'

// ---------------------------------------------------------------------------
// 查询：目录变化推 plans.changed（plans / plan_versions / prices）
// ---------------------------------------------------------------------------
export const PK = ['admin', 'plans'] as const

/**
 * 别的模块自己查 GET v1/plans（各收自己用得到的字段、各自一份 schema，出错范围只在那个模块）时用的键：
 * 挂在 PK 前缀下，套餐页写后 useInvalidatePlans 按前缀失效会一并刷新它们
 */
export const planOptionsKey = (module: string) => [...PK, 'options', module] as const
const topics = ['plans.changed'] as const

/** 全部套餐（不分页、含已归档）：类型页签的计数要全量，搜索与筛选在前端做 */
export function usePlans() {
  const api = useApi()
  return useQuery({
    queryKey: [...PK, 'list'],
    queryFn: ({ signal }) => api.get('v1/plans', plansSchema, { signal }).then((r) => r.plans),
    meta: { topics },
  })
}

const detailKey = (id: string | null) => [...PK, 'detail', id] as const

export function usePlan(id: string | null) {
  const api = useApi()
  return useQuery({
    queryKey: detailKey(id),
    queryFn: ({ signal }) => api.get(`v1/plans/${encodeURIComponent(id!)}`, planResponseSchema, { signal }).then((r) => r.plan),
    enabled: id !== null,
    meta: { topics },
  })
}

/** 详情「线路」页签的绑池选择器 */
export function usePlanPools(id: string | null) {
  const api = useApi()
  return useQuery({
    queryKey: [...PK, 'pools', id],
    queryFn: ({ signal }) => api.get(`v1/plans/${encodeURIComponent(id!)}/pools`, planPoolsSchema, { signal }).then((r) => r.pools),
    enabled: id !== null,
    meta: { topics },
  })
}

/** 新建弹窗的线路候选（还没有套餐 id）：只列未停用的池 */
export function usePoolOptions(enabled: boolean) {
  const api = useApi()
  return useQuery({
    queryKey: [...PK, 'pool-options'],
    queryFn: ({ signal }) => api.get('v1/node-pools', poolOptionsSchema, { signal }).then((r) => r.pools.filter((p) => p.status !== 'disabled')),
    enabled,
    staleTime: 60_000,
  })
}

/** 写成功后整个前缀重拉：列表的价格、版本号、节点数都可能跟着变 */
export function useInvalidatePlans() {
  const client = useQueryClient()
  return useCallback(() => client.invalidateQueries({ queryKey: PK }), [client])
}

/**
 * 写接口都回 {plan: PlanDetail}：先把详情缓存换成响应里的最新值（下一次写就带上新的 row_version），
 * 再按前缀失效让列表等跟上
 */
export function useApplyPlan() {
  const client = useQueryClient()
  return useCallback(
    (plan: PlanDetail) => {
      client.setQueryData(detailKey(plan.id), plan)
      return client.invalidateQueries({ queryKey: PK })
    },
    [client],
  )
}
