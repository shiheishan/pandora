import { formatMoney } from '../../../core/format'
import type { PaymentMethod } from './catalog'

/** 去掉百分比末尾多余的 0：例如 "2.50" -> "2.5", "0.00" -> "0", "10.00" -> "10" */
export function trimPercent(s: string): string {
  const n = Number(s)
  if (Number.isNaN(n) || n === 0) return '0'
  return String(parseFloat(s))
}

/** 是否有手续费：固定 > 0 或比例不为 0 */
export function hasFee(m: Pick<PaymentMethod, 'fee_fixed' | 'fee_percent'>): boolean {
  if (m.fee_fixed > 0) return true
  const pct = Number(m.fee_percent)
  return !Number.isNaN(pct) && pct !== 0
}

/** 规则说明：没有手续费回 ''；否则如 '手续费 2.5% + ¥0.30'（只有一项时只写那一项） */
export function feeRule(m: Pick<PaymentMethod, 'fee_fixed' | 'fee_percent'>): string {
  if (!hasFee(m)) return ''
  const pct = Number(m.fee_percent)
  const hasPct = !Number.isNaN(pct) && pct !== 0
  const hasFixed = m.fee_fixed > 0
  const pctStr = hasPct ? `${trimPercent(m.fee_percent)}%` : ''
  const fixedStr = hasFixed ? formatMoney(m.fee_fixed, 'CNY') : ''

  if (hasPct && hasFixed) {
    return `手续费 ${pctStr} + ${fixedStr}`
  }
  if (hasPct) {
    return `手续费 ${pctStr}`
  }
  return `手续费 ${fixedStr}`
}

/**
 * 渠道卡片/按钮上的手续费单行提示：
 * m.fee 与 m.total 都有且 m.fee > 0 时回 '手续费 ¥0.55，合计 ¥10.55'；
 * m.fee === 0 回 ''；
 * 没带 fee（没传金额）时回 feeRule(m)。
 */
export function feeLine(m: Pick<PaymentMethod, 'fee_fixed' | 'fee_percent' | 'fee' | 'total'>): string {
  if (m.fee !== undefined && m.total !== undefined) {
    if (m.fee > 0) {
      return `手续费 ${formatMoney(m.fee, 'CNY')}，合计 ${formatMoney(m.total, 'CNY')}`
    }
    return ''
  }
  return feeRule(m)
}

/**
 * 金额下面的小字提示：
 * surcharge > 0 时回 '含手续费 ¥x，订单 ¥(total − surcharge)'，否则 ''
 */
export function surchargeNote(surcharge: number, total: number): string {
  if (surcharge <= 0) return ''
  const orderAmount = total - surcharge
  return `含手续费 ${formatMoney(surcharge, 'CNY')}，订单 ${formatMoney(orderAmount, 'CNY')}`
}
