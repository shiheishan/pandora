/**
 * [INPUT]: 依赖 react 的 useCallback，依赖 ../core/api 的 isApiError，依赖 ../core/intent 的幂等键（转出），依赖 ../ui 的 useToast，依赖 ./me 的 useAdminMe
 * [OUTPUT]: 对外提供 useCan、useIntentKey、useFailure，以及纯函数内核 canWith、classifyFailure、handleFailure 与 FailureAction / FailureOptions / Fail 类型；转出 core/intent 的 useIntentKey、createIntentKey、endsIntent 与 IntentKey
 * [POS]: admin 各模块页写操作共用的三件小工具：按权限码判断、一次用户意图一把幂等键（实现在 core/intent，这里转出）、写失败的统一处理（reauth 取消静默、有 fields 标表单、其它 Toast；传了 intent 时 4xx 业务拒绝在这里丢弃幂等键）。hook 只是薄壳，逻辑在纯函数里，admin.test.ts 覆盖
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useCallback } from 'react'
import { isApiError } from '../core/api'
import { endsIntent, type IntentKey } from '../core/intent'
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
// 幂等键：实现收在 core/intent.ts（两个入口共用一份，第 4 阶段 ④），这里原样转出，
// 后台各页照旧从 actions 取 useIntentKey / createIntentKey / endsIntent / IntentKey。
// 自己先处理某些 4xx（如 409 标到表单）、不经 useFailure 的分支，在 catch 开头直接用 endsIntent。
// ---------------------------------------------------------------------------
export { createIntentKey, endsIntent, useIntentKey, type IntentKey } from '../core/intent'

// ---------------------------------------------------------------------------
// 写失败：reauth 对话框被取消（R34）静默；带 fields 且调用方能标表单的交给表单；
// 其它用 Toast 说服务端的话（ApiError.message 是可直接展示的中文）。
// 带幂等键的写操作把 intent 一并交进来，键的去留在这一处按 endsIntent 定，页面不各写一遍。
// ---------------------------------------------------------------------------
export type FailureAction = { kind: 'silent' } | { kind: 'fields'; fields: Record<string, string> } | { kind: 'toast'; message: string }

export function classifyFailure(error: unknown, canMarkFields: boolean): FailureAction {
  if (isApiError(error, 'reauth_required')) return { kind: 'silent' }
  if (isApiError(error) && canMarkFields && Object.keys(error.fields).length > 0) return { kind: 'fields', fields: { ...error.fields } }
  return { kind: 'toast', message: isApiError(error) ? error.message : '操作失败，请稍后重试' }
}

export interface FailureOptions {
  /** 能标到表单时传：422 的 fields 交给它，不再 Toast */
  fields?: (fields: Record<string, string>) => void
  /** 这次写操作用的幂等键槽位：失败结束意图时在这里 reset */
  intent?: Pick<IntentKey, 'reset'>
}

/** 第二参数可以只传标表单的回调（无幂等键的写操作），也可以传 FailureOptions */
export type Fail = (error: unknown, handle?: FailureOptions['fields'] | FailureOptions) => boolean

/**
 * 写失败的完整处理（纯函数内核，toast 由调用方注入）：先按 endsIntent 定幂等键去留，再
 * 静默 / 标表单 / Toast。返回 true 表示已处理（静默或已标到表单），调用方不必再提示；false 表示已 Toast
 */
export function handleFailure(error: unknown, handle: Parameters<Fail>[1], toast: (message: string) => void): boolean {
  const { fields: setFields, intent }: FailureOptions = typeof handle === 'function' ? { fields: handle } : (handle ?? {})
  if (intent && endsIntent(error)) intent.reset()
  const action = classifyFailure(error, setFields !== undefined)
  if (action.kind === 'fields') setFields?.(action.fields)
  if (action.kind === 'toast') toast(action.message)
  return action.kind !== 'toast'
}

export function useFailure(): Fail {
  const toast = useToast()
  return useCallback<Fail>((error, handle) => handleFailure(error, handle, (message) => toast(message, 'danger')), [toast])
}
