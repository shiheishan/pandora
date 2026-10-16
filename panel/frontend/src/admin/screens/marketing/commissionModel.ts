import { formatMoney } from '../../../core/format'
import { formatDateTime, minorToYuan, parseCount, yuanToMinor, type Built, type FieldErrors, type Tone } from './logic'
import type { CommissionOverview, ReviewEntry } from './schemas'

// ---------------------------------------------------------------------------
// 佣金与提现
// ---------------------------------------------------------------------------

/** 作废原因：去掉首尾空白后按字符数 5～500；合格返回 null，否则返回提示 */
export function voidReasonError(reason: string): string | null {
  const trimmed = reason.trim()
  const len = [...trimmed].length
  if (len < 5) return '作废必须填写原因，至少 5 个字'
  if (len > 500) return '原因最多 500 个字'
  return null
}

/** 通过原因可不填；填了最多 500 字。合格返回 null，否则返回提示 */
export function approveReasonError(reason: string): string | null {
  return [...reason.trim()].length > 500 ? '原因最多 500 个字' : null
}

/**
 * 冻结期说明：未到期「冻结至 MM-DD HH:mm」；已到期「冻结期已过，通过后下一轮自动解冻」。
 * 解冻时待结科目余额不足转来的条目（underfunded）通过后解冻仍会失败、作废也冲不回，只能先核对账本，不说「会自动解冻」。
 * 受益人已注销的条目（account_deleted）只能作废。
 */
export function freezeLabel(entry: Pick<ReviewEntry, 'frozen_until' | 'review_source'>, now: Date): string {
  if (entry.review_source === 'account_deleted') return '受益人账号已注销：只能作废，作废后冲回平台收入'
  if (entry.review_source === 'underfunded') return '待结科目余额不足：通过后解冻仍会失败，作废也冲不回，请先核对账本'
  if (new Date(entry.frozen_until).getTime() <= now.getTime()) return '冻结期已过，通过后下一轮自动解冻'
  return `冻结至 ${formatDateTime(entry.frozen_until)}`
}

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

/** 四个统计（契约设计映射） */
export function commissionStats(o: CommissionOverview): Array<{ label: string; value: string }> {
  return [
    { label: '累计佣金', value: formatMoney(o.total_earned, 'CNY') },
    { label: '冻结中', value: formatMoney(o.pending, 'CNY') },
    { label: '已提现', value: formatMoney(o.paid_out, 'CNY') },
    { label: '邀请注册', value: o.invited_users.toLocaleString('zh-CN') },
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
  scope: o.scope,
  freezeDays: String(o.freeze_days),
  minWithdraw: minorToYuan(o.min_withdraw),
})

/** 表单 → POST v1/commission/config（后端 DisallowUnknownFields，只发这四个字段） */
export function buildCommissionConfig(f: CommissionForm): Built<Record<string, unknown>> {
  const errors: FieldErrors = {}
  if (!Number.isInteger(f.rate) || f.rate < 0 || f.rate > 50) errors.rate_percent = '佣金比例需在 0 到 50 之间'
  const freeze = parseCount(f.freezeDays)
  if (freeze === null || Number.isNaN(freeze) || freeze > 90) errors.freeze_days = '冻结天数需在 0 到 90 之间'
  const min = yuanToMinor(f.minWithdraw)
  if (min === null) errors.min_withdraw = '填金额，最多两位小数'
  if (Object.keys(errors).length) return { ok: false, errors }
  return { ok: true, body: { rate_percent: f.rate, freeze_days: freeze, min_withdraw: min, scope: f.scope } }
}
