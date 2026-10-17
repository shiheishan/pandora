import { formatMoney } from '../../../core/format'
import type { Coupon, Plan } from './schemas'

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

export const pad = (n: number) => String(n).padStart(2, '0')
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

/** percent：discount_value/100 %；fixed：立减金额 */
export function discountLabel(c: Pick<Coupon, 'discount_type' | 'discount_value'>): string {
  return c.discount_type === 'percent' ? `${basisPointsToPercent(c.discount_value)}% 折扣` : `立减 ${formatMoney(c.discount_value)}`
}

/** 用量：「412 / 1000」与进度比例；上限 null 为「不限」、比例 0 */
export function usage(c: Pick<Coupon, 'redeemed_count' | 'max_redemptions'>): { label: string; ratio: number } {
  if (c.max_redemptions === null) return { label: `${c.redeemed_count} / 不限`, ratio: 0 }
  const ratio = c.max_redemptions > 0 ? Math.min(1, c.redeemed_count / c.max_redemptions) : 0
  return { label: `${c.redeemed_count} / ${c.max_redemptions}`, ratio }
}

/** 有效期标签：short 用于列表列，full 用于 title 与详情 */
export function couponPeriodLabel(
  c: Pick<Coupon, 'valid_from' | 'valid_until'>,
  now: Date,
): { short: string; full: string } {
  const fromFuture = c.valid_from ? new Date(c.valid_from).getTime() > now.getTime() : false
  const prefix = fromFuture && c.valid_from ? `${formatDate(c.valid_from).slice(5)} 起 · ` : ''
  const suffix = c.valid_until ? `至 ${formatDate(c.valid_until).slice(5)}` : '长期'
  const short = `${prefix}${suffix}`
  const fromFull = c.valid_from ? formatDate(c.valid_from) : '创建即生效'
  const untilFull = c.valid_until ? formatDate(c.valid_until) : '长期'
  return { short, full: `${fromFull} → ${untilFull}` }
}

/** 适用套餐：空 →「全部套餐」；套餐目录没取到（加载中或失败）时退回「N 个套餐」 */
export function planScopeLabel(ids: readonly string[], plans: readonly Plan[] | undefined): string {
  if (ids.length === 0) return '全部套餐'
  if (!plans) return `${ids.length} 个套餐`
  return ids.map((id) => plans.find((p) => p.id === id)?.name ?? '已删除的套餐').join('、')
}

/** 只有 active / paused 能切换；expired、exhausted 在「全部」里显示成标签（契约：设计缺，待补·前端） */
export function couponState(status: string): { switchable: boolean; on: boolean; tag: { label: string; tone: Tone } | null } {
  if (status === 'active') return { switchable: true, on: true, tag: null }
  if (status === 'paused') return { switchable: true, on: false, tag: null }
  if (status === 'expired') return { switchable: false, on: false, tag: { label: '已过期', tone: 'neutral' } }
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
  maxRedemptions: string
  perUser: string
  minOrder: string
  maxDiscount: string
  validFrom: string
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
  maxRedemptions: batch ? '1' : '',
  perUser: '1',
  minOrder: '',
  maxDiscount: '',
  validFrom: '',
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
  if (f.validFrom) {
    const iso = localInputToIso(f.validFrom)
    if (!iso) errors.valid_from = '时间格式不正确'
    else body.valid_from = iso
  }
  if (f.validUntil) {
    const iso = localInputToIso(f.validUntil)
    if (!iso) errors.valid_until = '时间格式不正确'
    else if (new Date(iso).getTime() <= Date.now()) errors.valid_until = '结束时间要晚于现在'
    else body.valid_until = iso
  }
  if (f.validFrom && f.validUntil && !errors.valid_from && !errors.valid_until) {
    if (new Date(body.valid_until as string).getTime() <= new Date(body.valid_from as string).getTime()) {
      errors.valid_until = '结束时间要晚于开始时间'
      delete body.valid_until
    }
  }
  if (f.planIds.length) body.applicable_plan_ids = f.planIds
  return Object.keys(errors).length ? { ok: false, errors } : { ok: true, body }
}
