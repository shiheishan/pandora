/**
 * [INPUT]: 依赖 ../types 的 MockModule，依赖 ./fixtures 的 portalState / scenario / gate
 * [OUTPUT]: 对外提供 orders 模块的假接口 MockModule
 * [POS]: dev/mock/portal 的「我的订单（门户-04）」假接口，归门户前端；形状、错误码照 api-contract.md（含修订 R32）。第 ① 步只有概览待支付条要的 GET v1/orders（单值 status 过滤），第 ③ 步订单页在此扩充；legacy 场景去掉「待补·后端」字段
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { MockModule } from '../types.ts'
import { gate, portalState, scenario, type OrderFixture } from './fixtures.ts'

const STATUSES = new Set(['draft', 'pending_payment', 'processing', 'paid', 'fulfilled', 'cancelled', 'expired', 'partially_refunded', 'refunded'])
const OPEN = new Set(['draft', 'pending_payment', 'processing'])

function orderRow(o: OrderFixture) {
  const legacy = scenario() === 'legacy'
  return {
    id: o.id,
    order_no: o.order_no,
    kind: o.kind,
    status: o.status,
    currency: 'CNY',
    total_amount: o.total_amount,
    discount_amount: 0,
    balance_applied: 0,
    payable_amount: o.total_amount,
    paid_amount: 0,
    refunded_amount: 0,
    ...(o.plan_name === undefined ? {} : { plan_name: o.plan_name }),
    cancellable: OPEN.has(o.status),
    created_at: o.created_at,
    ...(o.expires_at === undefined ? {} : { expires_at: o.expires_at }),
    ...(legacy
      ? {}
      : {
          ...(o.interval === undefined ? {} : { interval: o.interval, interval_count: o.interval_count ?? 1 }),
          ...(o.item_name === undefined ? {} : { item_name: o.item_name }),
        }),
  }
}

export const orders: MockModule = {
  routes: {
    // limit 1–100，非法值回落 20；status 只认单值（多值属待补·后端），未知值 400
    'GET /v1/orders': async (ctx) => {
      if (!(await gate(ctx))) return
      const status = ctx.query.get('status')
      if (status !== null && status !== '' && !STATUSES.has(status)) return ctx.fail(400, 'bad_request', '不支持的订单状态')
      const limitRaw = Number(ctx.query.get('limit') ?? '20')
      const limit = Number.isInteger(limitRaw) && limitRaw >= 1 && limitRaw <= 100 ? limitRaw : 20
      const offset = Math.max(0, Number(ctx.query.get('offset') ?? '0') || 0)
      const all = portalState(ctx.user.userId).orders.filter((o) => !status || o.status === status)
      ctx.send(200, { orders: all.slice(offset, offset + limit).map(orderRow), total: all.length })
    },
  },
}
