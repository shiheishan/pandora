import { z } from 'zod'

// 批量生成任务与加流量包的响应 schema：只依赖 zod，页面与假后端测试共用

const int = z.number().int()
const count = int.nonnegative()
const time = z.string()

// 批量生成是后台任务（identity/service GenerationJob，无 omitempty）：POST 回 202 与任务，
// 进度轮询 GET v1/users/bulk/generate/jobs/{id}，结果（含初始口令）从 …/result 下载 CSV
export const generationJobSchema = z.object({
  id: z.string(),
  actor_id: z.string(),
  status: z.enum(['queued', 'running', 'succeeded', 'failed']),
  total: count,
  completed: count,
  failed: count,
  email_prefix: z.string(),
  email_domain: z.string(),
  group_id: z.string().nullable(),
  reason: z.string(),
  error: z.string().nullable(),
  result_available: z.boolean(),
  result_expires_at: time.nullable(),
  created_at: time,
  started_at: time.nullable(),
  finished_at: time.nullable(),
})
export type GenerationJob = z.output<typeof generationJobSchema>
export const generationJobsSchema = z.object({ jobs: z.array(generationJobSchema) })

// POST v1/subscriptions/{id}/traffic-pack：billing.AdminTrafficGrantOutput（无 omitempty）。
// 流量包挂在这一份订阅上；remaining_bytes_total 的字段名没改，口径改成「这份订阅的流量包余量」
export const trafficGrantedSchema = z.object({
  subscription_id: z.string(),
  user_id: z.string(),
  user_email: z.string(),
  grant_id: z.string(),
  granted_bytes: int.positive(),
  remaining_bytes_total: int,
})

// 按筛选批量封禁（POST v1/users/bulk/ban）：封了几人、跳过的按原因计数
export const bulkBanSchema = z.object({ banned: count, skipped: z.record(z.string(), count) })

// 会员折扣：单个用户的会员减免百分比（10 表示九折）；页面与假后端测试共用
export const memberDiscountSchema = z.object({
  user_id: z.string(),
  percent: z.number().int().min(1).max(100).nullable(),
  updated_at: z.string().nullable(),
})
export type MemberDiscountData = z.output<typeof memberDiscountSchema>
