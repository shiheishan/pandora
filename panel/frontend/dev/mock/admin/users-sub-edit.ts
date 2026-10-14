import type { MockRoute } from '../types.ts'
import type { Sub, User } from './users.ts'

const QUOTA_MAX_BYTES = 10 * 1024 ** 4
const VALID_PERIODS = new Set(['total', 'cycle', 'day', 'month'])

function reasonProblem(v: unknown): string | null {
  if (typeof v !== 'string') return '请写清原因，5 到 500 个字'
  const n = [...v.trim()].length
  if (n < 5 || n > 500) return '请写清原因，5 到 500 个字'
  return null
}

function findSub(users: User[], subId: string): { user: User; sub: Sub } | null {
  for (const user of users) {
    const sub = user.subs.find((s) => s.id === subId)
    if (sub) return { user, sub }
  }
  return null
}

// ---------------------------------------------------------------------------
// 后台改单份订阅的到期时间与流量上限（与 subscription handler/admin_edit.go 同形）
// ---------------------------------------------------------------------------
export function subEditRoutes(store: { users: User[] }): Record<string, MockRoute> {
  return {
    'POST /v1/subscriptions/:id/period-end': async (ctx) => {
      if (!ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const hit = findSub(store.users, ctx.params.id)
      if (!hit) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      const { sub } = hit
      if (sub.status !== 'active') return ctx.fail(409, 'conflict', '只能改正在生效的订阅；已过期的请用「加时长」救回')
      const reasonErr = reasonProblem(body.reason)
      if (reasonErr) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { reason: reasonErr })

      const raw = typeof body.period_end === 'string' ? body.period_end : ''
      const t = Date.parse(raw)
      if (!raw || Number.isNaN(t)) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { period_end: '时间格式不正确' })
      const now = Date.now()
      const max = new Date(now)
      max.setFullYear(max.getFullYear() + 10)
      if (!(t > now) || t > max.getTime()) {
        return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { period_end: '到期时间须晚于现在，且不超过 10 年' })
      }

      const previous_end = sub.current_period_end ?? new Date(now).toISOString()
      const period_end = new Date(t).toISOString()
      sub.current_period_end = period_end
      ctx.send(200, {
        subscription_id: sub.id,
        user_id: hit.user.id,
        previous_end,
        period_end,
      })
    },

    'POST /v1/subscriptions/:id/quota': async (ctx) => {
      if (!ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const hit = findSub(store.users, ctx.params.id)
      if (!hit) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      const { sub } = hit
      if (sub.status !== 'active') return ctx.fail(409, 'conflict', '这一份已停用，不能再改')
      const reasonErr = reasonProblem(body.reason)
      if (reasonErr) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { reason: reasonErr })

      const period = typeof body.period === 'string' ? body.period : ''
      if (!VALID_PERIODS.has(period)) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { period: '配额周期不合法' })
      const metric = typeof body.metric === 'string' ? body.metric : ''
      if (metric !== 'traffic.bytes' || period !== 'cycle') {
        return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { metric: '这一份没有这个配额' })
      }

      const limit = body.limit_bytes
      if (limit !== null && !(typeof limit === 'number' && Number.isInteger(limit) && limit >= 0 && limit <= QUOTA_MAX_BYTES)) {
        return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { limit_bytes: '流量上限须在 0 到 10 TiB 之间，留空表示不限' })
      }

      sub.traffic_limit = limit as number | null
      ctx.send(200, { ok: true })
    },
  }
}
