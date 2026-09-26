/**
 * [INPUT]: 依赖 ../../../core/format 的 formatMoney，依赖 ./schemas 的类型
 * [OUTPUT]: 对外提供营销页的纯函数：金额 / 百分比输入换算、优惠文案、券与卡码与提现的状态映射、礼品卡面额与兑换内容、批次显示名、兑换率，以及三张表单（优惠券、礼品卡模板、生码、佣金设置）到请求体的构建与前端校验
 * [POS]: admin/screens/marketing 的逻辑层，组件只负责渲染与交互；映射全部取自 api-contract.md 后台-06 条目的「设计 / 映射」行，marketing.test.ts 逐条守住
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { formatMoney } from '../../../core/format'
import type { Batch, CommissionOverview, Coupon, GiftTemplate, Plan, Rewards, Usage } from './schemas'

// ---------------------------------------------------------------------------
// 输入换算：界面填元与百分比，接口收分与万分比。只认最多两位小数，
// 否则返回 null 让表单报错——静默四舍五入会让「立减 9.999」变成别的数。
// ---------------------------------------------------------------------------
const DECIMAL = /^\d+(\.\d{1,2})?$/

/** '10' → 1000，'10.5' → 1050；空串、负数、三位小数返回 null */
export function yuanToMinor(input: string): number | null {
  const s = input.trim()
  if (!DECIMAL.test(s)) return null
  const [units, cents = ''] = s.split('.')
  return Number(units) * 100 + Number(cents.padEnd(2, '0'))
}

/** 1050 → '10.50'，1000 → '10' */
export function minorToYuan(minor: number): string {
  const units = Math.trunc(minor / 100)
  const cents = Math.abs(minor % 100)
  return cents ? `${units}.${String(cents).padStart(2, '0')}` : String(units)
}

/** 百分比输入到万分比：'15' → 1500，'15.5' → 1550 */
export const percentToBasisPoints = yuanToMinor

/** 万分比到百分比文字：1550 → '15.5' */
export function basisPointsToPercent(bp: number): string {
  return String(bp / 100)
}

/** 非负整数输入：'' → null（未填），'12' → 12，'1.5' / '-1' → NaN（非法） */
export function parseCount(input: string): number | null {
  const s = input.trim()
  if (s === '') return null
  return /^\d+$/.test(s) ? Number(s) : Number.NaN
}

export const GB = 1024 ** 3

/** 字节到「100 GB」：整 GB 不带小数，其余保留一位 */
export function formatGB(bytes: number): string {
  const gb = bytes / GB
  return `${Number.isInteger(gb) ? gb : Number(gb.toFixed(1))} GB`
}

/** datetime-local 的「YYYY-MM-DDTHH:MM」按浏览器时区转成 RFC3339（带 Z）；空串 → '' */
export function localInputToIso(value: string): string {
  if (!value) return ''
  const t = new Date(value)
  return Number.isNaN(t.getTime()) ? '' : t.toISOString()
}

const pad = (n: number) => String(n).padStart(2, '0')
/** 本地日期 YYYY-MM-DD */
export function formatDate(iso: string): string {
  const t = new Date(iso)
  return `${t.getFullYear()}-${pad(t.getMonth() + 1)}-${pad(t.getDate())}`
}
/** 本地 MM-DD HH:mm（列表里的时间，设计稿口径） */
export function formatDateTime(iso: string): string {
  const t = new Date(iso)
  return `${pad(t.getMonth() + 1)}-${pad(t.getDate())} ${pad(t.getHours())}:${pad(t.getMinutes())}`
}

export type Tone = 'ok' | 'warn' | 'danger' | 'info' | 'neutral'

// ---------------------------------------------------------------------------
// 优惠券
// ---------------------------------------------------------------------------

/** percent：discount_value/100 %；fixed：立减金额（币种空串按 CNY） */
export function discountLabel(c: Pick<Coupon, 'discount_type' | 'discount_value' | 'currency'>): string {
  return c.discount_type === 'percent' ? `${basisPointsToPercent(c.discount_value)}% 折扣` : `立减 ${formatMoney(c.discount_value, c.currency || 'CNY')}`
}

