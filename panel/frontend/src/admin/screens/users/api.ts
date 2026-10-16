import { keepPreviousData, useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'
import { z } from 'zod'
import { useApi } from '../../../shell/runtime'
import { orderRowSchema } from '../billing/schemas'
import { planOptionsKey } from '../plans/api'
import { exactEmail } from './model'
import { signupSettingsSchema } from './signupSettings'
import { bulkBanSchema, generationJobSchema, generationJobsSchema } from './opsSchemas'
export { bulkBanSchema }

// 订单的封闭枚举与列表行归订单与收款模块（后台-05），用户详情「最近订单」同形
export { type OrderRow, type OrderStatus } from '../billing/schemas'

// ---------------------------------------------------------------------------
// 封闭枚举（迁移里的 CHECK）：未知值判为不符约定
// ---------------------------------------------------------------------------
// deleted：后台注销并匿名化（迁移 00040），邮箱换成占位值，界面显示「已注销 #<短 id>」
const USER_STATUSES = ['pending', 'active', 'suspended', 'banned', 'deletion_scheduled', 'anonymized', 'deleted'] as const
const RISK_LEVELS = ['trusted', 'normal', 'elevated', 'high'] as const
const SUB_STATUSES = ['active', 'expired'] as const
export type UserStatus = (typeof USER_STATUSES)[number]
export type RiskLevel = (typeof RISK_LEVELS)[number]
export type SubStatus = (typeof SUB_STATUSES)[number]

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
  current_subscription: currentSubSchema.nullable(),
}
const userRowSchema = z.object(userRowShape)
export const usersSchema = z.object({ users: z.array(userRowSchema), total: count })
export type UserRow = z.output<typeof userRowSchema>

// ---------------------------------------------------------------------------
// GET v1/users/{id}：列表行 + 资料、全部订阅（含配额与设备）、最近 20 单、是否管理员、统计
// （列表行里的 subscription_count / active_plan / current_subscription 在这里是零值，不用）
// ---------------------------------------------------------------------------
// period：配额周期（total / cycle / day / month），后台改流量上限按「指标 + 周期」定位一行
const quotaSchema = z.object({ metric: z.string(), period: z.string(), limit: int.nullable(), consumed: count, remaining: int.nullable() })
const subscriptionSchema = z.object({
  id: z.string(),
  // 用户起的备注名（购买模型统一）：没起为 null；后台只读，改名是用户在用户端里做的事
  label: z.string().nullable(),
  plan_name: z.string(),
  plan_version: int,
  status: z.enum(SUB_STATUSES),
  current_period_start: time.nullable(),
  current_period_end: time.nullable(),
  amount: int,
  quotas: z.array(quotaSchema),
  device_limit_override: count.nullable(),
  plan_max_devices: count.nullable(),
  online_devices: count,
})
export const userDetailSchema = z.object({
  ...userRowShape,
  email_verified: z.boolean(),
  subscriptions: z.array(subscriptionSchema),
  recent_orders: z.array(orderRowSchema),
  is_admin: z.boolean(),
  // 10-16 起只有人民币：实收是一个数（分）
  stats: z.object({ paid_total: int, order_count: count, referral_count: count }),
  referrer: z.object({ id: z.string(), email: z.string() }).nullable(),
  telegram: z.object({ username: z.string(), bound_at: time }).nullable(),
  // 后台备注（只在后台详情出现）；没写为 null
  admin_note: z.string().nullable(),
  // 注销时间；没注销为 null
  deleted_at: time.nullable(),
})
export type UserDetail = z.output<typeof userDetailSchema>
export type SubscriptionRow = z.output<typeof subscriptionSchema>
export type Quota = z.output<typeof quotaSchema>

