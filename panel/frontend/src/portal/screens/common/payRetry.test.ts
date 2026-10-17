import { describe, expect, it } from 'vitest'
import { ApiError } from '../../../core/api'
import { PAY_IN_PROGRESS_BACKOFF_MS, PAY_IN_PROGRESS_MAX_RETRIES, afterOrderCheck, payFailureAction } from './payRetry'

function apiError(code: string, status = 409): ApiError {
  return new ApiError({ status, code: code as ApiError['code'], message: code })
}

describe('payFailureAction', () => {
  it('payment_in_progress：attempts 0/1/2 自动重试且间隔递增', () => {
    const err = apiError('payment_in_progress')
    expect(payFailureAction(err, 0)).toEqual({ kind: 'retry_later', delayMs: 2000 })
    expect(payFailureAction(err, 1)).toEqual({ kind: 'retry_later', delayMs: 5000 })
    expect(payFailureAction(err, 2)).toEqual({ kind: 'retry_later', delayMs: 10000 })
    expect(PAY_IN_PROGRESS_BACKOFF_MS).toEqual([2000, 5000, 10000])
    // 合计要超过后端建单最长 15 秒
    expect(PAY_IN_PROGRESS_BACKOFF_MS.reduce((a, b) => a + b, 0)).toBeGreaterThan(15000)
    expect(PAY_IN_PROGRESS_MAX_RETRIES).toBe(3)
  })

  it('payment_in_progress：attempts 3 只展示错误', () => {
    expect(payFailureAction(apiError('payment_in_progress'), 3)).toEqual({ kind: 'show' })
  })

  it('conflict → check_order；order_lapsed → unpayable', () => {
    expect(payFailureAction(apiError('conflict'), 0)).toEqual({ kind: 'check_order' })
    expect(payFailureAction(apiError('order_lapsed'), 0)).toEqual({ kind: 'unpayable' })
  })

  it('network_error 或普通 Error → show', () => {
    expect(payFailureAction(apiError('network_error', 0), 0)).toEqual({ kind: 'show' })
    expect(payFailureAction(new Error('x'), 0)).toEqual({ kind: 'show' })
  })
})

describe('afterOrderCheck', () => {
  it('待付 → still_payable；已取消 / 已付 / undefined → unpayable；刷新失败 → show', () => {
    expect(afterOrderCheck('pending_payment', false)).toBe('still_payable')
    expect(afterOrderCheck('cancelled', false)).toBe('unpayable')
    expect(afterOrderCheck('paid', false)).toBe('unpayable')
    expect(afterOrderCheck(undefined, false)).toBe('unpayable')
    expect(afterOrderCheck(undefined, true)).toBe('show')
  })
})