/** 用量：「412 / 1000」与进度比例；上限 null 为「不限」、比例 0 */
export function usage(c: Pick<Coupon, 'redeemed_count' | 'max_redemptions'>): { label: string; ratio: number } {
  if (c.max_redemptions === null) return { label: `${c.redeemed_count} / 不限`, ratio: 0 }
  const ratio = c.max_redemptions > 0 ? Math.min(1, c.redeemed_count / c.max_redemptions) : 0
  return { label: `${c.redeemed_count} / ${c.max_redemptions}`, ratio }
}

export const validUntilLabel = (iso: string | null) => (iso ? formatDate(iso) : '长期')

/** 适用套餐：空 →「全部套餐」；套餐目录不可读（无 catalog.read 或加载失败）时退回「N 个套餐」 */
export function planScopeLabel(ids: readonly string[], plans: readonly Plan[] | undefined): string {
  if (ids.length === 0) return '全部套餐'
  if (!plans) return `${ids.length} 个套餐`
  return ids.map((id) => plans.find((p) => p.id === id)?.name ?? '已删除的套餐').join('、')
}

/** 只有 active / paused 能切换；expired、exhausted 在「全部」里显示成标签（契约：设计缺，待补·前端） */
export function couponState(status: string): { switchable: boolean; on: boolean; tag: { label: string; tone: Tone } | null } {
  if (status === 'active') return { switchable: true, on: true, tag: null }
  if (status === 'paused') return { switchable: true, on: false, tag: null }
  if (status === 'expired') return { switchable: false, on: false, tag: { label: '已过期', tone: 'warn' } }
  if (status === 'exhausted') return { switchable: false, on: false, tag: { label: '已用完', tone: 'neutral' } }
  return { switchable: false, on: false, tag: { label: status, tone: 'neutral' } }
}

/** 批次标签：name 与 code 不同才显示（后端没有 batch 概念，靠同名归批） */
export const couponBatchLabel = (c: Pick<Coupon, 'code' | 'name'>) => (c.name && c.name !== c.code ? c.name : '')

export interface CouponForm {
  code: string
  prefix: string
  count: string
  name: string
  kind: 'percent' | 'fixed'
  value: string
  currency: 'CNY' | 'USD'
  maxRedemptions: string
  perUser: string
  minOrder: string
  maxDiscount: string
  validUntil: string
  planIds: string[]
}

export const emptyCouponForm = (batch: boolean): CouponForm => ({
  code: '',
  prefix: '',
  count: '50',
  name: '',
  kind: 'percent',
  value: batch ? '15' : '10',
  currency: 'CNY',
  maxRedemptions: batch ? '1' : '',
  perUser: '1',
  minOrder: '',
  maxDiscount: '',
  validUntil: '',
  planIds: [],
})

export type FieldErrors = Record<string, string>
export type Built<T> = { ok: true; body: T } | { ok: false; errors: FieldErrors }

/** 批量生成前端上限 500（设计），后端 1000 */
export const COUPON_BATCH_MAX = 500

/**
 * 表单 → POST v1/coupons（单张）或 POST v1/coupons/batch 的请求体。错误键与后端 fields 同名
 * （code / prefix / count / name / discount_value …），接口 422 回来时同一处标红。
 */
