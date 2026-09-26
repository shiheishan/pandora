/**
 * [INPUT]: 依赖 node:crypto 的 randomUUID / randomInt，依赖 ../types 的 MockModule / MockContext / MockResult / Json，依赖 ./users 的 PLAN_IDS 与 ./plans-store 的 plans（券的适用套餐与套餐卡用固定套餐 id 与种子价格，GET v1/plans 归套餐模块）
 * [OUTPUT]: 对外提供 marketing 模块的假接口 MockModule
 * [POS]: dev/mock/admin 的「营销（后台-06）」假接口，归后台前端二：优惠券、礼品卡（模板 / 批次 / 掩码卡码 / 一次性导出 / 使用记录 / 统计）、佣金与提现。形状、权限、reauth、幂等 scope、校验文案照 api-contract.md（含 R4 R5 R6 R17）与 Go 处理器；请求体按后端 DisallowUnknownFields 拒绝未知字段
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomInt, randomUUID } from 'node:crypto'
import type { Json, MockContext, MockModule, MockResult } from '../types.ts'
import { plans } from './plans-store.ts'
import { PLAN_IDS } from './users.ts'

// ---------------------------------------------------------------------------
// 工具：错误信封（幂等表存完整响应，错误也要按信封入表）、未知字段、时间
// ---------------------------------------------------------------------------
const err = (status: number, code: string, message: string, fields?: Record<string, string>): MockResult => ({
  status,
  body: { error: { code, message, ...(fields ? { fields } : {}) } },
})
const invalid = (fields: Record<string, string>) => err(422, 'validation_failed', '请求参数校验未通过', fields)
const notFound = () => err(404, 'not_found', '资源不存在或无权访问')

/** 与后端 DisallowUnknownFields 一致：多一个字段就 400，前端字段拼错能在假后端上暴露 */
function unknownField(body: Json, allowed: readonly string[]): string | null {
  return Object.keys(body).find((k) => !allowed.includes(k)) ?? null
}

