import { randomUUID } from 'node:crypto'
import type { Json, MockModule, MockResult } from '../types.ts'
import {
  adjustments,
  adjustmentView,
  err,
  findOrder,
  fulfil,
  intentFor,
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
  pendingEffects,
  providers,
  providerSecrets,
  providerView,
  paymentSettings,
  PUBLIC_BASE_URL,
  effectiveNotifyBase,
  siteMinPayment,
  subscriptionEnded,
  todayLocal,
  type Adjustment,
  type Order,
  type Provider,
} from './billing-store.ts'
import { candidatesOf, manualDue, manualDueBelowMinimum, planOptions, placementView, prorationCredit, type Option } from './billing-placement.ts'
import { plans } from './plans-store.ts'
import { userStore } from './users.ts'

const STATUSES = ['pending_payment', 'paid', 'fulfilled', 'cancelled', 'expired']
const LATE = ['', 'suspense', 'applied']
const chars = (v: unknown) => (typeof v === 'string' ? [...v.trim()].length : 0)
const str = (v: unknown) => (typeof v === 'string' ? v : '')
const BAD_JSON = err(400, 'bad_request', '请求体不是合法的 JSON')

const purchaseLimits = { max_unpaid_orders: 0, max_subscriptions: 0 }

// ---- 支付渠道写入：照 billing/provider_admin.go / dto.go ----
// 用户 2026-10-07 定：易支付只出支付宝和微信
const EPAY_METHODS = ['alipay', 'wxpay']
const ONLINE_ADAPTERS = ['epay', 'btcpay', 'coinpayments'] as const
const COINPAYMENTS_DEFAULT = 'https://a-api.coinpayments.net'
const PROVIDER_SETTINGS = [
  'display_name',
  'base_url',
  'submit_path',
  'api_path',
  'methods',
  'default_method',
  'allow_private_host',
  'min_amount',
  'fee_fixed',
  'fee_percent',
  'merchant_id',
  'key',
  'webhook_secret',
] as const
const PROVIDER_PATH = /^\/[A-Za-z0-9._~/-]{0,127}$/
const PRIVATE_HOST = /^(localhost|127\.|10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.)/

/** 解析失败或空串回 null */
function parseURL(raw: string): URL | null {
  if (!raw) return null
  try {
    return new URL(raw)
  } catch {
    return null
  }
}

interface ProviderSettings {
  config: Pick<
    Provider,
    'display_name' | 'base_url' | 'submit_path' | 'api_path' | 'methods' | 'default_method' | 'allow_private_host' | 'fee_fixed' | 'fee_percent'
  >
  min_amount: number | null
  fee_fixed: number
  fee_percent: string
  merchant_id: string
  key: string
  webhook_secret: string
}

/** fee_percent 是 Go 的 *string：不是字符串在解码时就 400；不是十进制数 422（handler），超范围或超过两位小数 422（service） */
function normalizeFeePercent(body: Json, f: Record<string, string>): string | null {
  if (body.fee_percent === undefined || body.fee_percent === null) return null
  const s = str(body.fee_percent).trim()
  if (/[eE]/.test(s) || !/^-?\d+(\.\d+)?$/.test(s)) {
    f.fee_percent = '比例手续费需是数字，如 2.5'
    return null
  }
  const n = Number(s)
  if (n < 0 || n > 100 || (s.split('.')[1]?.length ?? 0) > 2) {
    f.fee_percent = '比例手续费需在 0 到 100 之间，最多两位小数'
    return null
  }
  return n.toFixed(2)
}