export function buildCouponRequest(f: CouponForm, batch: boolean): Built<Record<string, unknown>> {
  const errors: FieldErrors = {}
  const body: Record<string, unknown> = {}
  if (batch) {
    const prefix = f.prefix.trim().toUpperCase()
    if (!/^[A-Z0-9]{0,8}$/.test(prefix)) errors.prefix = '只能用大写字母和数字，最多 8 位'
    const count = parseCount(f.count)
    if (count === null || Number.isNaN(count) || count < 1 || count > COUPON_BATCH_MAX) errors.count = `一次生成 1 到 ${COUPON_BATCH_MAX} 张`
    if (!f.name.trim()) errors.name = '必填，之后靠它找回这一批'
    Object.assign(body, { prefix, count, name: f.name.trim() })
  } else {
    const code = f.code.trim().toUpperCase()
    if (!code) errors.code = '请填写优惠码'
    body.code = code
    if (f.name.trim()) body.name = f.name.trim()
  }
  const value = f.kind === 'percent' ? percentToBasisPoints(f.value) : yuanToMinor(f.value)
  if (value === null || value < 1 || (f.kind === 'percent' && value > 10000)) {
    errors.discount_value = f.kind === 'percent' ? '折扣填 0.01 到 100 之间的数' : '立减金额需大于 0，最多两位小数'
  }
  Object.assign(body, { discount_type: f.kind, discount_value: value ?? 0 })
  if (f.kind === 'fixed') body.currency = f.currency

  const max = parseCount(f.maxRedemptions)
  if (Number.isNaN(max) || max === 0) errors.max_redemptions = '填正整数，留空为不限'
  body.max_redemptions = max
  const perUser = parseCount(f.perUser)
  if (perUser === null || Number.isNaN(perUser) || perUser < 1) errors.max_redemptions_per_user = '每人至少可用 1 次'
  else body.max_redemptions_per_user = perUser
  if (f.minOrder.trim()) {
    const min = yuanToMinor(f.minOrder)
    if (min === null) errors.min_order_amount = '填金额，最多两位小数'
    else body.min_order_amount = min
  }
  if (f.kind === 'percent' && f.maxDiscount.trim()) {
    const cap = yuanToMinor(f.maxDiscount)
    if (cap === null || cap < 1) errors.max_discount = '填大于 0 的金额，留空为不封顶'
    else body.max_discount = cap
  }
  if (f.validUntil) {
    const iso = localInputToIso(f.validUntil)
    if (!iso) errors.valid_until = '时间格式不正确'
    else if (new Date(iso).getTime() <= Date.now()) errors.valid_until = '结束时间要晚于现在'
    else body.valid_until = iso
  }
  if (f.planIds.length) body.applicable_plan_ids = f.planIds
  return Object.keys(errors).length ? { ok: false, errors } : { ok: true, body }
}

// ---------------------------------------------------------------------------
// 礼品卡
// ---------------------------------------------------------------------------
export const TEMPLATE_TYPES = [
  ['general', '通用卡'],
  ['plan', '套餐卡'],
  ['mystery', '盲盒'],
] as const

/** 模板卡片：面额、类型标签、「兑换后」说明（契约 GET v1/gift-cards 的设计映射） */
export function templateFace(t: Pick<GiftTemplate, 'type' | 'rewards'>, plans: readonly Plan[] | undefined): { face: string; kind: string; after: string } {
  const r = t.rewards
  if (t.type === 'plan') return { face: plans?.find((p) => p.id === r.plan_id)?.name ?? '套餐', kind: '套餐', after: '立即开通' }
  if (t.type === 'mystery') return { face: '盲盒', kind: `${r.pool?.length ?? 0} 个奖品`, after: '随机抽取' }
  if (r.balance) return { face: formatMoney(r.balance, 'CNY'), kind: '余额', after: '直接入账' }
  if (r.traffic_bytes) return { face: formatGB(r.traffic_bytes), kind: '流量', after: '当期有效' }
  if (r.expire_days) return { face: `${r.expire_days} 天`, kind: '延期', after: '顺延到期' }
  if (r.reset_quota) return { face: '重置', kind: '流量', after: '清零本期用量' }
  return { face: '—', kind: '通用', after: '—' }
}

/** 兑换得到的内容：「余额 +¥100.00 · 流量 +100 GB」；套餐卡 granted 为空用模板名，盲盒用 prize_label */
export function grantedLabel(u: Pick<Usage, 'granted' | 'template_name' | 'prize_label'>): string {
  const g = u.granted
  const parts = [
    g.balance ? `余额 +${formatMoney(g.balance, 'CNY')}` : '',
    g.traffic_bytes ? `流量 +${formatGB(g.traffic_bytes)}` : '',
    g.expire_days ? `+${g.expire_days} 天` : '',
    g.reset_quota ? '重置流量' : '',
  ].filter(Boolean)
  if (u.prize_label) return parts.length ? `${u.prize_label}（${parts.join(' · ')}）` : u.prize_label
  return parts.length ? parts.join(' · ') : u.template_name
}

