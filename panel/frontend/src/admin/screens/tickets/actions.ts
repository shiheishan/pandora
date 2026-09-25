/**
 * [INPUT]: 依赖 react 的 useCallback / useRef，依赖 ../../../core/api 的 isApiError / newIdempotencyKey，依赖 ../../../ui 的 useToast，依赖 ../../me 的 useAdminMe
 * [OUTPUT]: 对外提供 useCan、useIntentKey、useFailure
 * [POS]: admin/screens/tickets 写操作的三件小工具：按权限码判断、一次用户意图一个幂等键、失败的统一处理（reauth 取消静默、有 fields 标到表单、其它 Toast）。与后台前端二 marketing/queries.ts 里的同名三件同一做法，已报告协调会话决定是否提升到共享层
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useCallback, useRef } from 'react'
import { isApiError, newIdempotencyKey } from '../../../core/api'
import { useToast } from '../../../ui'
import { useAdminMe } from '../../me'

export function useCan(): (code: string) => boolean {
  const perms = useAdminMe().data?.permissions
  return useCallback((code: string) => perms?.includes(code) ?? false, [perms])
}

/**
 * 幂等键：一次用户意图一个 key。同一份意图（fingerprint 相同）的重试与 reauth 重放复用它，
 * 意图变了（换了工单、改了正文）就是新 key；成功后 reset，下一次必然是新 key。
 */
export function useIntentKey() {
  const slot = useRef<{ fingerprint: string; key: string } | null>(null)
  const keyFor = useCallback((intent: unknown) => {
    const fingerprint = JSON.stringify(intent)
    if (slot.current?.fingerprint !== fingerprint) slot.current = { fingerprint, key: newIdempotencyKey() }
    return slot.current.key
  }, [])
  const reset = useCallback(() => {
    slot.current = null
  }, [])
  return { keyFor, reset }
}

/**
 * 写操作失败：reauth 对话框被取消（R34）静默；带 fields 的交给表单标红；其它用 Toast 说服务端的话。
 * 返回 true 表示已经在表单里处理了，调用方不必再提示。
 */
export function useFailure() {
  const toast = useToast()
  return useCallback(
    (error: unknown, setFields?: (fields: Record<string, string>) => void): boolean => {
      if (isApiError(error, 'reauth_required')) return true
      if (isApiError(error) && setFields && Object.keys(error.fields).length > 0) {
        setFields({ ...error.fields })
        return true
      }
      toast(isApiError(error) ? error.message : '操作失败，请稍后重试', 'danger')
      return false
    },
    [toast],
  )
}
