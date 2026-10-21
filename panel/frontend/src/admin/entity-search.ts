// ---------------------------------------------------------------------------
// ⌘K 实体搜索（10-16）：有查询时并行查 v1/users、v1/orders、v1/nodes 的列表
// 接口（都支持 q），各取前 4 条，给 CommandPalette 追加「用户 / 订单 / 节点」三组。
//
// - 防抖：输入停止 200ms 后才发请求，免得每敲一个字发一次。
// - 取消过期请求：queryFn 把 react-query 的 signal 原样传给 api.get，搜索词变了、
//   旧查询没人用时（enabled 翻转或 key 变化）由 react-query 用它 abort。
// - 失败静默：某一组查询出错就不显示那一组，不报错、不阻塞其他组。
// - 每组最多 ENTITY_LIMIT 条，搜索词截到 ENTITY_QUERY_MAX（后端 users 的 q 上限
//   200、nodes 120，这里统一收紧到 100）。
// - 不 import admin/screens/**：外框（⌘K 在 Shell）不能把模块页面拉进主包，
//   所以这里写一份最小 schema 与状态文案；口径与各模块的 model.ts 一致，
//   那边改了这边要跟着改。
// ---------------------------------------------------------------------------
import { useQueries } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { z } from 'zod'
import { formatMoney } from '../core/format'
import { useApi } from '../shell/runtime'
import type { PaletteGroup, PaletteItem } from './modules'

/** 每组最多几条 */
export const ENTITY_LIMIT = 4
export const ENTITY_DEBOUNCE_MS = 200
/** 搜索词上限（后端 users 的 q 上限 200、nodes 120），超出截断 */
export const ENTITY_QUERY_MAX = 100

/** 实体候选：比 PaletteItem 多一个完整地址，选中后直接打开那一条 */
export interface EntityItem extends PaletteItem {
  path: string
}
export const isEntityItem = (item: PaletteItem): item is EntityItem =>
  typeof (item as Partial<EntityItem>).path === 'string'

// ---------------------------------------------------------------------------
// 最小 schema：只取列表行里用到的字段（z.object 默认丢弃多余字段），
// 形状照各模块 schemas 的 Go 口径写严。
// ---------------------------------------------------------------------------
export const entityUsersSchema = z.object({
  users: z.array(
    z.object({
      id: z.string(),
      email: z.string(),
      status: z.string(),
      current_subscription: z
        .object({ plan_name: z.string(), status: z.string() })
        .nullable(),
    }),
  ),
})

export const entityOrdersSchema = z.object({
  orders: z.array(
    z.object({
      id: z.string(),
      order_no: z.string(),
      user_email: z.string(),
      status: z.string(),
      total_amount: z.number().int(),
    }),
  ),
})

/** 后端列表 nil 切片回 null，归一成 []（与各模块 schemas 同一口径） */
export const entityNodesSchema = z.object({
  nodes: z
    .array(
      z.object({
        id: z.string(),
        name: z.string(),
        serving_status: z.string(),
        stale: z.boolean(),
        node_type: z.string().nullable(),
        server_name: z.string().nullable(),
      }),
    )
    .nullable()
    .transform((v) => v ?? []),
})

// ---------------------------------------------------------------------------
// 状态文案：与 users/model.ts、nodes/logic.ts 的标签一致，不认得的值原样显示
// ---------------------------------------------------------------------------
const USER_STATUS_TEXT: Record<string, string> = {
  pending: '待验证',
  suspended: '已禁用',
  banned: '已禁用',
  deletion_scheduled: '注销中',
  anonymized: '已匿名',
  deleted: '已注销',
}

const SUB_STATUS_TEXT: Record<string, string> = {
  active: '生效中',
  expired: '已过期',
}

/**
 * 订单状态文案：与 users/model.ts 的 ORDER_STATUS_VIEW 逐条一致（超时未付写「超时取消」，报告 2.3-13；
 * 外框不能 import screens/**，守卫在 entity-search.test.ts）。实时动态（events-lookup.ts）也用这一份
 */
export const ORDER_STATUS_TEXT: Readonly<Record<string, string>> = {
  pending_payment: '待支付',
  paid: '已支付',
  fulfilled: '已支付',
  cancelled: '已取消',
  expired: '超时取消',
}

/** serving_status + stale → 状态文案（照 nodes/logic.ts 的 nodeState） */
function nodeStatusText(servingStatus: string, stale: boolean): string {
  if (servingStatus === 'active') return stale ? '离线' : '在线'
  if (servingStatus === 'draft') return '草稿'
  if (servingStatus === 'disabled') return '已停用'
  if (servingStatus === 'retired') return '已退役'
  return servingStatus
}

type EntityUser = z.output<typeof entityUsersSchema>['users'][number]
type EntityOrder = z.output<typeof entityOrdersSchema>['orders'][number]
type EntityNode = z.output<typeof entityNodesSchema>['nodes'][number]