function providerSettings(body: Json, adapter: string): { ok: ProviderSettings } | { fields: Record<string, string> } | { bad: MockResult } {
  const rawMin = body.min_amount
  if (rawMin !== undefined && rawMin !== null && (typeof rawMin !== 'number' || !Number.isInteger(rawMin))) return { bad: err(400, 'bad_request', '请求体不是合法的 JSON') }
  const rawFeeFixed = body.fee_fixed
  if (rawFeeFixed !== undefined && rawFeeFixed !== null && (typeof rawFeeFixed !== 'number' || !Number.isInteger(rawFeeFixed))) return { bad: err(400, 'bad_request', '请求体不是合法的 JSON') }
  if (body.fee_percent !== undefined && body.fee_percent !== null && typeof body.fee_percent !== 'string') return { bad: err(400, 'bad_request', '请求体不是合法的 JSON') }
  const f: Record<string, string> = {}
  const display_name = str(body.display_name).trim()
  if (chars(display_name) < 1 || chars(display_name) > 40) f.display_name = '名称需为 1–40 个字'
  const allow = body.allow_private_host === true
  let base_url = str(body.base_url).trim().replace(/\/+$/, '')
  if (adapter === 'coinpayments' && !base_url) base_url = COINPAYMENTS_DEFAULT.replace(/\/+$/, '')
  const needBase = adapter === 'epay' || adapter === 'btcpay'
  const url = base_url ? parseURL(base_url) : null
  if (needBase) {
    if (!url || (url.protocol !== 'https:' && url.protocol !== 'http:') || url.search || url.hash || url.username) f.base_url = '请填写完整的站点地址，如 https://pay.example.com'
    else if (url.protocol !== 'https:' && !allow) f.base_url = '站点地址必须使用 https'
    else if (!allow && PRIVATE_HOST.test(url.hostname)) f.base_url = `渠道配置校验未通过：主机 "${url.hostname}" 解析到非公网地址`
  } else if (base_url) {
    if (!url || (url.protocol !== 'https:' && url.protocol !== 'http:') || url.search || url.hash || url.username) f.base_url = '请填写完整的站点地址，如 https://pay.example.com'
    else if (url.protocol !== 'https:' && !allow) f.base_url = '站点地址必须使用 https'
  }
  let submit_path = ''
  let api_path = ''
  const methods: string[] = []
  let default_method = ''
  if (adapter === 'epay') {
    const paths = { submit_path: str(body.submit_path).trim() || '/submit.php', api_path: str(body.api_path).trim() || '/api.php' }
    for (const k of ['submit_path', 'api_path'] as const) if (!PROVIDER_PATH.test(paths[k]) || paths[k].startsWith('//')) f[k] = '路径需以 / 开头，只含字母、数字与 . _ ~ / -'
    submit_path = paths.submit_path
    api_path = paths.api_path
    const raw = Array.isArray(body.methods) ? body.methods.map((m) => str(m).trim()) : []
    for (const m of raw) {
      if (!EPAY_METHODS.includes(m)) f.methods = '支付方式只能从支付宝、微信支付中选'
      else if (!methods.includes(m)) methods.push(m)
    }
    if (methods.length === 0 && !f.methods) f.methods = '至少选一种支付方式'
    default_method = str(body.default_method).trim() || (methods[0] ?? '')
    if (!f.methods && !methods.includes(default_method)) f.default_method = '默认方式必须是已勾选的方式之一'
  }
  const min_amount = typeof rawMin === 'number' && rawMin !== 0 ? rawMin : null
  if (min_amount !== null && (min_amount < 1 || min_amount > 100_000)) f.min_amount = '最低付款额需在 ¥0.01 到 ¥1000.00 之间'
  let fee_fixed = 0
  if (rawFeeFixed !== undefined && rawFeeFixed !== null) {
    if (rawFeeFixed < 0) f.fee_fixed = '固定手续费不能为负'
    else if (rawFeeFixed > 100_000) f.fee_fixed = '固定手续费需在 ¥0 到 ¥1000.00 之间，最多两位小数'
    else fee_fixed = rawFeeFixed
  }
  const feePercentNorm = normalizeFeePercent(body, f)
  const webhook_secret = str(body.webhook_secret).trim()
  if (webhook_secret && [...webhook_secret].length > 256) f.webhook_secret = 'Webhook 密钥最多 256 个字符'
  if (Object.keys(f).length) return { fields: f }
  const fee_percent = feePercentNorm ?? '0.00'
  return {
    ok: {
      config: {
        display_name,
        base_url,
        submit_path,
        api_path,
        methods,
        default_method,
        allow_private_host: allow,
        fee_fixed,
        fee_percent,
      },
      min_amount,
      fee_fixed,
      fee_percent,
      merchant_id: str(body.merchant_id).trim(),
      key: str(body.key).trim(),
      webhook_secret,
    },
  }
}

function normalizeNotifyBase(raw: string): { ok: string } | { field: string } {
  const v = raw.trim().replace(/\/+$/, '')
  if (v === '') return { ok: '' }
  const u = parseURL(v)
  if (!u || u.protocol !== 'https:' || u.search || u.hash || u.username) return { field: '请填写完整的地址，如 https://pay-cb.example.com，不带查询串' }
  if (v.length > 200) return { field: '请填写完整的地址，如 https://pay-cb.example.com，不带查询串' }
  return { ok: v }
}