function reply(ctx: MockContext, result: MockResult) {
  ctx.send(result.status, result.body)
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const days = (n: number) => new Date(Date.now() + n * 86_400_000).toISOString()
const int = (v: unknown) => (typeof v === 'number' && Number.isInteger(v) ? v : null)
const text = (v: unknown) => (typeof v === 'string' ? v : '')
const page = (q: URLSearchParams, def: number, max: number) => {
  const limit = Number(q.get('limit'))
  const offset = Number(q.get('offset'))
  return { limit: limit >= 1 && limit <= max ? limit : def, offset: offset > 0 ? Math.floor(offset) : 0 }
}

// ---------------------------------------------------------------------------
// 套餐：GET v1/plans 归套餐模块（plans.ts），这里只借它的固定套餐 id 与种子价格，
// 让券的「适用套餐」与套餐卡在营销页上对得上套餐页
// ---------------------------------------------------------------------------
const PRO = plans.find((p) => p.id === PLAN_IDS[1])!
const PRO_MONTH = PRO.prices.find((p) => p.status === 'active' && p.currency === 'CNY' && p.billing_interval === 'month' && !p.user_group_id)!

// ---------------------------------------------------------------------------
// 优惠券（契约后台-06 · 优惠券）
// ---------------------------------------------------------------------------
interface Coupon {
  id: string
  code: string
  name: string
  discount_type: 'percent' | 'fixed'
  discount_value: number
  currency: string
  max_discount: number | null
  min_order_amount: number
  max_redemptions: number | null
  max_redemptions_per_user: number
  redeemed_count: number
  applicable_plan_ids: string[]
  valid_from: string | null
  valid_until: string | null
  status: string
  created_at: string
}

const COUPON_FIELDS = [
  'code',
  'name',
  'discount_type',
  'discount_value',
  'currency',
  'max_discount',
  'min_order_amount',
  'max_redemptions',
  'max_redemptions_per_user',
  'applicable_plan_ids',
  'valid_from',
  'valid_until',
] as const

const coupon = (c: Partial<Coupon> & Pick<Coupon, 'code' | 'discount_type' | 'discount_value'>, ageDays: number): Coupon => ({
  id: randomUUID(),
  name: c.code,
  currency: 'CNY',
  max_discount: null,
  min_order_amount: 0,
  max_redemptions: null,
  max_redemptions_per_user: 1,
  redeemed_count: 0,
  applicable_plan_ids: [],
  valid_from: null,
  valid_until: null,
  status: 'active',
  created_at: days(-ageDays),
  ...c,
})

const coupons: Coupon[] = [
  coupon({ code: 'AUTUMN26', discount_type: 'percent', discount_value: 2000, max_redemptions: 1000, redeemed_count: 412, valid_until: days(13) }, 1),
  coupon({ code: 'PROYEAR', discount_type: 'fixed', discount_value: 10000, applicable_plan_ids: [PLAN_IDS[1]!], max_redemptions: 200, redeemed_count: 38, valid_until: days(98), min_order_amount: 50000 }, 3),
  coupon({ code: 'WELCOME', discount_type: 'percent', discount_value: 1000, max_discount: 3000, redeemed_count: 2204 }, 120),
  coupon({ code: 'KOLA7Q2M8X', name: 'KOL 渠道 09', discount_type: 'percent', discount_value: 1500, max_redemptions: 1, redeemed_count: 1, valid_until: days(38) }, 5),
  coupon({ code: 'KOLB3ZPK4T', name: 'KOL 渠道 09', discount_type: 'percent', discount_value: 1500, max_redemptions: 1, valid_until: days(38) }, 5),
  coupon({ code: 'SUMMER25', discount_type: 'percent', discount_value: 2500, max_redemptions: 1000, redeemed_count: 980, valid_until: days(-24), status: 'paused' }, 90),
  coupon({ code: 'SPRING', discount_type: 'fixed', discount_value: 500, valid_until: days(-150), status: 'expired' }, 200),
  coupon({ code: 'FLASH100', discount_type: 'fixed', discount_value: 2000, max_redemptions: 100, redeemed_count: 100, status: 'exhausted' }, 30),
]

const redemptions = new Map<string, Array<{ email: string; order_no: string; discount: number; currency: string; at: string; reverted: boolean }>>()
coupons.forEach((c) => {
  const n = Math.min(c.redeemed_count, 3)
  redemptions.set(
    c.id,
    Array.from({ length: n }, (_, i) => ({
      email: ['zhang.wei@qq.com', 'mei.x@outlook.com', 'tomato@sina.com'][i]!,
      order_no: `PD-2609-${3312 + i}`,
      discount: [11980, 1180, 2780][i]!,
      currency: 'CNY',
      at: days(-i - 0.2),
      reverted: i === 2,
    })),
  )
})

/** normalizeCouponReq 的复刻：返回错误或规范化后的字段 */
function normalizeCoupon(body: Json, needCode: boolean): MockResult | Omit<Coupon, 'id' | 'redeemed_count' | 'status' | 'created_at'> {
  const code = text(body.code).trim().toUpperCase()
  const fields: Record<string, string> = {}
  if (needCode && code === '') fields.code = '必填'
  const type = body.discount_type
  const value = int(body.discount_value) ?? 0
  if (type === 'percent') {
    if (value < 1 || value > 10000) fields.discount_value = '折扣需在 0.01% 到 100% 之间'
  } else if (type === 'fixed') {
    if (value < 1) fields.discount_value = '立减金额需大于 0'
  } else {
    fields.discount_type = '只支持 percent 或 fixed'
  }
  if (Object.keys(fields).length) return invalid(fields)
  const time = (v: unknown): string | null | 'bad' => {
    const s = text(v).trim()
    if (s === '') return null
    const t = new Date(/Z|[+-]\d\d:\d\d$/.test(s) ? s : `${s}:00`)
    return Number.isNaN(t.getTime()) ? 'bad' : t.toISOString()
  }
  const from = time(body.valid_from)
  const until = time(body.valid_until)
  if (from === 'bad' || until === 'bad') return err(422, 'validation_failed', '时间格式不正确')
  if (from && until && until <= from) return err(422, 'validation_failed', '结束时间必须晚于开始时间')
  const perUser = int(body.max_redemptions_per_user)
  return {
    code,
    name: text(body.name) || code,
    discount_type: type as Coupon['discount_type'],
    discount_value: value,
    currency: text(body.currency) || 'CNY',
    max_discount: int(body.max_discount),
    min_order_amount: int(body.min_order_amount) ?? 0,
    max_redemptions: int(body.max_redemptions),
    max_redemptions_per_user: perUser && perUser > 0 ? perUser : 1,
    applicable_plan_ids: Array.isArray(body.applicable_plan_ids) ? body.applicable_plan_ids.map(String) : [],
    valid_from: from,
    valid_until: until,
  }
}

const ALPHABET = 'ABCDEFGHJKMNPQRSTUVWXYZ23456789'
const randomCode = (n: number) => Array.from({ length: n }, () => ALPHABET[randomInt(ALPHABET.length)]).join('')
const safePrefix = (p: string) => p.length <= 8 && /^[A-Z0-9]*$/.test(p)

// ---------------------------------------------------------------------------
// 礼品卡（契约后台-06 · 礼品卡，R4 R17）
// ---------------------------------------------------------------------------
interface Rewards {
  balance?: number
  traffic_bytes?: number
  expire_days?: number
  reset_quota?: boolean
  plan_id?: string
  price_id?: string
  pool?: Array<{ label: string; weight: number; balance?: number; traffic_bytes?: number; expire_days?: number }>
}
interface Template {
  id: string
  name: string
  description: string
  type: 'general' | 'plan' | 'mystery'
  status: 'active' | 'paused' | 'archived'
  rewards: Rewards
  conditions: { new_user_only?: boolean; paid_user_only?: boolean; allowed_plan_ids?: string[]; require_invite?: boolean }
  limits: { max_use_per_user?: number; cooldown_hours?: number }
  theme_color: string
  created_at: string
}
interface Code {
  id: string
  code: string
  status: 'unused' | 'used' | 'disabled' | 'expired'
  batch_id: string
  expires_at?: string
  used_email?: string
  used_at?: string
  created_at: string
  template_id: string
}
interface Batch {
  id: string
  template_id: string
  prefix: string
  count: number
  expires_at: string | null
  created_by_email: string | null
  created_at: string
  exported_at: string | null
  exported_by_email: string | null
}

const GB = 1024 ** 3
const templates: Template[] = [
  { id: randomUUID(), name: '100 元余额卡', description: '渠道合作用', type: 'general', status: 'active', rewards: { balance: 10000 }, conditions: {}, limits: { max_use_per_user: 1 }, theme_color: '#b9442b', created_at: days(-40) },
  { id: randomUUID(), name: '专业版月卡', description: '', type: 'plan', status: 'active', rewards: { plan_id: PRO.id, price_id: PRO_MONTH.id }, conditions: { new_user_only: true }, limits: {}, theme_color: '#34507c', created_at: days(-30) },
  { id: randomUUID(), name: '流量加油包', description: '当期有效', type: 'general', status: 'active', rewards: { traffic_bytes: 100 * GB }, conditions: {}, limits: { cooldown_hours: 24 }, theme_color: '#1f7a4f', created_at: days(-25) },
  {
    id: randomUUID(),
    name: '国庆盲盒',
    description: '',
    type: 'mystery',
    status: 'paused',
    rewards: { pool: [{ label: '¥5 余额', weight: 80, balance: 500 }, { label: '50 GB 流量', weight: 18, traffic_bytes: 50 * GB }, { label: '30 天会员', weight: 2, expire_days: 30 }] },
    conditions: { require_invite: true },
    limits: {},
    theme_color: '#9a5c00',
    created_at: days(-3),
  },
]
const batches: Batch[] = []
const codes: Code[] = []
const usages: Array<{ template_id: string; code_id: string; user_email: string; granted: Json; prize_label?: string; redeemed_at: string }> = []

const mask = (code: string) => (code.length <= 8 ? '•'.repeat(code.length) : code.slice(0, -8) + '•'.repeat(8))

function mintBatch(t: Template, count: number, prefix: string, expiresAt: string | null, by: string | null, ageDays = 0): Batch {
  const batch: Batch = { id: randomUUID(), template_id: t.id, prefix, count, expires_at: expiresAt, created_by_email: by, created_at: days(-ageDays), exported_at: null, exported_by_email: null }
  batches.unshift(batch)
  for (let i = 0; i < count; i++) {
    codes.push({ id: randomUUID(), code: prefix + randomCode(12), status: 'unused', batch_id: batch.id, ...(expiresAt ? { expires_at: expiresAt } : {}), created_at: batch.created_at, template_id: t.id })
  }
  return batch
}

// 存量批次按 D-C-4 一律记为已导出；再各兑出几张、停用一张
;[
  [templates[0]!, 40, 'GC', 32],
  [templates[1]!, 24, 'PRO', 12],
  [templates[2]!, 30, '', 5],
].forEach(([t, n, prefix, age]) => {
  const b = mintBatch(t as Template, n as number, prefix as string, null, 'linzhou@pandora.dev', age as number)
  b.exported_at = b.created_at
  b.exported_by_email = 'linzhou@pandora.dev'
  codes
    .filter((c) => c.batch_id === b.id)
    .slice(0, 5)
    .forEach((c, i) => {
      if (i === 4) return void (c.status = 'disabled')
      c.status = 'used'
      c.used_email = ['zhang.wei@qq.com', 'grace.h@icloud.com', 'wu.qing@163.com', 'lin.xiao@foxmail.com'][i]!
      c.used_at = days(-i - 0.4)
      const t2 = t as Template
      usages.push({ template_id: t2.id, code_id: c.id, user_email: c.used_email, granted: grantedOf(t2), redeemed_at: c.used_at })
    })
})

function grantedOf(t: Template): Json {
  if (t.type === 'plan') return {}
  const { balance, traffic_bytes, expire_days, reset_quota } = t.rewards
  return { ...(balance ? { balance } : {}), ...(traffic_bytes ? { traffic_bytes } : {}), ...(expire_days ? { expire_days } : {}), ...(reset_quota ? { reset_quota } : {}) }
}

const templateView = (t: Template) => {
  const own = codes.filter((c) => c.template_id === t.id)
  return { ...t, code_total: own.length, code_used: own.filter((c) => c.status === 'used').length }
}
const batchView = (b: Batch) => {
  const own = codes.filter((c) => c.batch_id === b.id)
  return {
    ...b,
    template_name: templates.find((t) => t.id === b.template_id)?.name ?? '',
    used: own.filter((c) => c.status === 'used').length,
    disabled: own.filter((c) => c.status === 'disabled').length,
  }
}
const codeView = (c: Code) => {
  const { code, ...rest } = c
  return { ...rest, code_masked: mask(code) }
}

/** validateRewards 的复刻 */
function rewardsError(type: string, r: Rewards): string | null {
  if (type === 'general') {
    if (!(r.balance! > 0) && !(r.traffic_bytes! > 0) && !(r.expire_days! > 0) && !r.reset_quota) return '通用卡至少要送一样东西：余额、流量、延长到期或重置流量'
    if (r.balance! < 0 || r.traffic_bytes! < 0 || r.expire_days! < 0) return '奖励数值不能为负'
    if (r.expire_days! > 3650) return '延长天数不能超过 3650 天'
    return null
  }
  if (type === 'plan') {
    if (!r.plan_id) return '套餐卡必须指定套餐'
    if (!UUID.test(r.plan_id)) return '套餐标识格式不正确'
    return null
  }
  if (type === 'mystery') {
    const pool = r.pool ?? []
    if (pool.length < 2) return '盲盒至少要有 2 个奖品，否则它就是一张普通卡'
    if (pool.length > 50) return '盲盒奖品不能超过 50 个'
    for (const [i, p] of pool.entries()) {
      if (!p.label?.trim()) return `第 ${i + 1} 个奖品缺少名称 —— 用户中奖后要看到它`
      if (!(p.weight > 0)) return `第 ${i + 1} 个奖品的权重必须大于 0（权重为 0 等于永远抽不到，不如直接删掉）`
      if (!(p.balance! > 0) && !(p.traffic_bytes! > 0) && !(p.expire_days! > 0)) return `第 ${i + 1} 个奖品什么都不送`
    }
    return null
  }
  return '不支持的卡型'
}

const REWARD_FIELDS = ['balance', 'traffic_bytes', 'expire_days', 'reset_quota', 'plan_id', 'price_id', 'pool']
const PRIZE_FIELDS = ['label', 'weight', 'balance', 'traffic_bytes', 'expire_days']
const CONDITION_FIELDS = ['new_user_only', 'paid_user_only', 'allowed_plan_ids', 'require_invite']
const LIMIT_FIELDS = ['max_use_per_user', 'cooldown_hours']

function csvCell(v: string) {
  return /[",\r\n]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v
}

// ---------------------------------------------------------------------------
// 佣金与提现（契约后台-06 · 佣金与提现，R4 R6；overview 的 total_earned / invited_users / scope
// 与 config 的 scope 是待补·后端，这里照契约形状给出）
// ---------------------------------------------------------------------------
const commission = { rate_percent: 20, freeze_days: 7, min_withdraw: 10000, scope: 'first_order' as 'first_order' | 'every_order' }
interface Withdrawal {
  id: string
  email: string
  user_id: string
  amount: number
  currency: string
  status: string
  payout_detail: string
  reject_reason: string
  requested_at: string
  completed_at: string | null
  earned_total: number
}
const withdrawal = (email: string, amount: number, status: string, payout: string, ageDays: number, extra: Partial<Withdrawal> = {}): Withdrawal => ({
  id: randomUUID(),
  email,
  user_id: randomUUID(),
  amount,
  currency: 'CNY',
  status,
  payout_detail: payout,
  reject_reason: '',
  requested_at: days(-ageDays),
  completed_at: null,
  earned_total: amount * 3,
  ...extra,
})
const withdrawals: Withdrawal[] = [
  withdrawal('chen.l@gmail.com', 42000, 'requested', '支付宝 · 138****2201', 0.1),
  withdrawal('dora@proton.me', 128000, 'reviewing', 'USDT · TXk9…a1Qe', 0.5),
  withdrawal('m.chen@gmail.com', 30000, 'approved', '支付宝 · 186****0932', 1.2),
  withdrawal('yx_agent@163.com', 260000, 'paid', '银行卡 · 招商 **** 7781', 4, { completed_at: days(-3) }),
  withdrawal('sp4m@mail.ru', 18000, 'rejected', '支付宝 · 150****1188', 5, { reject_reason: '邀请关系异常，多个账号共用同一设备', earned_total: 18000 }),
  withdrawal('late@pay.io', 50000, 'failed', '银行卡 · 建设 **** 1024', 7),
]

// ---------------------------------------------------------------------------
// 路由
// ---------------------------------------------------------------------------
export const marketing: MockModule = {
  routes: {
    // --- 优惠券 -----------------------------------------------------------
    'GET /v1/coupons': (ctx) => {
      if (!ctx.requirePermission('marketing.coupon.read')) return
      const q = text(ctx.query.get('q')).toLowerCase()
      const status = ctx.query.get('status') ?? ''
      const { limit, offset } = page(ctx.query, 25, 200)
      const hit = coupons
        .filter((c) => (!q || c.code.toLowerCase().includes(q) || c.name.toLowerCase().includes(q)) && (!status || c.status === status))
        .sort((a, b) => b.created_at.localeCompare(a.created_at))
      ctx.send(200, { coupons: hit.slice(offset, offset + limit).map((c) => ({ ...c, discounted_total: c.redeemed_count * 1200 })), total: hit.length })
    },
    'GET /v1/coupons/:id/redemptions': (ctx) => {
      if (!ctx.requirePermission('marketing.coupon.read') || !ctx.requirePermission('billing.order.read')) return
      if (!UUID.test(ctx.params.id!)) return ctx.fail(500, 'internal_error', '服务暂时不可用，请稍后重试')
      ctx.send(200, { redemptions: redemptions.get(ctx.params.id!) ?? [] })
    },
    'POST /v1/coupons': async (ctx) => {
      if (!ctx.requirePermission('marketing.coupon.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const extra = unknownField(body, COUPON_FIELDS)
      if (extra) return ctx.fail(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
      const norm = normalizeCoupon(body, true)
      if ('status' in norm) return reply(ctx, norm)
      if (coupons.some((c) => c.code === norm.code)) return ctx.fail(409, 'conflict', '这个优惠码已经存在')
      const c: Coupon = { ...norm, id: randomUUID(), redeemed_count: 0, status: 'active', created_at: new Date().toISOString() }
      coupons.unshift(c)
      ctx.send(200, { id: c.id, code: c.code })
    },
    'POST /v1/coupons/batch': async (ctx) => {
      if (!ctx.requirePermission('marketing.coupon.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('coupon_batch_generate', () => {
        const extra = unknownField(body, [...COUPON_FIELDS.filter((f) => f !== 'code'), 'count', 'prefix'])
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        const count = int(body.count) ?? 0
        const prefix = text(body.prefix).trim().toUpperCase()
        const name = text(body.name).trim()
        if (count < 1 || count > 1000) return invalid({ count: '一次生成 1 到 1000 张。要更多就分批 —— 一次几万张会把事务拖很久，而且生成出来的码也没法在一个页面里交接' })
        if (!safePrefix(prefix)) return invalid({ prefix: '前缀只能用大写字母和数字，最多 8 位' })
        if (!name) return invalid({ name: '批量生成必须填活动名称，之后要靠它把整批券找回来' })
        const norm = normalizeCoupon({ ...body, max_redemptions: body.max_redemptions === undefined ? 1 : body.max_redemptions }, false)
        if ('status' in norm) return norm
        const made: string[] = []
        for (let i = 0; i < count; i++) {
          const code = prefix + randomCode(10)
          made.push(code)
          coupons.unshift({ ...norm, code, name, id: randomUUID(), redeemed_count: 0, status: 'active', created_at: new Date().toISOString() })
        }
        return { status: 200, body: { count, codes: made, name } }
      })
    },
    'POST /v1/coupons/:id/status': async (ctx) => {
      if (!ctx.requirePermission('marketing.coupon.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      if (body.status !== 'active' && body.status !== 'paused') return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { status: '只能是 active 或 paused' })
      const c = coupons.find((x) => x.id === ctx.params.id)
      if (!c) return reply(ctx, notFound())
      c.status = body.status
      ctx.send(200, { ok: true })
    },

    // --- 礼品卡 -----------------------------------------------------------
    'GET /v1/gift-cards': (ctx) => {
      if (!ctx.requirePermission('marketing.giftcard.read')) return
      ctx.send(200, { templates: templates.filter((t) => t.status !== 'archived').map(templateView) })
    },
    'GET /v1/gift-cards/stats': (ctx) => {
      if (!ctx.requirePermission('marketing.giftcard.read')) return
      const used = codes.filter((c) => c.status === 'used')
      const sum = (pick: (t: Template) => number, list: Code[]) => list.reduce((s, c) => s + pick(templates.find((t) => t.id === c.template_id)!), 0)
      const general = (t: Template) => (t.type === 'general' ? (t.rewards.balance ?? 0) : 0)
      ctx.send(200, {
        templates: templates.filter((t) => t.status !== 'archived').length,
        codes_total: codes.length,
        codes_used: used.length,
        codes_unused: codes.filter((c) => c.status === 'unused').length,
        balance_out: sum(general, used),
        traffic_out: sum((t) => (t.type === 'general' ? (t.rewards.traffic_bytes ?? 0) : 0), used),
        balance_issued: sum(general, codes),
      })
    },
    'GET /v1/gift-cards/codes': (ctx) => {
      if (!ctx.requirePermission('marketing.giftcard.read')) return
      const status = ctx.query.get('status') ?? ''
      if (!['', 'unused', 'used', 'disabled', 'expired'].includes(status)) return ctx.fail(400, 'bad_request', '不支持的卡密状态')
      const templateId = ctx.query.get('template_id') ?? ''
      const batchId = ctx.query.get('batch_id') ?? ''
      if ((templateId && !UUID.test(templateId)) || (batchId && !UUID.test(batchId))) return ctx.fail(400, 'bad_request', '标识符格式不正确')
      const { limit, offset } = page(ctx.query, 50, 5000)
      const hit = codes.filter((c) => (!status || c.status === status) && (!templateId || c.template_id === templateId) && (!batchId || c.batch_id === batchId))
      ctx.send(200, { codes: hit.slice(offset, offset + limit).map(codeView), total: hit.length })
    },
    'GET /v1/gift-cards/batches': (ctx) => {
      if (!ctx.requirePermission('marketing.giftcard.read')) return
      const templateId = ctx.query.get('template_id') ?? ''
      if (templateId && !UUID.test(templateId)) return ctx.fail(400, 'bad_request', '标识符格式不正确')
      const { limit, offset } = page(ctx.query, 50, 200)
      const hit = batches.filter((b) => !templateId || b.template_id === templateId)
      ctx.send(200, { items: hit.slice(offset, offset + limit).map(batchView), total: hit.length })
    },
    'POST /v1/gift-cards/batches/:id/export': async (ctx) => {
      if (!ctx.requirePermission('marketing.giftcard.write') || !ctx.requireReauth()) return
      const raw = await ctx.body()
      // 后端解码 struct{}：空体与多字段都 400
      if (!raw || ctx.req.headers['content-length'] === '0' || Object.keys(raw).length > 0) return ctx.fail(400, 'bad_request', '请求体必须是 {}')
      await ctx.idempotent('giftcard_batch_export', () => {
        const b = batches.find((x) => x.id === ctx.params.id)
        if (!b) return notFound()
        if (b.exported_at) return err(409, 'conflict', '该批次已导出，完整卡码不可再次获取')
        b.exported_at = new Date().toISOString()
        b.exported_by_email = ctx.user.email
        const name = templates.find((t) => t.id === b.template_id)?.name ?? ''
        const rows = codes.filter((c) => c.batch_id === b.id).map((c) => [c.code, c.status, c.expires_at ? c.expires_at.slice(0, 16).replace('T', ' ') : '', name].map(csvCell).join(','))
        return {
          status: 200,
          raw: {
            contentType: 'text/csv; charset=utf-8',
            text: `\uFEFF卡密,状态,有效期,模板\n${rows.join('\n')}\n`,
            headers: { 'Content-Disposition': `attachment; filename="gift-codes-${b.id.slice(0, 8)}.csv"` },
          },
        }
      })
    },
    'GET /v1/gift-cards/usages': (ctx) => {
      if (!ctx.requirePermission('marketing.giftcard.read')) return
      const templateId = ctx.query.get('template_id') ?? ''
      if (templateId && !UUID.test(templateId)) return ctx.fail(400, 'bad_request', '标识符格式不正确')
      const rows = usages
        .filter((u) => !templateId || u.template_id === templateId)
        .sort((a, b) => b.redeemed_at.localeCompare(a.redeemed_at))
        .slice(0, 200)
        .map((u) => ({
          template_name: templates.find((t) => t.id === u.template_id)?.name ?? '',
          code_masked: mask(codes.find((c) => c.id === u.code_id)?.code ?? ''),
          user_email: u.user_email,
          granted: u.granted,
          ...(u.prize_label ? { prize_label: u.prize_label } : {}),
          redeemed_at: u.redeemed_at,
        }))
      ctx.send(200, { usages: rows })
    },
    'POST /v1/gift-cards': async (ctx) => {
      if (!ctx.requirePermission('marketing.giftcard.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const r = (body.rewards ?? {}) as Rewards & Json
      const c = (body.conditions ?? {}) as Template['conditions'] & Json
      const l = (body.limits ?? {}) as Template['limits'] & Json
      const extra =
        unknownField(body, ['id', 'name', 'description', 'type', 'status', 'rewards', 'conditions', 'limits', 'theme_color']) ??
        unknownField(r, REWARD_FIELDS) ??
        unknownField(c, CONDITION_FIELDS) ??
        unknownField(l, LIMIT_FIELDS) ??
        (r.pool ?? []).map((p) => unknownField(p as unknown as Json, PRIZE_FIELDS)).find(Boolean) ??
        null
      if (extra) return ctx.fail(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
      const name = text(body.name).trim()
      if (name.length < 1 || [...name].length > 120) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { name: '名称必填，不超过 120 字' })
      const status = (text(body.status) || 'active') as Template['status']
      if (!['active', 'paused', 'archived'].includes(status)) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { status: '状态只能是 active / paused / archived' })
      const type = text(body.type) as Template['type']
      const rewardErr = rewardsError(type, r)
      if (rewardErr) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { rewards: rewardErr })
      if ((l.max_use_per_user ?? 0) < 0 || (l.cooldown_hours ?? 0) < 0) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { limits: '限制值不能为负' })
      if (c.new_user_only && c.paid_user_only) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { conditions: '「仅新用户」和「仅付费用户」互斥，同时勾选等于谁都不能兑' })
      const id = text(body.id)
      if (templates.some((t) => t.name === name && t.id !== id)) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { name: '已经有同名的礼品卡了' })
      const fields = { name, description: text(body.description).trim(), status, rewards: r, conditions: c, limits: l, theme_color: text(body.theme_color) }
      if (id) {
        const t = templates.find((x) => x.id === id)
        if (!t) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
        if (t.type !== type) return ctx.fail(409, 'conflict', '不能修改卡型：已经发出去的码是按原卡型生成的')
        Object.assign(t, fields)
        return ctx.send(200, { template: templateView(t) })
      }
      const t: Template = { id: randomUUID(), type, ...fields, created_at: new Date().toISOString() }
      templates.push(t)
      ctx.send(200, { template: templateView(t) })
    },
    'POST /v1/gift-cards/:id/codes': async (ctx) => {
      if (!ctx.requirePermission('marketing.giftcard.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('giftcard_codes_generate', () => {
        const extra = unknownField(body, ['count', 'prefix', 'expires_at'])
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        const count = int(body.count) ?? 0
        const prefix = text(body.prefix)
        if (count < 1 || count > 5000) return invalid({ count: '一次生成 1 到 5000 个。要更多就分批 —— 单次几万个会把事务拖很久' })
        if (!safePrefix(prefix)) return invalid({ prefix: '前缀只能用大写字母和数字，最多 8 位' })
        let expires: string | null = null
        if (text(body.expires_at)) {
          const at = new Date(text(body.expires_at))
          if (Number.isNaN(at.getTime()) || !/T.*(Z|[+-]\d\d:\d\d)$/.test(text(body.expires_at))) return invalid({ expires_at: '时间格式不正确' })
          if (at.getTime() < Date.now()) return invalid({ expires_at: '有效期不能设在过去' })
          expires = at.toISOString()
        }
        const t = templates.find((x) => x.id === ctx.params.id)
        if (!t) return notFound()
        if (t.status === 'archived') return err(409, 'conflict', '已归档的礼品卡不能再生成新码')
        const b = mintBatch(t, count, prefix, expires, ctx.user.email)
        const sample = codes.filter((c) => c.batch_id === b.id).slice(0, 4).map((c) => c.code)
        return { status: 200, body: { batch_id: b.id, count, sample, batch: batchView(b) } }
      })
    },
    'POST /v1/gift-cards/codes/:id/toggle': async (ctx) => {
      if (!ctx.requirePermission('marketing.giftcard.write')) return
      const body = await ctx.body()
      if (!body || typeof body.disabled !== 'boolean') return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const c = codes.find((x) => x.id === ctx.params.id)
      if (!c) return reply(ctx, notFound())
      if (c.status === 'used') return ctx.fail(409, 'conflict', '这个码已经被兑换了，不能停用。要收回权益请走调账或工单')
      if (c.status === 'expired') return ctx.fail(409, 'conflict', '这个码已经过期了')
      const from = body.disabled ? 'unused' : 'disabled'
      if (c.status !== from) return ctx.fail(409, 'conflict', '这个码当前的状态不需要该操作')
      c.status = body.disabled ? 'disabled' : 'unused'
      ctx.send(200, { disabled: body.disabled })
    },

    // --- 佣金与提现 ---------------------------------------------------------
    'GET /v1/commission/overview': (ctx) => {
      if (!ctx.requirePermission('marketing.commission.read')) return
      const waiting = withdrawals.filter((w) => ['requested', 'reviewing', 'approved', 'processing'].includes(w.status)).length
      ctx.send(200, {
        pending: 390400,
        available: 1528000,
        paid_out: withdrawals.filter((w) => w.status === 'paid').reduce((s, w) => s + w.amount, 3166000 - 260000),
        this_month: 482100,
        entries: 1840,
        need_review: 3,
        waiting_withdrawals: waiting,
        rate_percent: commission.rate_percent,
        freeze_days: commission.freeze_days,
        min_withdraw: commission.min_withdraw,
        total_earned: 4821000,
        invited_users: 2318,
        scope: commission.scope,
      })
    },
    'GET /v1/withdrawals': (ctx) => {
      if (!ctx.requirePermission('marketing.commission.read')) return
      const status = ctx.query.get('status') ?? ''
      const rows = withdrawals.filter((w) => !status || w.status === status).sort((a, b) => b.requested_at.localeCompare(a.requested_at))
      ctx.send(200, { withdrawals: rows.slice(0, 200) })
    },
    'POST /v1/withdrawals/:id/review': async (ctx) => {
      if (!ctx.requirePermission('marketing.withdrawal.approve') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const extra = unknownField(body, ['action', 'reason'])
      if (extra) return ctx.fail(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
      const reason = text(body.reason).trim()
      if (body.action !== 'approve' && body.action !== 'reject') return ctx.fail(422, 'validation_failed', '操作只能是 approve 或 reject')
      if (body.action === 'reject' && !reason) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { reason: '拒绝时必须填写理由' })
      if (body.action === 'approve' && reason) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { reason: '批准时不能填写拒绝理由' })
      const w = withdrawals.find((x) => x.id === ctx.params.id)
      if (!w) return reply(ctx, notFound())
      if (w.status !== 'requested' && w.status !== 'reviewing') return ctx.fail(409, 'conflict', '该提现申请已被处理过')
      w.status = body.action === 'approve' ? 'approved' : 'rejected'
      if (body.action === 'reject') {
        w.reject_reason = reason
        w.completed_at = new Date().toISOString()
      }
      ctx.send(200, { status: w.status })
    },
    'POST /v1/withdrawals/:id/paid': async (ctx) => {
      if (!ctx.requirePermission('marketing.withdrawal.approve') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('commission_withdrawal_mark_paid', () => {
        const extra = unknownField(body, ['payout_reference'])
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        if (!text(body.payout_reference).trim()) return invalid({ payout_reference: '请填写转账流水号，日后对账要用' })
        const w = withdrawals.find((x) => x.id === ctx.params.id)
        if (!w) return notFound()
        if (w.status !== 'approved') return err(409, 'conflict', '只有已批准的提现才能标记为已打款')
        w.status = 'paid'
        w.completed_at = new Date().toISOString()
        return { status: 200, body: { status: 'paid' } }
      })
    },
    'POST /v1/commission/config': async (ctx) => {
      if (!ctx.requirePermission('marketing.commission.write') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const extra = unknownField(body, ['rate_percent', 'freeze_days', 'min_withdraw', 'scope'])
      if (extra) return ctx.fail(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
      const fields: Record<string, string> = {}
      const rate = body.rate_percent === undefined ? undefined : int(body.rate_percent)
      const freeze = body.freeze_days === undefined ? undefined : int(body.freeze_days)
      const min = body.min_withdraw === undefined ? undefined : int(body.min_withdraw)
      if (rate === null || (rate !== undefined && (rate < 0 || rate > 50))) fields.rate_percent = '佣金比例需在 0 到 50 之间'
      if (freeze === null || (freeze !== undefined && (freeze < 0 || freeze > 90))) fields.freeze_days = '冻结天数需在 0 到 90 之间'
      if (min === null || (min !== undefined && min < 0)) fields.min_withdraw = '最低提现金额不能为负'
      if (body.scope !== undefined && body.scope !== 'first_order' && body.scope !== 'every_order') fields.scope = '只能是 first_order 或 every_order'
      if (Object.keys(fields).length) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', fields)
      if (rate != null) commission.rate_percent = rate
      if (freeze != null) commission.freeze_days = freeze
      if (min != null) commission.min_withdraw = min
      if (body.scope !== undefined) commission.scope = body.scope as typeof commission.scope
      ctx.send(200, { ok: true })
    },
  },
}
