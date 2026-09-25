/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useMutation，依赖 ../../../shell/runtime 的 useApi，依赖 ./logic 的 HookBody，依赖 ./queries 的 useFailure / useIntentKey / useInvalidateSystem，依赖 ./schemas 的 hookSaved
 * [OUTPUT]: 对外提供 useSaveHook
 * [POS]: admin/screens/system 钩子的保存写操作，新建条、卡片启停与编辑弹窗共用：POST v1/plugin-hooks 按 code upsert（reauth + 幂等 plugin_hook_save），成功后重拉列表并把响应里一次性的签名密钥交给调用方；失败交给 fail(e, { fields, intent })
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation } from '@tanstack/react-query'
import { useApi } from '../../../shell/runtime'
import type { HookBody } from './logic'
import { useFailure, useIntentKey, useInvalidateSystem } from './queries'
import { hookSaved } from './schemas'

/** 保存钩子（新建 / 编辑 / 启停共用）：reauth + 幂等；成功后返回响应里一次性的签名密钥 */
export function useSaveHook(onSaved: (r: { secret?: string; secret_hint?: string }, body: HookBody) => void, setErrors?: (f: Record<string, string>) => void) {
  const api = useApi()
  const fail = useFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidateSystem()
  return useMutation({
    mutationFn: (body: HookBody) => api.post('v1/plugin-hooks', hookSaved, { body, idempotencyKey: intent.keyFor(body) }),
    onSuccess: (r, body) => {
      intent.reset()
      void invalidate('hooks')
      onSaved(r, body)
    },
    onError: (e) => fail(e, { fields: setErrors, intent }),
  })
}
