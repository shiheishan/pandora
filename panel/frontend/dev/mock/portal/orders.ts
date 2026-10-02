/**
 * [INPUT]: 依赖 ../types 的 MockModule，依赖 ./fixtures 的 portalState / gate，依赖 ./billing 的 orderRow / orderDetail / sweepExpired / cancelOrder / fulfill / isUuid，依赖 ./checkout 的 channelIntent
 * [OUTPUT]: 对外提供 orders 模块的假接口 MockModule
 * [POS]: dev/mock/portal 的「我的订单（门户-04）」假接口，归门户前端；形状、错误码照 api-contract.md（含修订 R32、R69）：列表（status 逗号多值、未知状态 400、limit / offset、counts 四类计数、按下单时间倒序）、明细、取消（无 body、非 UUID 400、重复取消回 already_terminal）、「我已支付，刷新状态」查单（假渠道读 checkout 的 channelIntent：回调丢失过的补记、未付 unpaid、没发起过支付 409、每账号每分钟 6 次 429）；读之前先把超时待支付单转 expired
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { MockModule } from '../types.ts'
import { cancelOrder, fulfill, isUuid, orderDetail, orderRow, sweepExpired } from './billing.ts'
import { channelIntent } from './checkout.ts'
import { gate, portalState } from './fixtures.ts'

const STATUSES = new Set(['draft', 'pending_payment', 'processing', 'paid', 'fulfilled', 'cancelled', 'expired', 'partially_refunded', 'refunded'])
const COUNT_GROUPS = {
  open: ['draft', 'pending_payment', 'processing'],
  paid: ['paid', 'fulfilled'],
  closed: ['cancelled', 'expired'],
  refunded: ['partially_refunded', 'refunded'],
} as const

// 查单的按账号限流（后端 ByAccount("order_query", time.Minute, 6)）
const queryLog = new Map<string, number[]>()

export const orders: MockModule = {
  routes: {
    'GET /v1/orders': async (ctx) => {
      if (!(await gate(ctx))) return
      const raw = ctx.query.get('status')
      const wanted = raw ? raw.split(',').filter(Boolean) : []
      if (wanted.some((s) => !STATUSES.has(s))) return ctx.fail(400, 'bad_request', '不支持的订单状态')
      const limitRaw = Number(ctx.query.get('limit') ?? '20')
      const limit = Number.isInteger(limitRaw) && limitRaw >= 1 && limitRaw <= 100 ? limitRaw : 20
      const offset = Math.max(0, Number(ctx.query.get('offset') ?? '0') || 0)
      const state = portalState(ctx.user.userId)
      sweepExpired(state)
      const sorted = [...state.orders].sort((a, b) => b.created_at.localeCompare(a.created_at))
      const all = sorted.filter((o) => wanted.length === 0 || wanted.includes(o.status))
      const count = (group: readonly string[]) => state.orders.filter((o) => group.includes(o.status)).length
      ctx.send(200, {
        orders: all.slice(offset, offset + limit).map(orderRow),
        total: all.length,
        counts: { open: count(COUNT_GROUPS.open), paid: count(COUNT_GROUPS.paid), closed: count(COUNT_GROUPS.closed), refunded: count(COUNT_GROUPS.refunded) },
      })
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

    // 契约：非 UUID 回 400（与详情的 404 不一致，照后端）；已取消重复取消回 200 already_terminal
    'POST /v1/orders/:id/cancel': (ctx) => {
      if (!isUuid(ctx.params.id)) return ctx.fail(400, 'bad_request', '订单 ID 不合法')
      const state = portalState(ctx.user.userId)
      sweepExpired(state)
      const order = state.orders.find((o) => o.id === ctx.params.id)
      if (!order) return ctx.fail(404, 'not_found', '订单不存在')
      const view = () => ({ order_id: order.id, status: 'cancelled', state_version: 2, cancelled_at: order.cancelled_at, cancel_reason: order.cancel_reason })
      if (order.status === 'cancelled') return ctx.send(200, { ...view(), already_terminal: true })
      if (order.status === 'expired') return ctx.fail(409, 'conflict', '订单已过期')
      if (!['draft', 'pending_payment'].includes(order.status)) return ctx.fail(409, 'conflict', '订单已支付或正在处理，不能取消')
      cancelOrder(state, order)
      ctx.send(200, { ...view(), already_terminal: false })
    },

    // 「我已支付，刷新状态」（PAY-009 主动查单）：假渠道按假收银台的记录回答。
    // 「模拟支付成功但回调丢失」过的单查到已付并补记履约；发起过支付但没付的回 unpaid；
    // 没发起过支付回 409（种子里的「处理中」单就是这样，用来看「查询失败」）；每账号每分钟 6 次
    'POST /v1/orders/:id/query': (ctx) => {
      const state = portalState(ctx.user.userId)
      sweepExpired(state)
      const order = isUuid(ctx.params.id) ? state.orders.find((o) => o.id === ctx.params.id) : undefined
      if (!order) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      const now = Date.now()
      const recent = (queryLog.get(ctx.user.userId) ?? []).filter((t) => now - t < 60_000)
      if (recent.length >= 6) return ctx.fail(429, 'rate_limited', '请求过于频繁，请稍后再试')
      queryLog.set(ctx.user.userId, [...recent, now])
      const intent = channelIntent(ctx.user.userId, order.id)
      if (!intent) return ctx.fail(409, 'conflict', '该订单从未发起过支付，无法向渠道查单')
      const view = (channel: 'paid' | 'unpaid', reconciled: boolean, already: boolean) =>
        ctx.send(200, { order_id: order.id, order_no: order.order_no, provider_code: intent.provider, channel_status: channel, reconciled, already_recorded: already, order_status: order.status })
      if (!intent.channelPaid) return view('unpaid', false, false)
      if (order.status === 'pending_payment' || order.status === 'processing') {
        fulfill(state, order, { provider: intent.provider, method: intent.method })
        return view('paid', true, false)
      }
      view('paid', false, true)
    },
  },
}
