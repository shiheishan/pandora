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
