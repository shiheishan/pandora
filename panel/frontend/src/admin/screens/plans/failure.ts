/**
 * [INPUT]: 依赖 react 的 useCallback，依赖 ../../../core/api 的 isApiError，依赖 ../../../ui 的 useToast，依赖 ../../actions 的 useFailure / Fail / FailureOptions
 * [OUTPUT]: 对外提供 useCatalogFailure
 * [POS]: admin/screens/plans 的写失败处理：在 actions.ts 的 useFailure 外面加一层：只有 422 的 fields 交给表单，调用方没有表单可标时（如发布前置条件）直接 Toast 出来而不是信封里笼统的「请求参数校验未通过」；409 的 fields 是乐观锁现值，一律 Toast 信封原文；其余状态（含后台只读降级的 503）照 useFailure 的通用口径，reauth 取消与幂等键去留也由它定
 */
import { useCallback } from 'react'
import { isApiError } from '../../../core/api'
import { useToast } from '../../../ui'
import { useFailure, type Fail, type FailureOptions } from '../../actions'

export function useCatalogFailure(): Fail {
  const fail = useFailure()
  const toast = useToast()
  return useCallback<Fail>(
    (error, handle) => {
      const opts: FailureOptions = typeof handle === 'function' ? { fields: handle } : { ...handle }
      // 只有 422 的 fields 是给表单的；409 的 fields 是乐观锁现值（row_version / updated_at = "current=…"），
      // 标到表单上看不见，信封 message 本身就是可读的「…已被其他管理员修改，请刷新后重试」
      if (!isApiError(error, 'validation_failed')) return fail(error, { intent: opts.intent })
      // 没有表单可标的确认框（发布、保存绑定、归档）：发布前置条件等原因在 fields 里，
      // 信封 message 只是「请求参数校验未通过」，所以把 fields 的话说出来
      opts.fields ??= (fields) => toast(Object.values(fields).join('；'), 'danger')
      return fail(error, opts)
    },
    [fail, toast],
  )
}
