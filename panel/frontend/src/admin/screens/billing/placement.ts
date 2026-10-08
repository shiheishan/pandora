import { formatDateTime, formatMoney } from '../../../core/format'
import type { Placement, ManualPreview } from './schemas'

// ===========================================================================
// 后台开单的「这单落到哪一份」：选项、默认值、徽标全由服务端（purchase.Options）给，
// 这里只负责把每一项的结果写成人话，并决定提交按钮的状态。前端不重算规则，也不算钱。
// ===========================================================================

/** 提交给 POST v1/orders/manual 的 target（purchase.Choice） */
export interface PlacementChoice {
  kind: Placement['kind']
  subscription_id?: string
}

export function choiceOf(p: Pick<Placement, 'kind' | 'subscription_id'>): PlacementChoice {
  return p.subscription_id ? { kind: p.kind, subscription_id: p.subscription_id } : { kind: p.kind }
}

const BADGE_LABELS = {
  same_plan: '同款',
  soonest_expiry: '最快到期',
  most_used: '用得最多',
  least_remaining: '剩得最少',
} as const

export function badgeLabel(badge: Placement['badge']): string | null {
  return badge ? BADGE_LABELS[badge] : null
}

const day = (at: string) => formatDateTime(at).slice(0, 10)

/** 这一份怎么称呼：有备注名写「备注名」套餐名，没有就只写套餐名 */
export function subjectOf(p: Pick<Placement, 'label' | 'plan_name'>): string {
  const plan = p.plan_name || '订阅'
  return p.label ? `「${p.label}」${plan}` : plan
}

/** 这一份现在的样子：状态与到期日，用来在同款多份时分得清谁是谁 */
export function stateLine(p: Pick<Placement, 'state' | 'period_end'>): string {
  const end = p.period_end ? day(p.period_end) : null
  if (p.state === 'revivable') return end ? `已过期（${end} 到期），还在 30 天续费窗口内` : '已过期，还在 30 天续费窗口内'
  return end ? `生效中，${end} 到期` : '生效中，长期有效'
}

/** 选项标题：一句话讲清「对哪一份做什么」 */
export function placementTitle(p: Placement, targetPlan: string): string {
  switch (p.kind) {
    case 'renew':
      return `${p.state === 'revivable' ? '恢复并续一期' : '续一期'}：${subjectOf(p)}`
    case 'change':
      return p.expired ? `恢复${subjectOf(p)}并改成${targetPlan}` : `把${subjectOf(p)}换成${targetPlan}`
    case 'new':
      return `另开一份${targetPlan}`
  }
}

/** 选项的结果：选之前就能看到会发生什么（到期日变化、剩余价值去向、链接是否变化） */
export function placementResult(p: Placement, settlement: 'grant' | 'pending' | 'offline'): string {
  switch (p.kind) {
    case 'renew': {
      // 生效中的接在原到期日后；过期 30 天内的从付款起算（恢复使用）
      const range = !p.new_period_end ? '' : p.period_end && p.state !== 'revivable' ? `到期 ${day(p.period_end)} → ${day(p.new_period_end)}` : `从现在起算，到期 ${day(p.new_period_end)}`
      return [stateLine(p), range, '订阅链接不变'].filter(Boolean).join('；')
    }
    case 'change': {
      const credit = p.credit ?? 0
      const money = formatMoney(credit, p.currency ?? 'CNY')
      const creditText =
        credit <= 0 ? '原套餐没有可抵的剩余价值' : settlement === 'grant' ? `没用完的 ${money} 全额退到用户余额` : `没用完的 ${money} 先抵新价，抵不完的退到用户余额`
      // 只退已付价值：赠送的时长（加时长卡、套餐卡续的期）不保留，有就写出来（与门户兑换卡同一句）
      const gift = (p.gift_days_lost ?? 0) > 0 ? `赠送的 ${p.gift_days_lost} 天不保留` : ''
      return [stateLine(p), creditText, gift, p.new_period_end ? `换后从现在起算，到期 ${day(p.new_period_end)}` : '', '订阅链接不变'].filter(Boolean).join('；')
    }
    case 'new':
      return ['会生成新的订阅链接，用户现有的订阅都不动', p.new_period_end ? `到期 ${day(p.new_period_end)}` : ''].filter(Boolean).join('；')
  }
}

/**
 * 价目旁的说明。换套餐且原套餐有剩余价值时，实际应付 = 新价 − 剩余价值（抵不完退余额），
 * 金额以订单为准，这里不替服务端算
 */
export function priceNote(amount: number, currency: string, option: Pick<Placement, 'kind' | 'credit'> | undefined): string {
  const price = formatMoney(amount, currency)
  if (option?.kind === 'change' && (option.credit ?? 0) > 0) return `新套餐 ${price}，先抵扣原套餐的剩余价值，实际应付以订单为准`
  return `应付 ${price}`
}

/** 当前选中的键：用户点过且仍在列表里用用户的，否则用服务端默认；没有默认就是空（不预选） */
export function selectedKey(preview: Pick<ManualPreview, 'options' | 'default_key'> | undefined, picked: string): string {
  if (!preview) return ''
  if (picked && preview.options.some((o) => o.key === picked)) return picked
  // 只有一个选项时不让选，直接用它
  if (preview.options.length === 1) return preview.options[0]!.key
  return preview.options.some((o) => o.key === preview.default_key) ? preview.default_key : ''
}

/**
 * 提交按钮的状态：能点，或灰着并写明原因（没有默认值时是「先选落点」）。
 * 用户、套餐、价格还没选全时落点无从谈起，按钮照常可点，点了由表单预检指出缺哪一项
 */
export type SubmitGate = { ok: true } | { ok: false; label: string }

export function submitGate(opts: {
  hasInputs: boolean
  loading: boolean
  failed: boolean
  selected: string
  /** 读取失败时按钮上的字：价格档不对时是「先换一个价格」，其余是「落点读取失败」 */
  failedLabel?: string
  /** 选好了也提交不了的原因（如待支付单低于最低付款额），原因与下一步写在表单里，按钮上只写短句 */
  blocked?: string | null
}): SubmitGate {
  if (!opts.hasInputs) return { ok: true }
  if (opts.loading) return { ok: false, label: '读取落点中…' }
  if (opts.failed) return { ok: false, label: opts.failedLabel ?? '落点读取失败' }
  if (!opts.selected) return { ok: false, label: '先选落点' }
  if (opts.blocked) return { ok: false, label: opts.blocked }
  return { ok: true }
}

/**
 * preview 的 422 落到哪：price_id 落到「套餐与周期」；entry_subscription_id（「给这份开单」带进来的那份）
 * 不对时不按入口预选，改由管理员自己选，原因写在落点区。其余失败（含网络）整块提示并给重试
 */
export interface PreviewFailure {
  /** 落到表单项上的错误，键与开单表单一致 */
  fields: Record<string, string>
  /** 入口订阅被拒：去掉入口重取一次 */
  entryRejected: string | null
  /** 没有落到任何表单项上，落点区整块提示 */
  general: string | null
}

export function previewFailure(e: { status: number; fields: Readonly<Record<string, string>>; message: string } | null): PreviewFailure {
  if (!e) return { fields: {}, entryRejected: null, general: null }
  const { price_id, entry_subscription_id } = e.fields
  if (e.status === 422 && (price_id || entry_subscription_id)) {
    return { fields: price_id ? { price_id } : {}, entryRejected: entry_subscription_id ?? null, general: null }
  }
  return { fields: {}, entryRejected: null, general: e.message }
}
