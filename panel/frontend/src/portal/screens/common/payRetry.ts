import { isApiError } from '../../../core/api'

/** 「正在发起」最多自动重试几次 */
export const PAY_IN_PROGRESS_MAX_RETRIES = 3
/**
 * 第 n 次自动重试前等多久（毫秒）。合计 17 秒，要盖过后端那次建单最长占着的时间：
 * 另一次发起的 CompleteIntent 最长 15 秒（completeIntentBudget）加 BeginIntent，
 * 合计短于它时最后一次重试可能仍落在建单窗口里，用户白等一轮又看到「请稍后点重试」。
 */
export const PAY_IN_PROGRESS_BACKOFF_MS: readonly number[] = [2000, 5000, 10000]

/** 发起支付失败后怎么办：retry_later = 过一会儿自动再发；check_order = 先刷新订单再定；unpayable = 订单不可付（调 onUnpayable）；show = 只显示错误（含 below_method_minimum：换方式付，不刷新订单） */
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
  // 低于所选方式的最低额：换一种付款方式才能付，不刷新订单、不当成被取代
  if (isApiError(error, 'below_method_minimum')) return { kind: 'show' }
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
