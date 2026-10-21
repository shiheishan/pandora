import { z } from 'zod'

// GET v1/me/subscriptions 的 schema（纯 zod，不依赖运行时）：库里只有这两态（00011_subscription.sql），外框 queries.ts 转出，tests/ 的假后端测试也直接用它。
// 字段与 Go subscription/handler 的 mySubscriptionResponse / quotaResponse 一一对应，都没有 omitempty：指针字段为 null 而不缺席。
const SUBSCRIPTION_STATUSES = ['active', 'expired'] as const

const quotaSchema = z.object({
  metric: z.string(),
  limit: z.number().int().nullable(),
  /** 送的流量（礼品卡、后台加流量、盲盒）：订阅套餐只在本期有效，一次性套餐加在总量上 */
  bonus_bytes: z.number().int(),
  consumed: z.number().int(),
  remaining: z.number().int().nullable(),
  /** cycle（订阅套餐，每期清零）/ total（一次性套餐，不清零）/ day / month */
  period: z.string(),
  period_start: z.string(),
  period_end: z.string().nullable(),
})

/** 一份订阅按套餐版本给的用量（Go usageResponse）：三项 null 分别是不限流量、不限台数、不限速 */
const usageSchema = z.object({
  /** 订阅套餐每期 / 一次性套餐每次购买的流量 */
  traffic_bytes: z.number().int().nullable(),
  /** 生效的设备上限（覆盖优先，否则版本的设备数），口径同 device_limit */
  device_limit: z.number().int().nullable(),
  throttle_kbps: z.number().int().nullable(),
})

const renewalPriceSchema = z.object({
  id: z.string(),
  unit_amount: z.number().int(),
  billing_interval: z.string(),
  interval_count: z.number().int(),
  available: z.boolean(),
})

export const subscriptionSchema = z.object({
  id: z.string(),
  plan_id: z.string(),
  price_id: z.string(),
  plan_name: z.string(),
  plan_kind: z.enum(['subscription', 'one_time']),
  plan_version: z.number().int(),
  status: z.enum(SUBSCRIPTION_STATUSES),
  /** live 生效 / revivable 过期 30 天内还能救回 / ended 停用 */
  state: z.string(),
  current_period_start: z.string().nullable(),
  current_period_end: z.string().nullable(),
  amount: z.number().int(),
  /** 一次性套餐买的那档的有效天数；null = 永久或订阅套餐 */
  validity_days: z.number().int().nullable(),
  quotas: z.array(quotaSchema),
  device_limit: z.number().int().nullable(),
  online_devices: z.number().int(),
  /** 流量配额行的上限（订阅套餐本期 / 一次性套餐总量，不含送的）；null = 不限。能用的 = 上限 + 送的 − 已用 */
  traffic_limit: z.number().int().nullable(),
  /** 送的流量：订阅套餐只在本期有效，一次性套餐一直有效 */
  bonus_bytes: z.number().int(),
  traffic_used: z.number().int(),
  /** 一次性套餐买的是永久：current_period_end 是远期哨兵，不显示日期，写「永久」 */
  permanent: z.boolean(),
  /** 付费流量重置：本期（一次性为有效期内）已用次数、上限（null = 不限）、每次价格（null = 没开） */
  resets_used: z.number().int(),
  reset_limit: z.number().int().nullable(),
  reset_price: z.number().int().nullable(),
  /** 下次流量清零（订阅套餐本期末）；一次性套餐为 null */
  next_reset_at: z.string().nullable(),
  /** 能续费：没停用、订阅套餐、套餐没归档且允许续费（一次性永远 false） */
  renewable: z.boolean(),
  /** 能叠加：没停用、一次性套餐、套餐在售且接受新购 */
  stackable: z.boolean(),
  renewal_price: renewalPriceSchema.nullable(),
  /** 用户起的备注名（subscriptions.label），没起为 null */
  label: z.string().nullable(),
  /** App 里的配置名：「站点名 · 备注名」，没有备注名用套餐名（与下载头同源，都是订阅模块的 render.ProfileName） */
  client_name: z.string(),
  /** 能付费换套餐（没停用）：可跨类型换 */
  changeable: z.boolean(),
  /** 按续费价续一期会到哪天：生效中的从当前到期日起算，过期的从现在起算；不能续时为 null */
  renew_until: z.string().nullable(),
  /** 这一份锁定版本的用量（选购页「你在用」卡显示它，不显示目录里的新版本） */
  usage: usageSchema,
  /** 下个周期会用的用量：只有订阅套餐、套餐发布了新版本、且和 usage 有不同时才有；一次性套餐恒为 null */
  next_usage: usageSchema.nullable(),
  /** 注册试用发的那一份（续过费仍算） */
  is_trial: z.boolean(),
})
export type Subscription = z.output<typeof subscriptionSchema>

export const subscriptionsSchema = z.object({ subscriptions: z.array(subscriptionSchema) })
