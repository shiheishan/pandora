import { z } from 'zod'
import { orderRowSchema } from '../billing/schemas'

// 用户列表行、用户详情与重置流量回执的 schema：只依赖 zod 与 billing/schemas（同样是纯 zod），页面与假后端测试（node 侧）共用；api.ts 原样转出

// ---------------------------------------------------------------------------
// 封闭枚举（迁移里的 CHECK）：未知值判为不符约定
// ---------------------------------------------------------------------------
// deleted：后台注销并匿名化（迁移 00040），邮箱换成占位值，界面显示「已注销 #<短 id>」
const USER_STATUSES = ['pending', 'active', 'suspended', 'banned', 'deletion_scheduled', 'anonymized', 'deleted'] as const
const RISK_LEVELS = ['trusted', 'normal', 'elevated', 'high'] as const
const SUB_STATUSES = ['active', 'expired'] as const
export type UserStatus = (typeof USER_STATUSES)[number]
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
  // 注册试用发的那一份（P13，用户 10-21 定）
  is_trial: z.boolean(),
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
export const quotaSchema = z.object({ metric: z.string(), period: z.string(), limit: int.nullable(), consumed: count, remaining: int.nullable() })
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
  // 注册试用发的那一份（P13，用户 10-21 定）
  is_trial: z.boolean(),
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

// POST v1/subscriptions/{id}/traffic-reset 的回执
export const resetDoneSchema = z.object({ reset: z.literal(true), freed_bytes: count })

// ---------------------------------------------------------------------------
// GET v1/users/{id}/profile：风控画像，明文 IP 只在这里
// ---------------------------------------------------------------------------
const profileRisk = z.enum(['', 'low', 'mid', 'high'])
export const profileSchema = z.object({
  events: z.array(z.object({ action: z.string(), outcome: z.string(), ip: z.string(), ua: z.string(), domain: z.string(), at: time })),
  ips: z.array(
    z.object({
      ip: z.string(),
      count: count,
      first: time,
      last: time,
      accounts: count,
      geo: z.string(),
      network_kind: z.string(),
      risk: profileRisk,
    }),
  ),
  related: z.array(z.object({ id: z.string(), email: z.string() })),
  fetches: z.array(z.object({ ip: z.string(), ua: z.string(), family: z.string(), result: z.string(), format: z.string(), at: time })),
  fetch_sources_7d: count,
  registered_ip: z.string(),
  /** 各 IP 风险里最高的一档（与风控聚类同一条规则）；空串 = 没有和别的账号共用 IP */
  risk: profileRisk,
})
export type ProfileRisk = z.output<typeof profileRisk>
type UserProfile = z.output<typeof profileSchema>
/** 画像 IP 表里只用到时间与去重时的字段（测试夹具不必带 geo / network_kind / risk） */
export type ProfileIpActivity = Pick<UserProfile['ips'][number], 'ip' | 'count' | 'first' | 'last' | 'accounts'>
