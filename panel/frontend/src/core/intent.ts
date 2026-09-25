/**
 * [INPUT]: 依赖 react 的 useState，依赖 ./api 的 isApiError / newIdempotencyKey
 * [OUTPUT]: 对外提供 IntentKey、createIntentKey、useIntentKey、endsIntent
 * [POS]: core 的幂等键约定（契约 1.5 与 R85），两个入口共用一份：一次用户意图一把键，同一意图的重试与 reauth 重放复用它，意图变了换新键，动作结束后丢弃；后台 admin/actions.ts 原样转出，门户 portal/screens/common/intent.ts 在它之上加下单防重复
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { isApiError, newIdempotencyKey } from './api'

// ---------------------------------------------------------------------------
// 意图用可 JSON 序列化的值描述（如 [工单 id, 请求体]）：指纹相同拿到同一把键——双击、断网或
// 5xx 后重试、reauth 重放，后端都回放同一结果；指纹变了（换了对象、改了表单）就换新键，
// 因为同键换请求体后端会回 409 idempotency_key_reuse。成功后调用方 reset()。
// ---------------------------------------------------------------------------
export interface IntentKey {
  keyFor: (intent: unknown) => string
  reset: () => void
}

/** 组件外也能用（单元测试即如此）；generate 可注入以便断言 */
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
  const [intent] = useState(() => createIntentKey())
  return intent
}

/**
 * 这次失败是否结束了用户意图：后端只重放 2xx（R85），4xx 是明确答复，同键再发会重新执行或回 409，
 * 所以丢弃键；reauth 被取消（R34）时处理器没执行、键没消耗，保留；断网（status 0）、5xx 与 2xx
 * 回包解析失败也保留，重试时回放
 */
export function endsIntent(error: unknown): boolean {
  return isApiError(error) && error.status >= 400 && error.status < 500 && error.code !== 'reauth_required'
}
