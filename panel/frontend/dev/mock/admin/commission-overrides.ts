import type { Json, MockContext, MockModule } from '../types.ts'
import { userStore } from './users.ts'

// 按用户单独的佣金设置（与 Go 的 commission overrides 一致）：不给或给 null 的项跟全局走，再设整行覆盖。

/** 与后端 DisallowUnknownFields 一致：多一个字段就 400 */
function unknownField(body: Json, allowed: readonly string[]): string | null {
  return Object.keys(body).find((k) => !allowed.includes(k)) ?? null
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const int = (v: unknown) => (typeof v === 'number' && Number.isInteger(v) ? v : null)
const text = (v: unknown) => (typeof v === 'string' ? v : '')

const seedUser = userStore.find((u) => !u.is_admin)!

const overrides = new Map<
  string,
  {
    user_id: string
    email: string
    rate_percent: number | null
    scope: 'first_order' | 'every_order' | null
    updated_at: string
  }
>()

overrides.set(seedUser.id, {
  user_id: seedUser.id,
  email: seedUser.email,
  rate_percent: 15,
  scope: null,
  updated_at: '2026-10-01T08:00:00Z',
})

function overrideView(row: { user_id: string; email: string; rate_percent: number | null; scope: 'first_order' | 'every_order' | null; updated_at: string }) {
  return {
    user_id: row.user_id,
    email: row.email,
    rate_percent: row.rate_percent,
    scope: row.scope,
    updated_at: row.updated_at,
  }
}

export const commissionOverrides: MockModule = {
  routes: {
    'GET /v1/commission/overrides': (ctx) => {
      const rows = [...overrides.values()].sort((a, b) => b.updated_at.localeCompare(a.updated_at)).map(overrideView)
      ctx.send(200, { overrides: rows })
    },
    'POST /v1/commission/overrides': async (ctx) => {
      if (!ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const extra = unknownField(body, ['email', 'rate_percent', 'scope'])
      if (extra) return ctx.fail(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
      const email = text(body.email).trim()
      const fields: Record<string, string> = {}
      if (email === '') fields.email = '请填写用户邮箱'
      // 不给与给 null 同义：这一项跟全局走
      const rate = body.rate_percent === undefined || body.rate_percent === null ? null : int(body.rate_percent)
      const scope = body.scope === undefined || body.scope === null ? null : text(body.scope)
      if (body.rate_percent !== undefined && body.rate_percent !== null && (rate === null || rate < 0 || rate > 50)) fields.rate_percent = '佣金比例需在 0 到 50 之间'
      if (scope !== null && scope !== 'first_order' && scope !== 'every_order') fields.scope = '计佣范围只能是 first_order 或 every_order'
      if (rate === null && scope === null && !fields.rate_percent && !fields.scope) fields.rate_percent = '比例与计佣范围至少设一项；都跟全局走就删除这条设置'
      if (Object.keys(fields).length) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', fields)
      const user = userStore.find((u) => u.email.toLowerCase() === email.toLowerCase())
      if (!user) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { email: '找不到这个邮箱的用户' })
      // 与后端 UpsertOverride 一致：整行覆盖，不与旧值合并
      const row = {
        user_id: user.id,
        email: user.email,
        rate_percent: rate,
        scope: scope as 'first_order' | 'every_order' | null,
        updated_at: new Date().toISOString(),
      }
      overrides.set(user.id, row)
      ctx.send(200, { override: overrideView(row) })
    },
    'DELETE /v1/commission/overrides/:user_id': async (ctx) => {
      if (!ctx.requireReauth()) return
      const id = ctx.params.user_id!
      if (!UUID.test(id) || !overrides.has(id)) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      overrides.delete(id)
      ctx.send(200, { ok: true })
    },
  },
}