/** 批次显示名：GB-MMDD-前 4 位，同一天的批次也能分开；CSV 文件名另用 id 前 8 位 */
export function batchLabel(b: Pick<Batch, 'id' | 'created_at'>): string {
  const t = new Date(b.created_at)
  return `GB-${pad(t.getMonth() + 1)}${pad(t.getDate())}-${b.id.slice(0, 4).toUpperCase()}`
}
export const batchFileName = (id: string) => `gift-codes-${id.slice(0, 8)}.csv`

export const CODE_STATUS: Readonly<Record<string, { label: string; tone: Tone }>> = {
  unused: { label: '可用', tone: 'ok' },
  used: { label: '已兑换', tone: 'neutral' },
  disabled: { label: '已停用', tone: 'danger' },
  expired: { label: '已过期', tone: 'warn' },
}

/** 兑换率 = used ÷ total，保留一位；total 为 0 时「—」 */
export function redeemRate(used: number, total: number): string {
  return total > 0 ? `${((used / total) * 100).toFixed(1)}%` : '—'
}

export interface PrizeForm {
  label: string
  weight: string
  balance: string
  trafficGb: string
  days: string
}

export interface TemplateForm {
  id: string | null
  type: GiftTemplate['type']
  name: string
  description: string
  status: GiftTemplate['status']
  balance: string
  trafficGb: string
  days: string
  resetQuota: boolean
  planId: string
  priceId: string
  pool: PrizeForm[]
  newUserOnly: boolean
  paidUserOnly: boolean
  requireInvite: boolean
  allowedPlanIds: string[]
  maxUsePerUser: string
  cooldownHours: string
  themeColor: string
}

const emptyPrize = (): PrizeForm => ({ label: '', weight: '1', balance: '', trafficGb: '', days: '' })

export function templateToForm(t: GiftTemplate | null): TemplateForm {
  const r: Rewards = t?.rewards ?? {}
  const yuan = (v?: number) => (v ? minorToYuan(v) : '')
  const gb = (v?: number) => (v ? String(Number((v / GB).toFixed(3))) : '')
  return {
    id: t?.id ?? null,
    type: t?.type ?? 'general',
    name: t?.name ?? '',
    description: t?.description ?? '',
    status: t?.status ?? 'active',
    balance: yuan(r.balance),
    trafficGb: gb(r.traffic_bytes),
    days: r.expire_days ? String(r.expire_days) : '',
    resetQuota: r.reset_quota ?? false,
    planId: r.plan_id ?? '',
    priceId: r.price_id ?? '',
    pool: r.pool?.length
      ? r.pool.map((p) => ({ label: p.label, weight: String(p.weight), balance: yuan(p.balance), trafficGb: gb(p.traffic_bytes), days: p.expire_days ? String(p.expire_days) : '' }))
      : [emptyPrize(), emptyPrize()],
    newUserOnly: t?.conditions.new_user_only ?? false,
    paidUserOnly: t?.conditions.paid_user_only ?? false,
    requireInvite: t?.conditions.require_invite ?? false,
    allowedPlanIds: t?.conditions.allowed_plan_ids ?? [],
    maxUsePerUser: t?.limits.max_use_per_user ? String(t.limits.max_use_per_user) : '',
    cooldownHours: t?.limits.cooldown_hours ? String(t.limits.cooldown_hours) : '',
    themeColor: t?.theme_color || '#b9442b',
  }
}
export { emptyPrize }

/** GB 输入（可带一位以上小数）→ 字节；空串 → 0，非法 → null */
function gbToBytes(input: string): number | null {
  const s = input.trim()
  if (s === '') return 0
  if (!/^\d+(\.\d+)?$/.test(s)) return null
  return Math.round(Number(s) * GB)
}

