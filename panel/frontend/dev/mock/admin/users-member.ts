import type { MockModule } from '../types.ts'
import { userStore } from './users.ts'

interface MemberDiscount {
  percent: number
  updated_at: string
}

const store = new Map<string, MemberDiscount>()

export const memberDiscounts: MockModule = {
  routes: {
    'GET /v1/member-discounts/:id': (ctx) => {
      const userId = ctx.params.id ?? ''
      const user = userStore.find((u) => u.id === userId)
      if (!user) return ctx.fail(404, 'not_found', '资源不存在或无权访问')

      const record = store.get(userId)
      ctx.send(200, {
        user_id: userId,
        percent: record?.percent ?? null,
        updated_at: record?.updated_at ?? null,
      })
    },

    'PUT /v1/member-discounts/:id': async (ctx) => {
      if (!ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')

      const extra = Object.keys(body).find((k) => k !== 'percent')
      if (extra) return ctx.fail(400, 'bad_request', `请求体包含未知字段 "${extra}"`)

      const userId = ctx.params.id ?? ''
      const user = userStore.find((u) => u.id === userId)
      if (!user) return ctx.fail(404, 'not_found', '资源不存在或无权访问')

      const percent = body.percent
      if (typeof percent !== 'number' || !Number.isInteger(percent) || percent < 1 || percent > 100) {
        return ctx.fail(422, 'validation_failed', '请求参数校验未通过', {
          percent: '请填 1 到 100 的整数（10 表示打九折）',
        })
      }

      const updated_at = new Date().toISOString()
      const record = { percent, updated_at }
      store.set(userId, record)

      ctx.send(200, {
        user_id: userId,
        percent: record.percent,
        updated_at: record.updated_at,
      })
    },

    'DELETE /v1/member-discounts/:id': async (ctx) => {
      if (!ctx.requireReauth()) return
      const userId = ctx.params.id ?? ''
      const user = userStore.find((u) => u.id === userId)
      if (!user) return ctx.fail(404, 'not_found', '资源不存在或无权访问')

      store.delete(userId)
      ctx.send(200, { ok: true })
    },
  },
}
