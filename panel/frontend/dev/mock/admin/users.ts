/**
 * [INPUT]: 依赖 ../types 的 MockModule
 * [OUTPUT]: 对外提供 users 模块的假接口 MockModule
 * [POS]: dev/mock/admin 的「用户（后台-03）」假接口，归后台前端一；形状、错误码、reauth 与幂等照 api-contract.md（含修订 Rn）。目前只有人工调账一条，第 2 阶段起用它在浏览器里验证 reauth 弹框与原键重放
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { MockModule } from '../types.ts'

// 用户余额（分），按用户 id 存；没调过的用户按 2650.00 元起
const balances = new Map<string, number>()

export const users: MockModule = {
  routes: {
    // 契约后台-03：billing.provider.write｜reauth：是｜幂等：admin_user_balance_adjust
    'POST /v1/users/:id/balance': async (ctx) => {
      if (!ctx.requirePermission('billing.provider.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('admin_user_balance_adjust', () => {
        const amount = typeof body.amount === 'number' && Number.isInteger(body.amount) ? body.amount : 0
        const reason = typeof body.reason === 'string' ? body.reason.trim() : ''
        if (amount === 0) return { status: 422, body: envelope('validation_failed', '调整金额不能为 0') }
        if ([...reason].length < 5) return { status: 422, body: envelope('validation_failed', '请求参数校验未通过', { reason: '调整原因至少 5 个字' }) }
        const id = ctx.params.id!
        const next = (balances.get(id) ?? 265000) + amount
        if (next < 0) return { status: 409, body: envelope('conflict', '余额不足，无法扣减') }
        balances.set(id, next)
        return { status: 200, body: { balance: next } }
      })
    },
  },
}

// 幂等表存的是完整响应，错误也要按信封形状入表，重放时原样返回
function envelope(code: string, message: string, fields?: Record<string, string>) {
  return { error: { code, message, ...(fields ? { fields } : {}) } }
}