/** 表单 → POST v1/gift-cards 的请求体；错误键 name / rewards / conditions / limits 与后端 fields 同名 */
export function buildTemplateRequest(f: TemplateForm): Built<Record<string, unknown>> {
  const errors: FieldErrors = {}
  const name = f.name.trim()
  if (!name || [...name].length > 120) errors.name = '名称必填，不超过 120 字'

  const rewards: Record<string, unknown> = {}
  const money = (s: string) => (s.trim() ? yuanToMinor(s) : 0)
  const dayCount = (s: string) => {
    const n = parseCount(s)
    return n === null ? 0 : n
  }
  if (f.type === 'general') {
    const balance = money(f.balance)
    const traffic = gbToBytes(f.trafficGb)
    const expire = dayCount(f.days)
    if (balance === null || traffic === null || Number.isNaN(expire)) errors.rewards = '余额填金额、流量填 GB、天数填整数'
    else if (!balance && !traffic && !expire && !f.resetQuota) errors.rewards = '通用卡至少要送一样东西：余额、流量、延长到期或重置流量'
    else if (expire > 3650) errors.rewards = '延长天数不能超过 3650 天'
    else Object.assign(rewards, balance ? { balance } : {}, traffic ? { traffic_bytes: traffic } : {}, expire ? { expire_days: expire } : {}, f.resetQuota ? { reset_quota: true } : {})
  } else if (f.type === 'plan') {
    if (!f.planId || !f.priceId) errors.rewards = '套餐卡要选定套餐和价格'
    else Object.assign(rewards, { plan_id: f.planId, price_id: f.priceId })
  } else {
    if (f.pool.length < 2) errors.rewards = '盲盒至少要有 2 个奖品'
    else if (f.pool.length > 50) errors.rewards = '盲盒奖品不能超过 50 个'
    const pool = f.pool.map((p, i) => {
      const weight = parseCount(p.weight)
      const balance = money(p.balance)
      const traffic = gbToBytes(p.trafficGb)
      const expire = dayCount(p.days)
      if (!errors.rewards) {
        if (!p.label.trim()) errors.rewards = `第 ${i + 1} 个奖品缺少名称`
        else if (weight === null || Number.isNaN(weight) || weight < 1) errors.rewards = `第 ${i + 1} 个奖品的权重要填正整数`
        else if (balance === null || traffic === null || Number.isNaN(expire)) errors.rewards = `第 ${i + 1} 个奖品的数值格式不对`
        else if (!balance && !traffic && !expire) errors.rewards = `第 ${i + 1} 个奖品什么都不送`
      }
      return { label: p.label.trim(), weight: weight ?? 0, ...(balance ? { balance } : {}), ...(traffic ? { traffic_bytes: traffic } : {}), ...(expire ? { expire_days: expire } : {}) }
    })
    rewards.pool = pool
  }

  if (f.newUserOnly && f.paidUserOnly) errors.conditions = '「仅新用户」和「仅付费用户」不能同时勾选'
  const conditions: Record<string, unknown> = {
    ...(f.newUserOnly ? { new_user_only: true } : {}),
    ...(f.paidUserOnly ? { paid_user_only: true } : {}),
    ...(f.requireInvite ? { require_invite: true } : {}),
    ...(f.allowedPlanIds.length ? { allowed_plan_ids: f.allowedPlanIds } : {}),
  }
  const maxUse = parseCount(f.maxUsePerUser)
  const cooldown = parseCount(f.cooldownHours)
  if (Number.isNaN(maxUse) || Number.isNaN(cooldown)) errors.limits = '次数与冷却小时填非负整数，留空为不限'
  const limits: Record<string, unknown> = { ...(maxUse ? { max_use_per_user: maxUse } : {}), ...(cooldown ? { cooldown_hours: cooldown } : {}) }

  if (Object.keys(errors).length) return { ok: false, errors }
  return {
    ok: true,
    body: {
      ...(f.id ? { id: f.id } : {}),
      name,
      description: f.description.trim(),
      type: f.type,
      status: f.status,
      rewards,
      conditions,
      limits,
      theme_color: f.themeColor,
    },
  }
}

export interface GenerateForm {
  count: string
  prefix: string
  expiresAt: string
}

