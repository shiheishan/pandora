/**
 * [INPUT]: 依赖 react 的 useCallback，依赖 ../../../core/api 的 isApiError，依赖 ../../../ui 的 useToast，依赖 ../../actions 的 useFailure / Fail / FailureOptions
 * [OUTPUT]: 对外提供 useCatalogFailure、SALES_OFF
 * [POS]: admin/screens/plans 的写失败处理：在 actions.ts 的 useFailure 外面加一层，把目录写接口的 503（销售开关 AEGIS_SALES_ENABLED 未开，契约后台-04 公共映射）换成说清原因、不劝重试的提示；只有 422 的 fields 交给表单，调用方没有表单可标时（如发布前置条件）直接 Toast 出来而不是信封里笼统的「请求参数校验未通过」；409 的 fields 是乐观锁现值，一律 Toast 信封原文；reauth 取消与幂等键去留仍由 useFailure 定
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useCallback } from 'react'
import { isApiError } from '../../../core/api'
import { useToast } from '../../../ui'
import { useFailure, type Fail, type FailureOptions } from '../../actions'

export const SALES_OFF = '销售开关未开启：新建、改价、发布与上架暂时做不了，重试也不会成功，请联系运维开启 AEGIS_SALES_ENABLED'

export function useCatalogFailure(): Fail {
  const fail = useFailure()
  const toast = useToast()
  return useCallback<Fail>(
    (error, handle) => {
      if (isApiError(error, 'service_unavailable')) {
        // 5xx 按 endsIntent 保留幂等键：开关打开后同一意图再点，后端会重新执行
        toast(SALES_OFF, 'danger')
        return false
      }
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