function unknownField(body: Json, allowed: readonly string[]): MockResult | null {
  const extra = Object.keys(body).find((k) => !allowed.includes(k))
  return extra ? err(400, 'bad_request', `请求体包含未知字段 "${extra}"`) : null
}

/** 线下凭证号（billing.validateOfflineReference） */
function referenceProblem(ref: string): Record<string, string> | null {
  return ref === '' || [...ref].length > 128 ? { reference: '请填写线下凭证号（银行流水号、收据编号等），最多 128 字' } : null
}

/** provider_payment_id 唯一：同一张凭证不能入账两次（另一张单用过回 409，文案照 Go，R74） */
function referenceOwner(ref: string): Order | undefined {
  return orders.find((o) => o.payments.some((p) => p.provider_payment_id === `offline:${ref}`))
}

const day = (v: string | null, end: boolean) => {
  if (!v || !/^\d{4}-\d{2}-\d{2}$/.test(v)) return null
  const t = Date.parse(`${v}T00:00:00Z`)
  return Number.isFinite(t) ? t + (end ? 86_400_000 : 0) : null
}

/** 人工开单与落点预览共用：用户、套餐、价格都要存在且在售（假后端的近似，真实错误形状以后端为准） */
function manualSubject(body: Json) {
  const userId = str(body.user_id)
  const planId = str(body.plan_id)
  if (!userId || !planId) return { ok: false as const, error: err(400, 'bad_request', '缺少租户、操作人、用户或套餐') }
  if (!isUuid(userId) || !isUuid(planId)) return { ok: false as const, error: err(400, 'bad_request', '标识符格式不正确') }
  const user = userStore.find((u) => u.id === userId)
  if (!user) return { ok: false as const, error: NOT_FOUND }
  const plan = plans.find((p) => p.id === planId)
  const price = plan?.prices.find((x) => x.id === str(body.price_id))
  if (!plan || plan.status !== 'active' || !price || price.status !== 'active') return { ok: false as const, error: invalid({ price_id: '价格不存在、已下架或不属于该套餐' }) }
  return { ok: true as const, user, plan, price }
}

/** 一次性套餐：套餐有 kind 时看 kind，没有时看价格档是不是 one_time（套餐目录还在改，两种都认） */
const isOneTime = (plan: unknown, price?: { billing_interval: string }) => (plan as { kind?: string }).kind === 'one_time' || price?.billing_interval === 'one_time'

