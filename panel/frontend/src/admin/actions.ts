/**
 * [INPUT]: 依赖 react 的 useCallback / useState，依赖 ../core/api 的 isApiError / newIdempotencyKey，依赖 ../ui 的 useToast，依赖 ./me 的 useAdminMe
 * [OUTPUT]: 对外提供 useCan、useIntentKey、useFailure，以及它们的纯函数内核 canWith、createIntentKey、classifyFailure 与 FailureAction 类型
 * [POS]: admin 各模块页写操作共用的三件小工具：按权限码判断、一次用户意图一把幂等键、写失败的统一处理（reauth 取消静默、有 fields 标表单、其它 Toast）。hook 只是薄壳，逻辑在纯函数里，admin.test.ts 覆盖
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useCallback, useState } from 'react'
import { isApiError, newIdempotencyKey } from '../core/api'
import { useToast } from '../ui'
import { useAdminMe } from './me'

// ---------------------------------------------------------------------------
// 权限：只看 GET v1/me 的 permissions；me 没回来时一律 false（宁可少显示入口）
// ---------------------------------------------------------------------------
export function canWith(permissions: readonly string[] | undefined, code: string): boolean {
  return permissions?.includes(code) ?? false
}

export function useCan(): (code: string) => boolean {
  const perms = useAdminMe().data?.permissions
  return useCallback((code: string) => canWith(perms, code), [perms])
}

// ---------------------------------------------------------------------------
// 幂等键：一次用户意图一把 key（契约 1.5）。意图用可 JSON 序列化的值描述
// （如 [工单 id, 请求体]）：同一意图的重试与 reauth 重放复用同一把，意图变了
// （换了对象、改了表单）就换新 key——同 key 换请求体后端会回 409 idempotency_key_reuse；
// 成功后 reset，下一次提交必然是新 key。
// ---------------------------------------------------------------------------
export interface IntentKey {
  keyFor: (intent: unknown) => string
  reset: () => void
}

export function createIntentKey(generate: () => string = newIdempotencyKey): IntentKey {
  let slot: { fingerprint: string; key: string } | null = null
  return {
    keyFor(intent) {
      const fingerprint = JSON.stringify(intent)
      if (slot?.fingerprint !== fingerprint) slot = { fingerprint, key: generate() }
      return slot.key
    },
    reset() {
      slot = null
    },
  }
}

/** 组件一生一个槽位（useState 惰性初始化，身份稳定，可放进依赖数组） */
export function useIntentKey(): IntentKey {
  const [intent] = useState(createIntentKey)
  return intent
}

// ---------------------------------------------------------------------------
// 写失败：reauth 对话框被取消（R34）静默；带 fields 且调用方能标表单的交给表单；
// 其它用 Toast 说服务端的话（ApiError.message 是可直接展示的中文）。
// ---------------------------------------------------------------------------
export type FailureAction = { kind: 'silent' } | { kind: 'fields'; fields: Record<string, string> } | { kind: 'toast'; message: string }

export function classifyFailure(error: unknown, canMarkFields: boolean): FailureAction {
  if (isApiError(error, 'reauth_required')) return { kind: 'silent' }
  if (isApiError(error) && canMarkFields && Object.keys(error.fields).length > 0) return { kind: 'fields', fields: { ...error.fields } }
  return { kind: 'toast', message: isApiError(error) ? error.message : '操作失败，请稍后重试' }
}

/** 返回 true 表示已处理（静默或已标到表单），调用方不必再提示；false 表示已 Toast */
export function useFailure(): (error: unknown, setFields?: (fields: Record<string, string>) => void) => boolean {
  const toast = useToast()
  return useCallback(
    (error, setFields) => {
      const action = classifyFailure(error, setFields !== undefined)
      if (action.kind === 'fields') setFields?.(action.fields)
      if (action.kind === 'toast') toast(action.message, 'danger')
      return action.kind !== 'toast'
    },
    [toast],
  )
}
