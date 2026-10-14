import { isApiError } from '../../../core/api'

/** 「正在发起」最多自动重试几次 */
export const PAY_IN_PROGRESS_MAX_RETRIES = 3
/** 第 n 次自动重试前等多久（毫秒） */
export const PAY_IN_PROGRESS_BACKOFF_MS: readonly number[] = [2000, 4000, 8000]

/** 发起支付失败后怎么办：retry_later = 过一会儿自动再发；check_order = 先刷新订单再定；unpayable = 订单不可付（调 onUnpayable）；show = 只显示错误 */
export type PayFailureAction = { kind: 'retry_later'; delayMs: number } | { kind: 'check_order' } | { kind: 'unpayable' } | { kind: 'show' }

const PENDING_PAYMENT = 'pending_payment'

/** attempts 是已经自动重试过的次数（第一次失败时为 0） */
export function payFailureAction(error: unknown, attempts: number): PayFailureAction {
  if (isApiError(error, 'payment_in_progress')) {
    if (attempts < PAY_IN_PROGRESS_MAX_RETRIES) {
      const delayMs = PAY_IN_PROGRESS_BACKOFF_MS[attempts] ?? PAY_IN_PROGRESS_BACKOFF_MS[PAY_IN_PROGRESS_BACKOFF_MS.length - 1]!
      return { kind: 'retry_later', delayMs }
    }
    return { kind: 'show' }
  }
  if (isApiError(error, 'conflict')) return { kind: 'check_order' }
  if (isApiError(error, 'order_lapsed')) return { kind: 'unpayable' }
  return { kind: 'show' }
}

/** 刷新订单之后：status 是订单状态（刷新失败传 undefined、refreshFailed 传 true） */
export function afterOrderCheck(status: string | undefined, refreshFailed: boolean): 'unpayable' | 'still_payable' | 'show' {
  if (refreshFailed) return 'show'
  if (status === PENDING_PAYMENT) return 'still_payable'
  return 'unpayable'
}