export const billing: MockModule = {
  routes: {
    // ---- 订单 ---------------------------------------------------------------
    'GET /v1/orders': (ctx) => {
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
      const o = findOrder(ctx.params.id)
      if (!o) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      ctx.send(200, { order: orderDetail(o) })
    },

    'GET /v1/orders/:id/payments': (ctx) => {
      const o = findOrder(ctx.params.id)
      if (!o) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      ctx.send(200, orderHistory(o))
    },

    // 取消：写权限 + 幂等，没挂 reauth（契约）；400 / 409 文案照 billing 域的 Go 原文（中文，R114）
    'POST /v1/orders/:id/cancel': async (ctx) => {
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
        if (['paid', 'fulfilled'].includes(o.status)) return err(409, 'conflict', '订单已支付，不能取消')
        if (o.status !== 'pending_payment') return err(409, 'conflict', `订单当前状态（${o.status}）不能取消`)
        if (o.state_version !== expected) return err(409, 'conflict', '订单状态已变化，请刷新后再操作')
        if (o.payments.some((p) => p.status === 'succeeded')) return err(409, 'conflict', '这张订单已有入账，不能取消')
        const at = new Date().toISOString()
        Object.assign(o, { status: 'cancelled', cancelled_at: at, cancel_reason: reason, state_version: o.state_version + 1, updated_at: at })
        for (const i of o.intents) if (i.status === 'requires_action') Object.assign(i, { status: 'cancelled', updated_at: at })
        return { status: 200, body: { order: { ...out(), already_terminal: false }, already_terminal: false } }
      })
    },

    // 人工开单的落点：purchase.Options 的一支（同款可续 → 不同款过期 30 天内恢复并改成 P → 新开 → 不同款生效中换掉），
    // 只读的 POST：写权限，不挂 reauth，也没有幂等键。形状是 purchase.Placement（选项 + 这份现在的样子 + 落地之后）
    'POST /v1/orders/manual/preview': async (ctx) => {
      const body = await ctx.body()
      if (!body) return ctx.send(BAD_JSON.status, BAD_JSON.body)
      const bad = unknownField(body, ['user_id', 'plan_id', 'price_id', 'entry_subscription_id'])
      if (bad) return ctx.send(bad.status, bad.body)
      // 顺序照 billing.ManualOrderOptions：用户、套餐标识不对 400；价格档、入口订阅标识不对 422 带 fields（F9）；
      // 用户或套餐不存在 404。价格档找不到不报错，按一个月算新到期日（offerPeriod 的 LEFT JOIN）
      const userId = str(body.user_id)
      const planId = str(body.plan_id)
      if (!isUuid(userId) || !isUuid(planId)) return ctx.fail(400, 'bad_request', '标识符格式不正确')
      const priceId = str(body.price_id)
      const entry = str(body.entry_subscription_id)
      const fields: Record<string, string> = {}
      if (priceId && !isUuid(priceId)) fields.price_id = '价格档标识不正确'
      if (entry && !isUuid(entry)) fields.entry_subscription_id = '订阅标识不正确'
      if (Object.keys(fields).length) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', fields)
      const user = userStore.find((u) => u.id === userId)
      const plan = plans.find((p) => p.id === planId)
      if (!user || !plan) return ctx.send(NOT_FOUND.status, NOT_FOUND.body)
      const price = plan.prices.find((x) => x.id === priceId)
      const now = Date.now()
      const cands = candidatesOf(user, now)
      const { options, defaultKey } = planOptions(plan.id, cands, entry, isOneTime(plan, price))
      const info = price ? { interval: price.billing_interval, count: price.interval_count } : { interval: 'month', count: 1 }
      // billing.ManualOrderPreview：站点最低额与每个落点按「待用户支付」开时的应付、是否低于最低额（与建单 422 同一个判定）
      const minPay = price ? siteMinPayment() : 0
      ctx.send(200, {
        options: options.map((o) => {
          const c = cands.find((x) => x.sub.id === o.subscription_id)
          const view = placementView(o, c, info, now)
          const due = price ? manualDue(o.kind, price.unit_amount, typeof view.credit === 'number' ? view.credit : 0) : 0
          return { ...view, due, below_minimum: manualDueBelowMinimum(due, minPay) }
        }),
        default_key: defaultKey,
        min_payment: minPay,
      })
    },

    // 人工开单：写权限 → reauth → 幂等（scope 与用户结账共用 order_create），201 为预写响应（R64 / R74）。
    // 落点由 target 定（preview 给出的选项之一）：续费 / 换套餐落在已有订阅上，链接不变；新开另生成一份。
    // 选项多于一个而没带 target 回 422「请选择这单落到哪一份」，target 不在当前选项里回 422「这个用法现在不能用了…」
    'POST /v1/orders/manual': async (ctx) => {
      if (!ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('order_create', () => {
        const bad = unknownField(body, ['user_id', 'plan_id', 'price_id', 'reason', 'settlement', 'reference', 'target'])
        if (bad) return bad
        const userId = str(body.user_id)
        const planId = str(body.plan_id)
        if (!userId || !planId) return err(400, 'bad_request', '缺少租户、操作人、用户或套餐')
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
        const subject = manualSubject(body)
        if (!subject.ok) return subject.error
        const { user, plan, price } = subject

        // 落点：照 preview 同一套规则重算，target 必须是其中一项
        const now = Date.now()
        const cands = candidatesOf(user, now)
        const { options } = planOptions(plan.id, cands, '', isOneTime(plan, price))
        let chosen: Option | undefined
        if (body.target !== undefined) {
          const t = body.target
          if (typeof t !== 'object' || t === null || Array.isArray(t)) return err(400, 'bad_request', '请求体不是合法的 JSON')
          const badTarget = unknownField(t as Json, ['kind', 'subscription_id'])
          if (badTarget) return badTarget
          const kind = str((t as Json).kind)
          const key = kind === 'new' ? 'new' : `${kind}:${str((t as Json).subscription_id)}`
          chosen = options.find((o) => o.key === key)
          if (!chosen) return err(422, 'validation_failed', '这个用法现在不能用了，请刷新后再选')
        } else if (options.length === 1) {
          chosen = options[0]
        }
        if (!chosen) return err(422, 'validation_failed', '请选择这单落到哪一份')
        const pick: Option = chosen
        const targetSub = pick.subscription_id ? user.subs.find((x) => x.id === pick.subscription_id) : undefined

        const grant = settlement === 'grant'
        const amount = price.unit_amount
        // 换套餐：原套餐没用完的部分先抵新价，抵不完的退进余额（赠送时全额退回）；过期的那份没有可抵的
        const credit = pick.kind === 'change' && targetSub && !pick.expired ? prorationCredit(targetSub, now) : 0
        const payable = grant ? 0 : manualDue(pick.kind, amount, credit)
        const refund = pick.kind !== 'change' ? 0 : grant ? credit : Math.max(credit - amount, 0)
        const paysNow = grant || payable === 0
        if (settlement === 'offline' && payable <= 0) return err(409, 'conflict', '这张订单不需要支付，请改用赠送')
        // 待支付单低于站点最低付款额：用户付不了，后台单不用余额、不免零头，回 422 带 fields.settlement
        // （billing.errManualBelowMinimum），后台落到「结算方式」上
        if (settlement === 'pending' && manualDueBelowMinimum(payable, siteMinPayment())) {
          const msg = '应付金额低于支付渠道的最低付款额，用户无法在线支付，请改用赠送或线下已收款'
          return err(422, 'validation_failed', msg, { settlement: msg })
        }
        if (settlement === 'offline' && referenceOwner(reference)) return err(409, 'conflict', '凭证号已用于其他订单')

        const at = new Date().toISOString()
        const o: Order = {
          id: randomUUID(),
          order_no: nextOrderNo(),
          user_id: user.id,
          user_email: user.email,
          kind: pick.kind === 'renew' ? 'renewal' : pick.kind === 'stack' ? 'stack' : pick.kind === 'change' ? 'upgrade' : 'new',
          status: 'pending_payment',
          subtotal_amount: amount,
          discount_amount: grant ? amount : 0,
          total_amount: payable,
          balance_applied: 0,
          payable_amount: payable,
          paid_amount: 0,
          state_version: 1,
          manual_reason: reason,
          created_by: ctx.user.userId,
          created_by_email: ctx.user.email,
          subscription_id: targetSub?.id ?? null,
          created_at: at,
          updated_at: at,
          paid_at: null,
          expires_at: paysNow ? null : new Date(Date.now() + 30 * 60_000).toISOString(),
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
            },
          ],
          intents: [],
          payments: [],
        }
        if (pick.kind !== 'new' && targetSub) pendingEffects.set(o.id, { kind: pick.kind as 'renew' | 'stack' | 'change', subscriptionId: targetSub.id, credit, refund })
        if (paysNow) {
          o.paid_at = at
          if (!grant) Object.assign(o, { state_version: 3 })
          fulfil(o, at)
        }
        if (settlement === 'offline') {
          o.payments.push(paymentFor(o, 'offline', null, at, `offline:${reference}`))
          Object.assign(o, { paid_amount: payable, paid_at: at, state_version: 3 })
          fulfil(o, at)
        }
        orders.push(o)
        const { discount_amount, id: order_id, order_no, total_amount, balance_applied, payable_amount, status } = o
        // 落成换套餐时多这两项：原套餐的剩余价值，与已经退进余额的部分（待支付的单付款后才退）
        const change = pick.kind === 'change' ? { proration_credit: credit, balance_refund: o.status === 'fulfilled' ? refund : 0 } : {}
        return { status: 201, body: { discount_amount, order_id, order_no, total_amount, balance_applied, payable_amount, status, ...change } }
      })
    },

    // 标记已支付：写权限 → reauth → 幂等；金额从订单读，不收传入（R2 响应 snake_case）
    'POST /v1/orders/:id/mark-paid': async (ctx) => {
      if (!ctx.requireReauth()) return
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
        if (o.status !== 'pending_payment') return err(409, 'conflict', `只有待支付的订单可以标记为已支付，当前状态：${o.status}`)
        // 同一凭证在仍待支付的单上入过账：上一次钱进了挂账（R117）
        if (owner === o) return err(409, 'conflict', `凭证号 ${reference} 已经入过账，不能重复标记`)
        if (o.payable_amount <= 0) return err(409, 'conflict', '这张订单不需要支付')
        if (owner) return err(409, 'conflict', '凭证号已用于其他订单')
        const at = new Date().toISOString()
        const p = paymentFor(o, 'offline', null, at, `offline:${reference}`)
        o.payments.push(p)
        // 续费 / 变更单的订阅已结束：钱照常入账、隔离进挂账，订单不动，回 409 说明去向（R117）
        if (subscriptionEnded.has(o.id)) {
          lateCases.unshift({ id: randomUUID(), case_kind: 'ineligible_subscription', status: 'suspense', amount: o.payable_amount, order: o, received_at: at })
          return err(409, 'conflict', '订阅已结束，款项已转入挂账，可在挂账里转入用户余额')
        }
        Object.assign(o, { paid_amount: o.payable_amount, paid_at: at, state_version: o.state_version + 2, updated_at: at })
        if (o.kind === 'topup') o.status = 'paid'
        else fulfil(o, at)
        return { status: 200, body: { processed: true, already_handled: false, payment_id: p.id, subscription_id: o.subscription_id ?? '', ledger_txn_id: randomUUID() } }
      })
    },

    // 向渠道查单（PAY-009）：写权限 → 幂等，不挂 reauth（与取消同门槛，router_billing.go）。
    // 假渠道的规则是确定的：走过 epay_backup 的单渠道查询失败（503），待支付超过 1 小时的单
    // 像回调丢了、渠道答已付（补记：待支付的结清开通，已取消 / 过期的进挂账），其余答未付；
    // 从没发起过支付回 409。文案与 Go 的 billing/payment_query.go 一致
    'POST /v1/orders/:id/query': async (ctx) => {
      await ctx.idempotent('admin_order_query', () => {
        const o = findOrder(ctx.params.id)
        if (!o) return NOT_FOUND
        const last = o.intents.at(-1)
        if (!last) return err(409, 'conflict', '该订单从未发起过支付，无法向渠道查单')
        if (o.intents.some((i) => i.provider_code === 'epay_backup')) return err(503, 'service_unavailable', '渠道查单失败，请稍后再试')
        const view = (channel: 'paid' | 'unpaid', reconciled: boolean, already: boolean, quarantine?: string) => ({
          status: 200,
          body: { order_id: o.id, order_no: o.order_no, provider_code: last.provider_code, channel_status: channel, reconciled, already_recorded: already, ...(quarantine ? { quarantine_kind: quarantine } : {}), order_status: o.status },
        })
        const pending = o.status === 'pending_payment'
        const ref = `${last.provider_code}:${o.order_no}`
        if (o.payments.some((p) => p.provider_payment_id === ref)) return view('paid', false, true)
        const lostCallback = Date.now() - Date.parse(o.created_at) > 60 * 60_000
        if (!lostCallback) return view('unpaid', false, false)
        const at = new Date().toISOString()
        if (!pending) {
          if (o.status !== 'cancelled' && o.status !== 'expired') return view('unpaid', false, false)
          const p = paymentFor(o, last.provider_code, null, at, ref)
          o.payments.push(p)
          lateCases.unshift({ id: randomUUID(), case_kind: 'released_order', status: 'suspense', amount: o.payable_amount, order: o, received_at: at })
          return view('paid', true, false, 'released_order')
        }
        const active = o.intents.find((i) => i.status === 'requires_action')
        const intent = active ?? intentFor(o, last.provider_code, 'succeeded', at)
        if (active) Object.assign(active, { status: 'succeeded', updated_at: at })
        else o.intents.push(intent)
        o.payments.push(paymentFor(o, last.provider_code, intent, at, ref))
        Object.assign(o, { paid_amount: o.payable_amount, paid_at: at, state_version: o.state_version + 2, updated_at: at })
        if (o.kind === 'topup') o.status = 'paid'
        else fulfil(o, at)
        return view('paid', true, false)
      })
    },

    // ---- 挂账（保留规则 6）-----------------------------------------------------
    'GET /v1/late-payments': (ctx) => {
      const status = (ctx.query.get('status') ?? '').trim()
      if (!LATE.includes(status)) return ctx.fail(400, 'bad_request', '不支持的挂账状态')
      const limitRaw = Number.parseInt(ctx.query.get('limit') ?? '', 10)
      const limit = limitRaw >= 1 && limitRaw <= 100 ? limitRaw : 25
      const offset = Math.max(0, Number.parseInt(ctx.query.get('offset') ?? '', 10) || 0)
      const rows = lateCases
        .filter((c) => !status || c.status === status)
        .sort((a, b) => Number(b.status === 'suspense') - Number(a.status === 'suspense') || b.received_at.localeCompare(a.received_at))
      let pending_amount = 0
      for (const c of lateCases) if (c.status === 'suspense') pending_amount += c.amount
      ctx.send(200, {
        cases: rows.slice(offset, offset + limit).map(lateView),
        total: rows.length,
        pending_amount,
      })
    },

    // 转入余额：reauth → 幂等；后端用 json.NewDecoder，不拒绝多余字段
    'POST /v1/late-payments/:id/apply-to-balance': async (ctx) => {
      if (!ctx.requireReauth()) return
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
        if (u) u.balance += c.amount
        return { status: 200, body: { ledger_txn_id: randomUUID() } }
      })
    },

    // ---- 支付渠道 -------------------------------------------------------------
    'GET /v1/payment-providers': (ctx) => {
      ctx.send(200, { providers: [...providers].sort((a, b) => a.code.localeCompare(b.code)).map(providerView) })
    },

    'GET /v1/payment-settings': (ctx) => {
      const effective = effectiveNotifyBase()
      ctx.send(200, { notify_base_url: paymentSettings.notify_base_url, effective_base: effective, public_base_url: PUBLIC_BASE_URL })
    },

    'PUT /v1/payment-settings': async (ctx) => {
      if (!ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.send(BAD_JSON.status, BAD_JSON.body)
      const bad = unknownField(body, ['notify_base_url'])
      if (bad) return ctx.send(bad.status, bad.body)
      const norm = normalizeNotifyBase(str(body.notify_base_url))
      if ('field' in norm) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { notify_base_url: norm.field })
      paymentSettings.notify_base_url = norm.ok
      const effective = effectiveNotifyBase()
      ctx.send(200, { notify_base_url: paymentSettings.notify_base_url, effective_base: effective, public_base_url: PUBLIC_BASE_URL })
    },

    'GET /v1/settings/purchase': (ctx) => {
      ctx.send(200, { ...purchaseLimits })
    },

    'POST /v1/settings/purchase': async (ctx) => {
      if (!ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.send(BAD_JSON.status, BAD_JSON.body)
      const bad = unknownField(body, ['max_unpaid_orders', 'max_subscriptions'])
      if (bad) return ctx.send(bad.status, bad.body)
      const fields: Record<string, string> = {}
      const u = body.max_unpaid_orders
      if (u === undefined || u === null || typeof u !== 'number' || !Number.isInteger(u)) fields.max_unpaid_orders = '必填'
      else if (u < 0 || u > 50) fields.max_unpaid_orders = '未支付订单上限要在 0 到 50 之间（0 表示不限）'
      const s = body.max_subscriptions
      if (s === undefined || s === null || typeof s !== 'number' || !Number.isInteger(s)) fields.max_subscriptions = '必填'
      else if (s < 0 || s > 50) fields.max_subscriptions = '订阅数上限要在 0 到 50 之间（0 表示不限）'
      if (Object.keys(fields).length) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', fields)
      purchaseLimits.max_unpaid_orders = u as number
      purchaseLimits.max_subscriptions = s as number
      ctx.send(200, { ...purchaseLimits })
    },

    // 启停：reauth，没有幂等；两个布尔漏传按 false
    'POST /v1/payment-providers/:code/toggle': async (ctx) => {
      if (!ctx.requireReauth()) return
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

    // 新建：reauth → 幂等；建出来是「已启用、暂停收新单」，只回凭据是否变更
    'POST /v1/payment-providers': async (ctx) => {
      if (!ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.send(BAD_JSON.status, BAD_JSON.body)
      await ctx.idempotent('payment_provider_create', () => {
        const bad = unknownField(body, ['code', 'adapter', ...PROVIDER_SETTINGS])
        if (bad) return bad
        const adapter = str(body.adapter).trim()
        const parsed = providerSettings(body, adapter || 'epay')
        if ('bad' in parsed) return parsed.bad
        const f = 'fields' in parsed ? { ...parsed.fields } : {}
        const code = str(body.code).trim()
        if (!/^[a-z][a-z0-9_-]{1,31}$/.test(code)) f.code = '编码需为 2–32 位小写字母、数字、- 或 _，以字母开头'
        else if (code === 'offline') f.code = 'offline 是系统内置渠道的编码'
        if (!ONLINE_ADAPTERS.includes(adapter as (typeof ONLINE_ADAPTERS)[number])) f.adapter = '只支持易支付（epay）、BTCPay（btcpay）、CoinPayments（coinpayments）'
        if (!str(body.merchant_id).trim()) f.merchant_id = '必填'
        if (!str(body.key).trim()) f.key = '必填'
        if (adapter === 'btcpay' && !str(body.webhook_secret).trim()) f.webhook_secret = '必填'
        if (Object.keys(f).length || !('ok' in parsed)) return invalid(f)
        if (providers.some((p) => p.code === code)) return err(409, 'conflict', '渠道编码已存在，请换一个')
        const p: Provider = {
          id: randomUUID(),
          code,
          adapter,
          enabled: true,
          accepting_new: false,
          has_credentials: true,
          ...parsed.ok.config,
          min_amount: parsed.ok.min_amount ?? 100,
          fee_fixed: parsed.ok.fee_fixed,
          fee_percent: parsed.ok.fee_percent,
        }
        providers.push(p)
        providerSecrets.set(p.id, {
          merchant_id: parsed.ok.merchant_id,
          key: parsed.ok.key,
          ...(adapter === 'btcpay' && parsed.ok.webhook_secret ? { webhook_secret: parsed.ok.webhook_secret } : {}),
        })
        return { status: 201, body: { id: p.id, code, credentials_changed: true } }
      })
    },

    // 编辑：请求体不收 code / adapter（建后不可改）；商户号、密钥留空 = 不改；offline 与非易支付只读
    'PUT /v1/payment-providers/:code': async (ctx) => {
      if (!ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.send(BAD_JSON.status, BAD_JSON.body)
      await ctx.idempotent('payment_provider_update', () => {
        const bad = unknownField(body, PROVIDER_SETTINGS)
        if (bad) return bad
        const p = providers.find((x) => x.code === ctx.params.code)
        if (!p) return NOT_FOUND
        if (p.code === 'offline' || p.adapter === 'offline') return err(409, 'conflict', '系统内置渠道不可编辑')
        if (!ONLINE_ADAPTERS.includes(p.adapter as (typeof ONLINE_ADAPTERS)[number])) return err(409, 'conflict', '这类渠道不支持在后台编辑')
        const parsed = providerSettings(body, p.adapter)
        if ('bad' in parsed) return parsed.bad
        if ('fields' in parsed) return invalid(parsed.fields)
        const current = providerSecrets.get(p.id) ?? { merchant_id: '', key: '' }
        const next = {
          merchant_id: parsed.ok.merchant_id || current.merchant_id,
          key: parsed.ok.key || current.key,
          webhook_secret: parsed.ok.webhook_secret || current.webhook_secret,
        }
        const missing: Record<string, string> = {}
        if (!next.merchant_id) missing.merchant_id = '该渠道还没有商户号，需填写'
        if (!next.key) missing.key = '该渠道还没有密钥，需填写'
        if (p.adapter === 'btcpay' && !next.webhook_secret) missing.webhook_secret = '该渠道还没有 Webhook 密钥，需填写'
        if (Object.keys(missing).length) return invalid(missing)
        const changed = next.merchant_id !== current.merchant_id || next.key !== current.key || next.webhook_secret !== current.webhook_secret
        const fee_fixed = body.fee_fixed !== undefined ? parsed.ok.fee_fixed : p.fee_fixed
        const fee_percent = body.fee_percent !== undefined ? parsed.ok.fee_percent : p.fee_percent
        Object.assign(p, parsed.ok.config, {
          has_credentials: true,
          min_amount: parsed.ok.min_amount ?? p.min_amount,
          fee_fixed,
          fee_percent,
        })
        providerSecrets.set(p.id, next)
        return { status: 200, body: { id: p.id, code: p.code, credentials_changed: changed } }
      })
    },

    // ---- 收入调整（报表口径，只追加）---------------------------------------------
    'GET /v1/revenue/adjustments': (ctx) => {
      const rows = adjustments
        .sort((a, b) => b.created_at.localeCompare(a.created_at))
        .slice(0, 200)
      ctx.send(200, { adjustments: rows.map(adjustmentView) })
    },

    'POST /v1/revenue/adjustments': async (ctx) => {
      if (!ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.send(BAD_JSON.status, BAD_JSON.body)
      await ctx.idempotent('revenue_adjustment_create', () => {
        const bad = unknownField(body, ['amount', 'reason', 'effective_on'])
        if (bad) return bad
        const f: Record<string, string> = {}
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
      if (!ctx.requireReauth()) return
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
