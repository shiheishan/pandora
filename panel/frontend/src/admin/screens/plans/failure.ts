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
