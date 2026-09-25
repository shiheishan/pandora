/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useQuery / useQueryClient / keepPreviousData，依赖 react 的 useCallback，依赖 zod，依赖 ../../../shell/runtime 的 useApi
 * [OUTPUT]: 对外提供用户模块的 zod schema 与类型（UserRow、UserDetail、SubscriptionRow、OrderRow、UserGroup、UserProfile 等）、读 hook（useUsers、useUser、useUserGroups、useUserProfile）、UK 查询键前缀与 useInvalidateUsers、写接口的响应 schema
 * [POS]: admin/screens/users 的数据层：形状照 api-contract.md 后台-03（含修订 R9 / R11 / R12 / R22），并按 domain/adminops/users.go、api/admin/profile.go、usergroup.go 的 json tag 核对；按保留规则 2，没有任何字段携带订阅令牌或订阅地址
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { keepPreviousData, useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'
import { z } from 'zod'
import { useApi } from '../../../shell/runtime'

// ---------------------------------------------------------------------------
// 封闭枚举（迁移里的 CHECK）：未知值判为不符约定
// ---------------------------------------------------------------------------
export const USER_STATUSES = ['pending', 'active', 'suspended', 'banned', 'deletion_scheduled', 'anonymized'] as const
export const RISK_LEVELS = ['trusted', 'normal', 'elevated', 'high'] as const
export const SUB_STATUSES = ['pending', 'trialing', 'active', 'past_due', 'grace', 'paused', 'cancelled', 'expired'] as const
export const ORDER_STATUSES = ['draft', 'pending_payment', 'processing', 'paid', 'fulfilled', 'cancelled', 'expired', 'refunded', 'partially_refunded'] as const
export const ORDER_KINDS = ['new', 'renewal', 'upgrade', 'downgrade', 'addon', 'topup', 'manual'] as const
export type UserStatus = (typeof USER_STATUSES)[number]
export type RiskLevel = (typeof RISK_LEVELS)[number]
export type SubStatus = (typeof SUB_STATUSES)[number]
export type OrderStatus = (typeof ORDER_STATUSES)[number]
export type OrderKind = (typeof ORDER_KINDS)[number]

const int = z.number().int()
const count = int.nonnegative()
const time = z.string()

// ---------------------------------------------------------------------------
// GET v1/users：列表行（current_subscription 是后端二补的当前订阅摘要）
// ---------------------------------------------------------------------------
const currentSubSchema = z.object({
  id: z.string(),
  plan_name: z.string(),
  status: z.enum(SUB_STATUSES),
  current_period_end: time.nullable(),
  traffic: z.object({ limit: int.nullable(), consumed: count }),
  // 生效值：COALESCE(订阅覆盖, 套餐 max_devices, 0)，0 表示不限
  device_limit: count,
  online_devices: count,
})

const userRowShape = {
  id: z.string(),
  email: z.string(),
  display_name: z.string().nullable(),
  status: z.enum(USER_STATUSES),
  risk_level: z.enum(RISK_LEVELS),
  group_name: z.string(),
  group_id: z.string().nullable(),
  created_at: time,
  last_login_at: time.nullable(),
  subscription_count: count,
  active_plan: z.string().nullable(),
  balance: int,
  currency: z.string(),
  current_subscription: currentSubSchema.nullable(),
}
const userRowSchema = z.object(userRowShape)
export const usersSchema = z.object({ users: z.array(userRowSchema), total: count })
export type UserRow = z.output<typeof userRowSchema>
export type CurrentSub = z.output<typeof currentSubSchema>

// ---------------------------------------------------------------------------
// GET v1/users/{id}：列表行 + 资料、全部订阅（含配额与设备）、最近 20 单、角色、统计
// （列表行里的 subscription_count / active_plan / current_subscription 在这里是零值，不用）
// ---------------------------------------------------------------------------
const quotaSchema = z.object({ metric: z.string(), limit: int.nullable(), consumed: count, remaining: int.nullable() })
const subscriptionSchema = z.object({
  id: z.string(),
  plan_name: z.string(),
  plan_version: int,
  status: z.enum(SUB_STATUSES),
  current_period_start: time.nullable(),
  current_period_end: time.nullable(),
  amount: int,
  currency: z.string(),
  auto_renew: z.boolean(),
  quotas: z.array(quotaSchema),
  device_limit_override: count.nullable(),
  plan_max_devices: count.nullable(),
  online_devices: count,
})
const orderRowSchema = z.object({
  id: z.string(),
  order_no: z.string(),
  user_email: z.string(),
  kind: z.enum(ORDER_KINDS),
  status: z.enum(ORDER_STATUSES),
  currency: z.string(),
  total_amount: int,
  payable_amount: int,
  paid_amount: int,
  refunded_amount: int,
  balance_applied: int,
  created_at: time,
  paid_at: time.nullable(),
  provider_code: z.string().nullable(),
  provider_name: z.string().nullable(),
  plan_name: z.string(),
  interval: z.string(),
  interval_count: int,
  item_count: int,
})
export const userDetailSchema = z.object({
  ...userRowShape,
  email_verified: z.boolean(),
  subscriptions: z.array(subscriptionSchema),
  recent_orders: z.array(orderRowSchema),
  roles: z.array(z.string()),
  stats: z.object({ paid_total: int, order_count: count, referral_count: count }),
  referrer: z.object({ id: z.string(), email: z.string() }).nullable(),
  telegram: z.object({ username: z.string(), bound_at: time }).nullable(),
})
export type UserDetail = z.output<typeof userDetailSchema>
export type SubscriptionRow = z.output<typeof subscriptionSchema>
export type OrderRow = z.output<typeof orderRowSchema>
export type Quota = z.output<typeof quotaSchema>

// ---------------------------------------------------------------------------
// GET v1/user-groups：筛选下拉与抽屉里的分配下拉
// ---------------------------------------------------------------------------
const userGroupSchema = z.object({
  id: z.string(),
  code: z.string(),
  name: z.string(),
  description: z.string(),
  users: count,
  plans: count,
  prices: count,
  coupons: count,
})
export const userGroupsSchema = z.object({ groups: z.array(userGroupSchema) })
export type UserGroup = z.output<typeof userGroupSchema>

// ---------------------------------------------------------------------------
// GET v1/users/{id}/profile（security.audit.read）：风控画像，明文 IP 只在这里
// ---------------------------------------------------------------------------
const profileSchema = z.object({
  events: z.array(z.object({ action: z.string(), outcome: z.string(), ip: z.string(), ua: z.string(), domain: z.string(), at: time })),
  ips: z.array(z.object({ ip: z.string(), count: count, first: time, last: time, accounts: count })),
  related: z.array(z.object({ id: z.string(), email: z.string() })),
  fetches: z.array(z.object({ ip: z.string(), ua: z.string(), family: z.string(), result: z.string(), format: z.string(), at: time })),
  fetch_sources_7d: count,
  registered_ip: z.string(),
})
export type UserProfile = z.output<typeof profileSchema>

// ---------------------------------------------------------------------------
// 写接口的响应
// ---------------------------------------------------------------------------
export const okSchema = z.object({ ok: z.literal(true) })
export const statusSetSchema = z.object({ ok: z.literal(true), status: z.enum(['active', 'suspended', 'banned']) })
export const passwordResetSchema = z.object({ ok: z.literal(true), sessions_revoked: z.literal(true) })
// R11 / 5.A D-B-1：换发后不回令牌，只有用户邮箱与旧链接已吊销
export const rotatedSchema = z.object({ user_email: z.string(), old_revoked: z.literal(true) }).strict()
export const balanceSchema = z.object({ balance: int })

// ---------------------------------------------------------------------------
// 查询
// ---------------------------------------------------------------------------
export const UK = ['admin', 'users'] as const
export const USERS_PAGE = 25

export interface UsersParams {
  q?: string
  status?: string
  group_id?: string
  sub_state?: 'active' | 'expired' | 'none'
  offset: number
}

export function useUsers(params: UsersParams) {
  const api = useApi()
  return useQuery({
    queryKey: [...UK, 'list', params],
    queryFn: ({ signal }) => api.get('v1/users', usersSchema, { signal, query: { ...params, limit: USERS_PAGE } }),
    meta: { topics: ['subscriptions.changed', 'orders.changed'] },
    placeholderData: keepPreviousData,
  })
}

export function useUser(id: string | null) {
  const api = useApi()
  return useQuery({
    queryKey: [...UK, 'detail', id],
    queryFn: ({ signal }) => api.get(`v1/users/${encodeURIComponent(id!)}`, userDetailSchema, { signal }),
    enabled: id !== null,
    meta: { topics: ['subscriptions.changed', 'orders.changed'] },
  })
}

export function useUserGroups() {
  const api = useApi()
  return useQuery({
    queryKey: [...UK, 'groups'],
    queryFn: ({ signal }) => api.get('v1/user-groups', userGroupsSchema, { signal }).then((r) => r.groups),
    staleTime: 5 * 60_000,
  })
}

export function useUserProfile(id: string, enabled: boolean) {
  const api = useApi()
  return useQuery({
    queryKey: [...UK, 'profile', id],
    queryFn: ({ signal }) => api.get(`v1/users/${encodeURIComponent(id)}/profile`, profileSchema, { signal }),
    enabled,
  })
}

/** 写成功后：列表与该用户详情重拉（分组人数变了时连带用户组） */
export function useInvalidateUsers() {
  const client = useQueryClient()
  return useCallback(
    (withGroups = false) =>
      Promise.all([
        client.invalidateQueries({ queryKey: [...UK, 'list'] }),
        client.invalidateQueries({ queryKey: [...UK, 'detail'] }),
        withGroups ? client.invalidateQueries({ queryKey: [...UK, 'groups'] }) : undefined,
      ]),
    [client],
  )
}
