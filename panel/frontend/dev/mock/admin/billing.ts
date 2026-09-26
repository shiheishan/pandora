/**
 * [INPUT]: 依赖 node:crypto 的 randomUUID，依赖 ../types 的 Json / MockModule / MockResult，依赖 ./billing-store 的订单 / 挂账 / 渠道 / 收入调整数据与视图，依赖 ./plans-store 的 plans，依赖 ./users 的 userStore
 * [OUTPUT]: 对外提供 billing 模块的假接口 MockModule
 * [POS]: dev/mock/admin 的「订单与收款（后台-05）」假接口，归后台前端一：订单列表（q、status 逗号多值白名单、user_id、from / to、limit / offset，R63）/ 详情 / 支付记录 / 取消（state_version CAS）/ 人工开单（grant / pending / offline，R64 / R74）/ 标记已支付、挂账列表与转入余额（R3）、渠道列表与启停（R66）、收入调整列表 / 登记 / 冲销。权限 → reauth → 幂等 scope 照 router_billing.go 与 router_dashboard.go，校验键名与文案照 Go（billing 域几处英文 message 原样保留，页面负责映射），按 DisallowUnknownFields 拒绝未知字段（apply-to-balance 例外，与后端的 json.NewDecoder 一致）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomUUID } from 'node:crypto'
import type { Json, MockModule, MockResult } from '../types.ts'
import {
  adjustments,
  adjustmentView,
  err,
  findOrder,
  fulfil,
  invalid,
  isUuid,
  lateCases,
  lateView,
  NOT_FOUND,
  nextOrderNo,
  orderDetail,
  orderHistory,
  orderRow,
  orders,
  paymentFor,
  providers,
  providerView,
  subscriptionEnded,
  todayLocal,
  type Adjustment,
  type Order,
} from './billing-store.ts'
import { plans } from './plans-store.ts'
import { userStore } from './users.ts'

const STATUSES = ['draft', 'pending_payment', 'processing', 'paid', 'fulfilled', 'cancelled', 'expired', 'partially_refunded', 'refunded']
const LATE = ['', 'suspense', 'applied', 'refunded', 'manual_review', 'refund_pending']
const chars = (v: unknown) => (typeof v === 'string' ? [...v.trim()].length : 0)
const str = (v: unknown) => (typeof v === 'string' ? v : '')
const BAD_JSON = err(400, 'bad_request', '请求体不是合法的 JSON')

function unknownField(body: Json, allowed: readonly string[]): MockResult | null {
  const extra = Object.keys(body).find((k) => !allowed.includes(k))
  return extra ? err(400, 'bad_request', `请求体包含未知字段 "${extra}"`) : null
}

/** 线下凭证号（billing.validateOfflineReference） */
function referenceProblem(ref: string): Record<string, string> | null {
  return ref === '' || [...ref].length > 128 ? { reference: '请填写线下凭证号（银行流水号、收据编号等），最多 128 字' } : null
}

/** provider_payment_id 唯一：同一张凭证不能入账两次（另一张单用过回 409，文案后端是英文，R74） */
function referenceOwner(ref: string): Order | undefined {
  return orders.find((o) => o.payments.some((p) => p.provider_payment_id === `offline:${ref}`))
}

const day = (v: string | null, end: boolean) => {
  if (!v || !/^\d{4}-\d{2}-\d{2}$/.test(v)) return null
  const t = Date.parse(`${v}T00:00:00Z`)
  return Number.isFinite(t) ? t + (end ? 86_400_000 : 0) : null
}