/** 表单 → POST v1/gift-cards/{id}/codes：数量 1..5000，前缀大写字母数字 ≤ 8，有效期不能在过去 */
export function buildGenerateRequest(f: GenerateForm): Built<{ count: number; prefix?: string; expires_at?: string }> {
  const errors: FieldErrors = {}
  const count = parseCount(f.count)
  if (count === null || Number.isNaN(count) || count < 1 || count > 5000) errors.count = '一次生成 1 到 5000 个'
  const prefix = f.prefix.trim().toUpperCase()
  if (!/^[A-Z0-9]{0,8}$/.test(prefix)) errors.prefix = '只能用大写字母和数字，最多 8 位'
  const iso = localInputToIso(f.expiresAt)
  if (f.expiresAt && !iso) errors.expires_at = '时间格式不正确'
  else if (iso && new Date(iso).getTime() <= Date.now()) errors.expires_at = '有效期不能设在过去'
  if (Object.keys(errors).length) return { ok: false, errors }
  return { ok: true, body: { count: count!, ...(prefix ? { prefix } : {}), ...(iso ? { expires_at: iso } : {}) } }
}

// ---------------------------------------------------------------------------
// 佣金与提现
// ---------------------------------------------------------------------------

/** 状态映射（契约 GET v1/withdrawals）；打款只对 approved 开放（processing 由后端拒绝标记） */
export function withdrawalState(status: string): { label: string; tone: Tone; canReview: boolean; canPay: boolean } {
  switch (status) {
    case 'requested':
    case 'reviewing':
      return { label: '待审核', tone: 'warn', canReview: true, canPay: false }
    case 'approved':
      return { label: '待打款', tone: 'info', canReview: false, canPay: true }
    case 'processing':
      return { label: '打款中', tone: 'info', canReview: false, canPay: false }
    case 'paid':
      return { label: '已打款', tone: 'ok', canReview: false, canPay: false }
    case 'rejected':
      return { label: '已拒绝', tone: 'neutral', canReview: false, canPay: false }
    case 'failed':
      return { label: '打款失败', tone: 'danger', canReview: false, canPay: false }
    case 'returned':
      return { label: '已退回', tone: 'danger', canReview: false, canPay: false }
    default:
      return { label: status, tone: 'neutral', canReview: false, canPay: false }
  }
}

/** 四个统计（契约设计映射）；total_earned / invited_users 是待补·后端，缺时显示「—」 */
export function commissionStats(o: CommissionOverview): Array<{ label: string; value: string }> {
  const money = (v: number | undefined) => (v === undefined ? '—' : formatMoney(v, 'CNY'))
  return [
    { label: '累计佣金', value: money(o.total_earned) },
    { label: '冻结中', value: money(o.pending) },
    { label: '已提现', value: money(o.paid_out) },
    { label: '邀请注册', value: o.invited_users === undefined ? '—' : o.invited_users.toLocaleString('zh-CN') },
  ]
}

export interface CommissionForm {
  rate: number
  scope: 'first_order' | 'every_order'
  freezeDays: string
  minWithdraw: string
}

export const commissionToForm = (o: CommissionOverview): CommissionForm => ({
  rate: o.rate_percent,
  scope: o.scope ?? 'every_order',
  freezeDays: String(o.freeze_days),
  minWithdraw: minorToYuan(o.min_withdraw),
})

/**
 * 表单 → POST v1/commission/config。scope 只在后端 overview 回了 scope 时才发：
 * 计佣范围是待补·后端，后端解码 DisallowUnknownFields，没实现前多发这个字段会整单 400。
 */
export function buildCommissionConfig(f: CommissionForm, supportsScope: boolean): Built<Record<string, unknown>> {
  const errors: FieldErrors = {}
  if (!Number.isInteger(f.rate) || f.rate < 0 || f.rate > 50) errors.rate_percent = '佣金比例需在 0 到 50 之间'
  const freeze = parseCount(f.freezeDays)
  if (freeze === null || Number.isNaN(freeze) || freeze > 90) errors.freeze_days = '冻结天数需在 0 到 90 之间'
  const min = yuanToMinor(f.minWithdraw)
  if (min === null) errors.min_withdraw = '填金额，最多两位小数'
  if (Object.keys(errors).length) return { ok: false, errors }
  return { ok: true, body: { rate_percent: f.rate, freeze_days: freeze, min_withdraw: min, ...(supportsScope ? { scope: f.scope } : {}) } }
}