// ---------------------------------------------------------------------------
// 纯函数：行 → 候选。都先截到前 4 条，标题一律 mono、路径里的 id 编码。
// ---------------------------------------------------------------------------
export function userItems(rows: EntityUser[]): EntityItem[] {
  return rows.slice(0, ENTITY_LIMIT).map((row) => {
    const parts: string[] = []
    const statusText = USER_STATUS_TEXT[row.status] ?? row.status
    // 非正常用户先给用户状态；active 不写（正常态不用占字）
    if (row.status !== 'active') parts.push(statusText)
    if (row.current_subscription) {
      const subStatus = SUB_STATUS_TEXT[row.current_subscription.status] ?? row.current_subscription.status
      parts.push(`${row.current_subscription.plan_name} · ${subStatus}`)
    } else {
      parts.push('无订阅')
    }
    return {
      key: `u:${row.id}`,
      kind: '用户',
      title: row.email,
      sub: parts.join(' · '),
      mono: true,
      module: 'users' as const,
      tab: 'list',
      path: `/users/list/${encodeURIComponent(row.id)}`,
    }
  })
}

export function orderItems(rows: EntityOrder[]): EntityItem[] {
  return rows.slice(0, ENTITY_LIMIT).map((row) => ({
    key: `o:${row.id}`,
    kind: '订单',
    title: row.order_no,
    sub: `${formatMoney(row.total_amount)} · ${ORDER_STATUS_TEXT[row.status] ?? row.status} · ${row.user_email}`,
    mono: true,
    module: 'billing' as const,
    tab: 'orders',
    path: `/billing/orders/${encodeURIComponent(row.id)}`,
  }))
}

export function nodeItems(rows: EntityNode[]): EntityItem[] {
  return rows.slice(0, ENTITY_LIMIT).map((row) => ({
    key: `n:${row.id}`,
    kind: '节点',
    title: row.name,
    sub: [
      row.node_type ?? '未设协议',
      row.server_name ?? '未绑定服务器',
      nodeStatusText(row.serving_status, row.stale),
    ].join(' · '),
    mono: true,
    module: 'nodes' as const,
    tab: 'nodes',
    path: `/nodes/nodes/${encodeURIComponent(row.id)}`,
  }))
}

/** 三组按 用户、订单、节点 顺序排，没条目的组（含 undefined / 出错的组）跳过 */
export function entityGroups(results: {
  users?: EntityItem[]
  orders?: EntityItem[]
  nodes?: EntityItem[]
}): PaletteGroup[] {
  const groups: PaletteGroup[] = []
  if (results.users?.length) groups.push({ label: '用户', items: results.users })
  if (results.orders?.length) groups.push({ label: '订单', items: results.orders })
  if (results.nodes?.length) groups.push({ label: '节点', items: results.nodes })
  return groups
}

export function normalizeEntityQuery(q: string): string {
  return q.trim().slice(0, ENTITY_QUERY_MAX)
}

// ---------------------------------------------------------------------------
// hook：防抖 + 三个并行查询。
// useDebounced 照 portal/screens/common/useDebounced.ts 抄一份私有的——
// 门户与后台不互相 import。
// ---------------------------------------------------------------------------
function useDebounced<T>(value: T, ms: number): T {
  const [settled, setSettled] = useState(value)
  useEffect(() => {
    const timer = setTimeout(() => setSettled(value), ms)
    return () => clearTimeout(timer)
  }, [value, ms])
  return settled
}

export function useEntitySearch(
  query: string,
  enabled: boolean,
): { groups: PaletteGroup[]; pending: boolean } {
  const api = useApi()
  const want = enabled ? normalizeEntityQuery(query) : ''
  const term = useDebounced(want, ENTITY_DEBOUNCE_MS)
  const active = enabled && term !== ''

  const [usersRes, ordersRes, nodesRes] = useQueries({
    queries: [
      {
        queryKey: ['admin', 'palette', 'users', term],
        queryFn: ({ signal }: { signal: AbortSignal }) =>
          api.get('v1/users', entityUsersSchema, { signal, query: { q: term, limit: ENTITY_LIMIT, offset: 0 } }),
        enabled: active,
        retry: false,
        staleTime: 30_000,
      },
      {
        queryKey: ['admin', 'palette', 'orders', term],
        queryFn: ({ signal }: { signal: AbortSignal }) =>
          api.get('v1/orders', entityOrdersSchema, { signal, query: { q: term, limit: ENTITY_LIMIT, offset: 0 } }),
        enabled: active,
        retry: false,
        staleTime: 30_000,
      },
      {
        queryKey: ['admin', 'palette', 'nodes', term],
        queryFn: ({ signal }: { signal: AbortSignal }) =>
          api.get('v1/nodes', entityNodesSchema, { signal, query: { q: term, limit: ENTITY_LIMIT, offset: 0 } }),
        enabled: active,
        retry: false,
        staleTime: 30_000,
      },
    ],
  })

  const groups = active
    ? entityGroups({
        // 出错的组传 undefined（静默不显示）；还没拿到数据的组也是 undefined
        users: usersRes.isError || !usersRes.data ? undefined : userItems(usersRes.data.users),
        orders: ordersRes.isError || !ordersRes.data ? undefined : orderItems(ordersRes.data.orders),
        nodes: nodesRes.isError || !nodesRes.data ? undefined : nodeItems(nodesRes.data.nodes),
      })
    : []

  // 还在搜：防抖没追上输入，或某个查询在请求中且还没有数据
  const pending =
    enabled &&
    want !== '' &&
    (want !== term || [usersRes, ordersRes, nodesRes].some((r) => r.isFetching && !r.data))

  return { groups, pending }
}