export const billing: MockModule = {
  routes: {
    // ---- 订单 ---------------------------------------------------------------
    'GET /v1/orders': (ctx) => {
      if (!ctx.requirePermission('billing.order.read')) return
      const qs = ctx.query
      const statuses = (qs.get('status') ?? '')
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean)
      if (statuses.some((s) => !STATUSES.includes(s))) return ctx.fail(400, 'bad_request', '不支持的订单状态')
      const userId = (qs.get('user_id') ?? '').trim()
      if (userId && !isUuid(userId)) return ctx.fail(400, 'bad_request', '用户 ID 格式不正确')
      const q = (qs.get('q') ?? '').trim().toLowerCase()
      const from = day(qs.get('from'), false)
      const to = day(qs.get('to'), true)
      const limitRaw = Number.parseInt(qs.get('limit') ?? '', 10)
      const limit = limitRaw >= 1 && limitRaw <= 100 ? limitRaw : 25
      const offset = Math.max(0, Number.parseInt(qs.get('offset') ?? '', 10) || 0)
      const rows = orders
        .filter(
          (o) =>
            (!q || o.order_no.toLowerCase().includes(q) || o.user_email.toLowerCase().includes(q)) &&
            (!statuses.length || statuses.includes(o.status)) &&
            (!userId || o.user_id === userId) &&
            (from === null || Date.parse(o.created_at) >= from) &&
            (to === null || Date.parse(o.created_at) < to),
        )
        .sort((a, b) => b.created_at.localeCompare(a.created_at))
      ctx.send(200, { orders: rows.slice(offset, offset + limit).map(orderRow), total: rows.length })
    },

    'GET /v1/orders/:id': (ctx) => {
      if (!ctx.requirePermission('billing.order.read')) return
      const o = findOrder(ctx.params.id)
      if (!o) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      ctx.send(200, { order: orderDetail(o) })
    },

    'GET /v1/orders/:id/payments': (ctx) => {
      if (!ctx.requirePermission('billing.payment.read')) return
      const o = findOrder(ctx.params.id)
      if (!o) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      ctx.send(200, orderHistory(o))
    },

    // 取消：写权限 + 幂等，没挂 reauth（契约）；400 文案是 billing 域的英文原文
    'POST /v1/orders/:id/cancel': async (ctx) => {
      if (!ctx.requirePermission('billing.order.write')) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('admin_order_cancel', () => {
        const bad = unknownField(body, ['expected_state_version', 'reason'])
        if (bad) return bad
        const o = findOrder(ctx.params.id)
        if (!o) return NOT_FOUND
        const expected = body.expected_state_version
        if (typeof expected !== 'number' || !Number.isInteger(expected) || expected <= 0) return err(400, 'bad_request', 'expected_state_version 必须是正整数')
        const reason = str(body.reason).trim()
        if (chars(reason) < 5 || chars(reason) > 500) return err(400, 'bad_request', '取消原因需要 5 到 500 个字')
        const out = () => ({ order_id: o.id, status: o.status, state_version: o.state_version, cancelled_at: o.cancelled_at ?? undefined, cancel_reason: o.cancel_reason ?? undefined })
        if (o.status === 'cancelled') return { status: 200, body: { order: { ...out(), already_terminal: true }, already_terminal: true } }
        // R95 / R114：与 billing.releaseOrderReservation 同顺序同文案——终态、已付、其余不可取消，再版本号，最后才看入账
        if (o.status === 'expired') return err(409, 'conflict', '订单已过期')
        if (['paid', 'fulfilled', 'partially_refunded', 'refunded'].includes(o.status)) return err(409, 'conflict', '订单已支付，不能取消')
        if (!['draft', 'pending_payment', 'processing'].includes(o.status)) return err(409, 'conflict', `订单当前状态（${o.status}）不能取消`)
        if (o.state_version !== expected) return err(409, 'conflict', '订单状态已变化，请刷新后再操作')
        if (o.payments.some((p) => p.status === 'succeeded')) return err(409, 'conflict', '这张订单已有入账，不能取消')
        const at = new Date().toISOString()
        Object.assign(o, { status: 'cancelled', cancelled_at: at, cancel_reason: reason, state_version: o.state_version + 1, updated_at: at })
        for (const i of o.intents) if (['created', 'requires_action', 'processing'].includes(i.status)) Object.assign(i, { status: 'cancelled', updated_at: at })
        return { status: 200, body: { order: { ...out(), already_terminal: false }, already_terminal: false } }
      })
    },

    // 人工开单：写权限 → reauth → 幂等（scope 与用户结账共用 order_create），201 为预写响应（R64 / R74）
    'POST /v1/orders/manual': async (ctx) => {
      if (!ctx.requirePermission('billing.order.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('order_create', () => {
        const bad = unknownField(body, ['user_id', 'plan_id', 'price_id', 'reason', 'settlement', 'reference'])
        if (bad) return bad
        const userId = str(body.user_id)
        const planId = str(body.plan_id)
        if (!userId || !planId) return err(400, 'bad_request', 'tenant, actor, user and plan are required')
        if (!isUuid(userId) || !isUuid(planId)) return err(400, 'bad_request', '标识符格式不正确')
        const reason = str(body.reason).trim()
        if (chars(reason) < 5 || chars(reason) > 500) return invalid({ reason: '请写清开单原因，5 到 500 个字。这条会进审计，是日后对账的唯一依据' })
        const settlement = str(body.settlement) || 'grant'
        const reference = str(body.reference).trim()
        if (settlement === 'balance') return invalid({ settlement: '暂不支持从余额扣除；需要时请先调账，再用赠送开单' })
        if (!['grant', 'pending', 'offline'].includes(settlement)) return invalid({ settlement: '结算方式只能是 grant、pending 或 offline' })
        if (settlement === 'offline') {
          const r = referenceProblem(reference)
          if (r) return invalid(r)
        }
        const user = userStore.find((u) => u.id === userId)
        if (!user) return NOT_FOUND
        const plan = plans.find((p) => p.id === planId)
        const price = plan?.prices.find((x) => x.id === str(body.price_id))
        // 假后端的近似：CreateOrder 的套餐 / 价格失效错误形状以后端为准
        if (!plan || plan.status !== 'active' || !price || price.status !== 'active') return invalid({ price_id: '价格不存在、已下架或不属于该套餐' })
        if (settlement === 'offline' && price.unit_amount <= 0) return err(409, 'conflict', '这张订单不需要支付，请改用赠送')
        if (settlement === 'offline' && referenceOwner(reference)) return err(409, 'conflict', '凭证号已用于其他订单')

        const at = new Date().toISOString()
        const grant = settlement === 'grant'
        const amount = price.unit_amount
        const o: Order = {
          id: randomUUID(),
          order_no: nextOrderNo(),
          user_id: user.id,
          user_email: user.email,
          kind: 'new',
          status: 'pending_payment',
          currency: price.currency,
          subtotal_amount: amount,
          discount_amount: grant ? amount : 0,
          total_amount: grant ? 0 : amount,
          balance_applied: 0,
          payable_amount: grant ? 0 : amount,
          paid_amount: 0,
          refunded_amount: 0,
          state_version: 1,
          manual_reason: reason,
          created_by: ctx.user.userId,
          created_by_email: ctx.user.email,
          subscription_id: null,
          created_at: at,
          updated_at: at,
          paid_at: null,
          expires_at: grant ? null : new Date(Date.now() + 30 * 60_000).toISOString(),
          fulfilled_at: null,
          cancelled_at: null,
          expired_at: null,
          cancel_reason: null,
          items: [
            {
              id: randomUUID(),
              plan_id: plan.id,
              price_id: price.id,
              product_name: plan.name,
              plan_name: plan.name,
              plan_version: plan.versions.find((v) => v.id === plan.current_version_id)?.version ?? 1,
              interval: price.billing_interval,
              interval_count: price.interval_count,
              quantity: 1,
              unit_amount: amount,
              currency: price.currency,
            },
          ],
          intents: [],
          payments: [],
          refunds: [],
        }
        if (grant) {
          o.paid_at = at
          fulfil(o, at)
        }
        if (settlement === 'offline') {
          o.payments.push(paymentFor(o, 'offline', null, at, `offline:${reference}`))
          Object.assign(o, { paid_amount: amount, paid_at: at, state_version: 3 })
          fulfil(o, at)
        }
        orders.push(o)
        const { discount_amount, id: order_id, order_no, currency, total_amount, balance_applied, payable_amount, status } = o
        return { status: 201, body: { discount_amount, order_id, order_no, currency, total_amount, balance_applied, payable_amount, status } }
      })
    },

    // 标记已支付：写权限 → reauth → 幂等；金额从订单读，不收传入（R2 响应 snake_case）
    'POST /v1/orders/:id/mark-paid': async (ctx) => {
      if (!ctx.requirePermission('billing.order.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('admin_order_mark_paid', () => {
        const bad = unknownField(body, ['reason', 'reference'])
        if (bad) return bad
        const o = findOrder(ctx.params.id)
        if (!o) return NOT_FOUND
        const reason = str(body.reason).trim()
        if (chars(reason) < 5 || chars(reason) > 500) return invalid({ reason: '请写清收款说明，5 到 500 个字' })
        const reference = str(body.reference).trim()
        const r = referenceProblem(reference)
        if (r) return invalid(r)
        const owner = referenceOwner(reference)
        if (o.status !== 'pending_payment' && o.status !== 'processing') return err(409, 'conflict', `只有待支付的订单可以标记为已支付，当前状态：${o.status}`)
        // 同一凭证在仍待支付的单上入过账：上一次钱进了挂账（R117）
        if (owner === o) return err(409, 'conflict', `凭证号 ${reference} 已经入过账，不能重复标记`)
        if (o.payable_amount <= 0) return err(409, 'conflict', '这张订单不需要支付')
        if (owner) return err(409, 'conflict', '凭证号已用于其他订单')
        const at = new Date().toISOString()
        const p = paymentFor(o, 'offline', null, at, `offline:${reference}`)
        o.payments.push(p)
        // 续费 / 变更单的订阅已结束：钱照常入账、隔离进挂账，订单不动，回 409 说明去向（R117）
        if (subscriptionEnded.has(o.id)) {
          lateCases.unshift({ id: randomUUID(), case_kind: 'ineligible_subscription', status: 'suspense', amount: o.payable_amount, currency: o.currency, order: o, received_at: at })
          return err(409, 'conflict', '订阅已结束，款项已转入挂账，可在挂账里转入用户余额')
        }
        Object.assign(o, { paid_amount: o.payable_amount, paid_at: at, state_version: o.state_version + 2, updated_at: at })
        if (o.kind === 'topup') o.status = 'paid'
        else fulfil(o, at)
        return { status: 200, body: { processed: true, already_handled: false, payment_id: p.id, subscription_id: o.subscription_id ?? '', ledger_txn_id: randomUUID() } }
      })
    },

    // ---- 挂账（保留规则 6）-----------------------------------------------------
    'GET /v1/late-payments': (ctx) => {
      if (!ctx.requirePermission('billing.ledger.read')) return
      const status = (ctx.query.get('status') ?? '').trim()
      if (!LATE.includes(status)) return ctx.fail(400, 'bad_request', '不支持的挂账状态')
      const limitRaw = Number.parseInt(ctx.query.get('limit') ?? '', 10)
      const limit = limitRaw >= 1 && limitRaw <= 100 ? limitRaw : 25
      const offset = Math.max(0, Number.parseInt(ctx.query.get('offset') ?? '', 10) || 0)
      const rows = lateCases
        .filter((c) => !status || c.status === status)
        .sort((a, b) => Number(b.status === 'suspense') - Number(a.status === 'suspense') || b.received_at.localeCompare(a.received_at))
      const pending: Record<string, number> = {}
      for (const c of lateCases) if (c.status === 'suspense') pending[c.currency] = (pending[c.currency] ?? 0) + c.amount
      ctx.send(200, {
        cases: rows.slice(offset, offset + limit).map(lateView),
        total: rows.length,
        pending_amounts: pending,
        pending_amount: Object.values(pending).reduce((a, b) => a + b, 0),
      })
    },

    // 转入余额：billing.adjustment.write → reauth → 幂等；后端用 json.NewDecoder，不拒绝多余字段
    'POST /v1/late-payments/:id/apply-to-balance': async (ctx) => {
      if (!ctx.requirePermission('billing.adjustment.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体无法解析')
      await ctx.idempotent('late_payment_apply', () => {
        const c = isUuid(ctx.params.id) ? lateCases.find((x) => x.id === ctx.params.id) : undefined
        if (!c) return NOT_FOUND
        const reason = str(body.reason).trim()
        if (chars(reason) < 5 || chars(reason) > 500) return invalid({ reason: '请写清处理原因，5 到 500 个字' })
        if (c.status !== 'suspense') return err(409, 'conflict', '这笔挂账已经处理过了')
        const at = new Date().toISOString()
        Object.assign(c, { status: 'applied', resolved_at: at, resolution_reason: reason })
        const u = userStore.find((x) => x.id === c.order.user_id)
        if (u && u.currency === c.currency) u.balance += c.amount
        return { status: 200, body: { ledger_txn_id: randomUUID() } }
      })
    },

    // ---- 支付渠道 -------------------------------------------------------------
    'GET /v1/payment-providers': (ctx) => {
      if (!ctx.requirePermission('billing.payment.read')) return
      ctx.send(200, { providers: [...providers].sort((a, b) => a.code.localeCompare(b.code)).map(providerView) })
    },

    // 启停：billing.provider.write → reauth，没有幂等；两个布尔漏传按 false
    'POST /v1/payment-providers/:code/toggle': async (ctx) => {
      if (!ctx.requirePermission('billing.provider.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const bad = unknownField(body, ['enabled', 'accepting_new'])
      if (bad) return ctx.send(bad.status, bad.body)
      const p = providers.find((x) => x.code === ctx.params.code)
      if (!p) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      p.enabled = body.enabled === true
      p.accepting_new = body.accepting_new === true
      ctx.send(200, { ok: true })
    },

    // ---- 收入调整（报表口径，只追加）---------------------------------------------
    'GET /v1/revenue/adjustments': (ctx) => {
      if (!ctx.requirePermission('billing.ledger.read')) return
      const currency = (ctx.query.get('currency') ?? '').toUpperCase()
      if (currency && currency !== 'CNY' && currency !== 'USD') return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { currency: '仅支持 CNY 或 USD' })
      const rows = adjustments
        .filter((a) => !currency || a.currency === currency)
        .sort((a, b) => b.created_at.localeCompare(a.created_at))
        .slice(0, 200)
      ctx.send(200, { adjustments: rows.map(adjustmentView) })
    },

    'POST /v1/revenue/adjustments': async (ctx) => {
      if (!ctx.requirePermission('billing.adjustment.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.send(BAD_JSON.status, BAD_JSON.body)
      await ctx.idempotent('revenue_adjustment_create', () => {
        const bad = unknownField(body, ['currency', 'amount', 'reason', 'effective_on'])
        if (bad) return bad
        const f: Record<string, string> = {}
        const currency = str(body.currency).trim().toUpperCase()
        if (currency !== 'CNY' && currency !== 'USD') f.currency = '仅支持 CNY 或 USD'
        const amount = body.amount
        if (typeof amount !== 'number' || !Number.isInteger(amount) || amount === 0 || Math.abs(amount) > 1e12) f.amount = '调整金额必须非零且绝对值不超过 100 亿'
        if (chars(body.reason) < 5 || chars(body.reason) > 500) f.reason = '理由需为 5–500 个字符'
        const effective = str(body.effective_on)
        if (effective && !/^\d{4}-\d{2}-\d{2}$/.test(effective)) f.effective_on = '日期格式应为 YYYY-MM-DD'
        if (Object.keys(f).length) return invalid(f)
        const today = todayLocal()
        if (effective > today) return invalid({ effective_on: '生效日期不能晚于租户今天' })
        const a: Adjustment = {
          id: randomUUID(),
          currency: currency as Adjustment['currency'],
          amount: amount as number,
          reason: str(body.reason).trim(),
          effective_on: effective || today,
          created_by: ctx.user.userId,
          created_by_email: ctx.user.email,
          created_at: new Date().toISOString(),
        }
        adjustments.push(a)
        return { status: 200, body: adjustmentView(a) }
      })
    },

    'POST /v1/revenue/adjustments/:id/reverse': async (ctx) => {
      if (!ctx.requirePermission('billing.adjustment.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.send(BAD_JSON.status, BAD_JSON.body)
      await ctx.idempotent('revenue_adjustment_reverse', () => {
        const bad = unknownField(body, ['reason'])
        if (bad) return bad
        const reason = str(body.reason).trim()
        if (chars(reason) < 5 || chars(reason) > 500) return invalid({ reason: '撤销理由需为 5–500 个字符' })
        const original = adjustments.find((a) => a.id === ctx.params.id)
        if (!original) return NOT_FOUND
        if (original.reversal_of) return err(409, 'conflict', '反向记录不能再次撤销')
        if (adjustments.some((a) => a.reversal_of === original.id)) return err(409, 'conflict', '该调整已撤销')
        const a: Adjustment = {
          id: randomUUID(),
          currency: original.currency,
          amount: -original.amount,
          reason,
          effective_on: original.effective_on,
          reversal_of: original.id,
          created_by: ctx.user.userId,
          created_by_email: ctx.user.email,
          created_at: new Date().toISOString(),
        }
        adjustments.push(a)
        return { status: 200, body: adjustmentView(a) }
      })
    },
  },
}