// ---------------------------------------------------------------------------
// GET v1/user-groups：用户组页、筛选下拉与各处的分配下拉
// ---------------------------------------------------------------------------
// 停用的组：分配下拉里隐藏（当前值照样显示并标「（已停用）」），不能再加新成员；已有成员不受影响
const userGroupSchema = z.object({
  id: z.string(),
  code: z.string(),
  name: z.string(),
  description: z.string(),
  status: z.enum(['active', 'disabled']),
  users: count,
  plans: count,
  coupons: count,
  // R104：把这个组列入「仅限用户组」名单的节点池（只读）；Go 的 nil 切片可能编成 null，归一成 []
  exclusive_pools: z
    .array(z.object({ id: z.string(), name: z.string() }))
    .nullable()
    .transform((v) => v ?? []),
})
export const userGroupsSchema = z.object({ groups: z.array(userGroupSchema) })
export type UserGroup = z.output<typeof userGroupSchema>

// ---------------------------------------------------------------------------
// GET v1/users/{id}/profile：风控画像，明文 IP 只在这里
// ---------------------------------------------------------------------------
export const profileSchema = z.object({
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
// POST v1/subscriptions/{id}/extend：billing.AdminExtendOutput（无 omitempty）
export const extendedSchema = z.object({
  subscription_id: z.string(),
  user_email: z.string(),
  days: int.positive(),
  previous_end: time,
  period_end: time,
})

export const groupSavedSchema = z.object({ id: z.string() })

// 后台改邮箱 / 备注 / 邀请人 / 注销（xb-reg）
export const emailSetSchema = z.object({ ok: z.literal(true), email: z.string() })
export const userDeletedSchema = z.object({ ok: z.literal(true), short_id: z.string(), terminated_subscriptions: count })
// POST v1/subscriptions/{id}/period-end：直接改到期时刻（只改生效中的）
export const periodEndSetSchema = z.object({ subscription_id: z.string(), user_id: z.string(), previous_end: time, period_end: time })
// POST v1/users/bulk/ban：skipped 的键是 self / admin / already_banned / deleted，只列出现过的

// ---------------------------------------------------------------------------
// 批量运营：预览 / 生成 / 群发（筛选字段直接放在请求体顶层；后端 DisallowUnknownFields，空值不传）
// ---------------------------------------------------------------------------
export interface BulkFilter {
  status?: 'active' | 'suspended' | 'banned'
  group_id?: string
  plan_id?: string
  expires_within_days?: number
  sub_state?: 'active' | 'expired' | 'none'
}
export const bulkPreviewSchema = z.object({
  total: count,
  samples: z.array(z.string()),
  sample_rows: z.array(z.object({ email: z.string(), plan_name: z.string().nullable(), current_period_end: time.nullable() })),
})
// 批量生成任务与加流量的 schema 在 opsSchemas.ts（不依赖 React，假后端测试也引用）
export { generationJobSchema, trafficGrantedSchema, type GenerationJob } from './opsSchemas'
/** 一个批量生成任务的进度：没结束时每 1.5 秒轮询一次，结束即停 */
export function useGenerationJob(id: string | null) {
  const api = useApi()
  return useQuery({
    queryKey: [...UK, 'generation-job', id],
    queryFn: ({ signal }) => api.get(`v1/users/bulk/generate/jobs/${encodeURIComponent(id!)}`, generationJobSchema, { signal }),
    enabled: id !== null,
    refetchInterval: (q) => (q.state.data && (q.state.data.status === 'succeeded' || q.state.data.status === 'failed') ? false : 1500),
  })
}

/** 最近的批量生成任务（最多 20 个，最新在前） */
export function useGenerationJobs() {
  const api = useApi()
  return useQuery({
    queryKey: [...UK, 'generation-jobs'],
    queryFn: ({ signal }) => api.get('v1/users/bulk/generate/jobs', generationJobsSchema, { signal }).then((r) => r.jobs),
  })
}
export const bulkMailSchema = z.object({ queued: count, skipped: count })

// ---------------------------------------------------------------------------
// GET v1/devices：生效中的订阅按在线数倒序，最多 200 条
// ---------------------------------------------------------------------------
const onlineDeviceSchema = z.object({
  subscription_id: z.string(),
  email: z.string(),
  plan: z.string(),
  // 生效值，0 表示不限
  limit: count,
  online: count,
  nodes: count,
  overridden: z.boolean(),
  exceeded: z.boolean(),
  last_seen_at: time.nullable(),
})
// R103：设备识别窗口只有这四档（分钟），缺行按 5
export const DEVICE_WINDOWS = [5, 10, 30, 60] as const
export type DeviceWindow = (typeof DEVICE_WINDOWS)[number]
export const devicesSchema = z.object({
  devices: z.array(onlineDeviceSchema),
  mode: z.enum(['loose', 'strict']),
  grace: count,
  window_minutes: z.union([z.literal(5), z.literal(10), z.literal(30), z.literal(60)]),
})
export type OnlineDevice = z.output<typeof onlineDeviceSchema>
export type DeviceMode = z.output<typeof devicesSchema>['mode']

// POST v1/subscriptions/{id}/traffic-reset：清掉的字节数
export const resetDoneSchema = z.object({ reset: z.literal(true), freed_bytes: count })

// ---------------------------------------------------------------------------
// GET v1/plans：批量筛选的「套餐」下拉只要 id / 名称 / 状态
// ---------------------------------------------------------------------------
export const planOptionsSchema = z.object({ plans: z.array(z.object({ id: z.string(), name: z.string(), status: z.enum(['draft', 'active', 'archived']) })) })

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

export function useUserProfile(id: string) {
  const api = useApi()
  return useQuery({
    queryKey: [...UK, 'profile', id],
    queryFn: ({ signal }) => api.get(`v1/users/${encodeURIComponent(id)}/profile`, profileSchema, { signal }),
  })
}

/**
 * 按邮箱找人（设备策略只有 subscription_id + email、手动重置只填邮箱）：GET v1/users?q= 是模糊匹配，
 * 取邮箱完全相等的那条；找不到返回 null
 */
export function useFindUserByEmail() {
  const api = useApi()
  return useCallback(
    async (email: string) => {
      const r = await api.get('v1/users', usersSchema, { query: { q: email.trim(), limit: 100, offset: 0 } })
      return exactEmail(r.users, email) ?? null
    },
    [api],
  )
}

export function usePlanOptions() {
  const api = useApi()
  return useQuery({
    // 挂在套餐的键前缀下：套餐页写后按前缀失效会一并刷新；别的管理员改了套餐靠 plans.changed
    queryKey: planOptionsKey('users'),
    queryFn: ({ signal }) => api.get('v1/plans', planOptionsSchema, { signal }).then((r) => r.plans),
    staleTime: 5 * 60_000,
    meta: { topics: ['plans.changed'] },
  })
}

/** 预览是只读的 POST：按筛选条件做查询键，条件变了自动重拉 */
export function useBulkPreview(filter: BulkFilter) {
  const api = useApi()
  return useQuery({
    queryKey: [...UK, 'bulk', filter],
    queryFn: ({ signal }) => api.post('v1/users/bulk/preview', bulkPreviewSchema, { signal, body: filter }),
    placeholderData: keepPreviousData,
  })
}

export function useDevices() {
  const api = useApi()
  return useQuery({
    queryKey: [...UK, 'devices'],
    queryFn: ({ signal }) => api.get('v1/devices', devicesSchema, { signal }),
    // 在线数是 5 分钟窗口，一分钟刷一次足够
    refetchInterval: 60_000,
  })
}

/** 写成功后：列表、详情与设备策略重拉（分组人数变了时连带用户组） */
export function useInvalidateUsers() {
  const client = useQueryClient()
  return useCallback(
    (withGroups = false) =>
      Promise.all([
        client.invalidateQueries({ queryKey: [...UK, 'list'] }),
        client.invalidateQueries({ queryKey: [...UK, 'detail'] }),
        client.invalidateQueries({ queryKey: [...UK, 'devices'] }),
        client.invalidateQueries({ queryKey: [...UK, 'bulk'] }),
        withGroups ? client.invalidateQueries({ queryKey: [...UK, 'groups'] }) : undefined,
      ]),
    [client],
  )
}

// GET v1/settings/signup：SignupGrowth（营销）与 SignupSecurity（安全）两个组件共用
export function useSignupSettings() {
  const api = useApi()
  return useQuery({
    queryKey: [...UK, 'signup-settings'],
    queryFn: ({ signal }) => api.get('v1/settings/signup', signupSettingsSchema, { signal }),
  })
}
