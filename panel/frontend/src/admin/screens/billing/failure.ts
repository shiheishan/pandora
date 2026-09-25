/**
 * [INPUT]: 依赖 react 的 useCallback，依赖 ../../../core/api 的 isApiError，依赖 ../../../ui 的 useToast，依赖 ../../actions 的 useFailure / endsIntent / Fail / FailureOptions，依赖 ./model 的 knownMessage
 * [OUTPUT]: 对外提供 useBillingFailure
 * [POS]: admin/screens/billing 的写失败处理：在 actions.ts 的 useFailure 外面加一层，把 billing 域还是英文的 message（凭证号重复 R74、取消订单的 409 / 400）换成中文再 Toast；其余（reauth 取消静默、422 fields 标表单、幂等键按 endsIntent 去留）原样交给 useFailure
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useCallback } from 'react'
import { isApiError } from '../../../core/api'
import { useToast } from '../../../ui'
import { endsIntent, useFailure, type Fail, type FailureOptions } from '../../actions'
import { knownMessage } from './model'

export function useBillingFailure(): Fail {
  const fail = useFailure()
  const toast = useToast()
  return useCallback<Fail>(
    (error, handle) => {
      const known = isApiError(error) ? knownMessage(error.message) : null
      if (!known) return fail(error, handle)
      const opts: FailureOptions = typeof handle === 'function' ? { fields: handle } : (handle ?? {})
      if (opts.intent && endsIntent(error)) opts.intent.reset()
      toast(known, 'danger')
      return false
    },
    [fail, toast],
  )
}
