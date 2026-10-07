import { z } from 'zod'

// 批量生成任务与加流量包的响应 schema：只依赖 zod，页面与假后端测试共用

const int = z.number().int()
const count = int.nonnegative()
const time = z.string()

// 批量生成是后台任务（adminops.UserGenerationJob，无 omitempty）：POST 回 202 与任务，
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

// POST v1/subscriptions/{id}/traffic-pack：billing.AdminTrafficGrantOutput（无 omitempty）
export const trafficGrantedSchema = z.object({
  subscription_id: z.string(),
  user_id: z.string(),
  user_email: z.string(),
  grant_id: z.string(),
  granted_bytes: int.positive(),
  remaining_bytes_total: int,
})
