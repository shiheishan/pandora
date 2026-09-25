/**
 * [INPUT]: 依赖 ../types 的 MockModule，依赖 ./fixtures 的 portalState / gate，依赖 ./billing 的 orderRow / orderDetail / sweepExpired
 * [OUTPUT]: 对外提供 orders 模块的假接口 MockModule
 * [POS]: dev/mock/portal 的「我的订单（门户-04）」假接口，归门户前端；形状、错误码照 api-contract.md（含修订 R32、R69）：列表（单值 status 过滤、未知状态 400、limit / offset）与明细（支付回跳确认读它）；读之前先把超时待支付单转 expired。第 ③ 步订单页在此扩充
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { MockModule } from '../types.ts'
import { orderDetail, orderRow, sweepExpired } from './billing.ts'
import { gate, portalState } from './fixtures.ts'

const STATUSES = new Set(['draft', 'pending_payment', 'processing', 'paid', 'fulfilled', 'cancelled', 'expired', 'partially_refunded', 'refunded'])

export const orders: MockModule = {
  routes: {
    'GET /v1/orders': async (ctx) => {
      if (!(await gate(ctx))) return
      const status = ctx.query.get('status')
      if (status !== null && status !== '' && !STATUSES.has(status)) return ctx.fail(400, 'bad_request', '不支持的订单状态')
      const limitRaw = Number(ctx.query.get('limit') ?? '20')
      const limit = Number.isInteger(limitRaw) && limitRaw >= 1 && limitRaw <= 100 ? limitRaw : 20
      const offset = Math.max(0, Number(ctx.query.get('offset') ?? '0') || 0)
      const state = portalState(ctx.user.userId)
      sweepExpired(state)
      const all = state.orders.filter((o) => !status || o.status === status)
      ctx.send(200, { orders: all.slice(offset, offset + limit).map(orderRow), total: all.length })
    },

    // 不存在、不属于本人、非 UUID 一律 404
    'GET /v1/orders/:id': async (ctx) => {
      if (!(await gate(ctx))) return
      const state = portalState(ctx.user.userId)
      sweepExpired(state)
      const order = state.orders.find((o) => o.id === ctx.params.id)
      if (!order) return ctx.fail(404, 'not_found', '订单不存在')
      ctx.send(200, orderDetail(state, order))
    },
  },
}
