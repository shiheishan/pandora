import { cancelReasonLabel } from '../billing/model'

/**
 * 余额流水「原因」那一列的文字：后端 memo 原样给。订单取消退回（balance_release）的 memo 是取消原因码
 * （order 的 user_cancelled / expired / account_deleted / plan_archived），经订单页同一张表翻成中文；
 * 认不出的英文码（形如 user_cancelled）不显示，与用户端 cancelText 同口径；管理员写的中文原话照写。
 * 其余类型的 memo 是写死的中文或管理员写的原因（可能就是 refund1 这种英文），原样显示，与用户端钱包一致。
 * 单独成文件：billing/model 依赖 users/model，放进 users/model 会成环。
 */
export function balanceEntryNote(kind: string, memo: string): string {
  const text = memo.trim()
  if (!text) return ''
  if (kind !== 'balance_release') return text
  const label = cancelReasonLabel(text)
  return /^[a-z0-9_]+$/.test(label) ? '' : label
}
