/**
 * [INPUT]: 依赖 react 的 useCallback / useState，依赖 ../core/api 的 isApiError / newIdempotencyKey，依赖 ../ui 的 useToast，依赖 ./me 的 useAdminMe
 * [OUTPUT]: 对外提供 useCan、useIntentKey、useFailure，以及它们的纯函数内核 canWith、createIntentKey、endsIntent、classifyFailure、handleFailure 与 IntentKey / FailureAction / FailureOptions / Fail 类型
 * [POS]: admin 各模块页写操作共用的三件小工具：按权限码判断、一次用户意图一把幂等键、写失败的统一处理（reauth 取消静默、有 fields 标表单、其它 Toast；传了 intent 时 4xx 业务拒绝在这里丢弃幂等键）。hook 只是薄壳，逻辑在纯函数里，admin.test.ts 覆盖
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
// 成功后调用方 reset，失败交给 useFailure 按 endsIntent 判断（契约 1.5 与 R85：后端只
// 重放 2xx，4xx 业务拒绝后同 key 会重新执行或回 409，所以拒绝即结束这次意图）。
// 自己先处理某些 4xx（如 409 标到表单）、不经 useFailure 的分支，在 catch 开头直接用 endsIntent。
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

/**
 * 这次失败是否结束了用户意图：4xx 是后端的明确答复，丢弃键；reauth 取消（R34）时处理器
 * 没执行、键没消耗，保留；断网（status 0）、5xx 与 2xx 回包解析失败保留键，重试时回放。
 */
export function endsIntent(error: unknown): boolean {
  return isApiError(error) && error.status >= 400 && error.status < 500 && error.code !== 'reauth_required'
}

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
