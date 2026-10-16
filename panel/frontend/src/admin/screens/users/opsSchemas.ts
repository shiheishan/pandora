import { z } from 'zod'

// 批量生成任务与加流量的响应 schema：只依赖 zod，页面与假后端测试共用

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

// POST v1/subscriptions/{id}/traffic-pack（路径沿用旧名）：后台加流量，送的量订阅套餐当期有效、一次性加到总量上。
// bonus_bytes 是加完后这一份本期送的量合计
export const trafficGrantedSchema = z.object({
  subscription_id: z.string(),
  user_id: z.string(),
  user_email: z.string(),
  granted_bytes: int.positive(),
  bonus_bytes: int.nonnegative(),
})

// 按筛选批量封禁（POST v1/users/bulk/ban）：封了几人、跳过的按原因计数
export const bulkBanSchema = z.object({ banned: count, skipped: z.record(z.string(), count) })
