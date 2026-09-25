/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../core/api 的 ApiError / newIdempotencyKey
 * [OUTPUT]: 对外提供 useIntentKey、createIntentKey、IntentKey、endsIntent、usePlacedOrder、createPlacedOrder、PlacedOrder
 * [POS]: portal/screens/common 的幂等键约定（契约 1.5：一次用户意图一个键，重试与重放复用，动作结束后丢弃）：按请求指纹给键，同样的请求（双击、断网或 5xx 后重试）拿到同一个键、后端回放同一结果，改了任何参数才换新键；成功或 4xx 业务拒绝即动作结束，调用方 reset()（协调会话定的统一口径，所有门户写操作都照此）。下单类动作成功后键已丢弃，usePlacedOrder 记住刚下的待支付单，同样的请求在有效期内再点就重开这张单的支付，不再下第二张（否则余额抵扣会冻结两次）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { ApiError, newIdempotencyKey } from '../../../core/api'

export type IntentKey = ((request: unknown) => string) & { reset(): void }

const fingerprintOf = (request: unknown) => JSON.stringify(request)

/** 组件外也能用的版本（单元测试即如此）；useIntentKey 给每个组件实例一份稳定的 */
export function createIntentKey(newKey: () => string = newIdempotencyKey): IntentKey {
  let last: { fingerprint: string; key: string } | null = null
  const next = (request: unknown) => {
    const fingerprint = fingerprintOf(request)
    if (last?.fingerprint !== fingerprint) last = { fingerprint, key: newKey() }
    return last.key
  }
  return Object.assign(next, {
    reset: () => {
      last = null
    },
  })
}

export function useIntentKey(): IntentKey {
  const [intent] = useState(() => createIntentKey())
  return intent
}

/** 这次失败是否结束了用户意图：4xx 是后端给的明确答复，丢弃键；断网（status 0）与 5xx 保留键以便重试回放 */
export const endsIntent = (error: unknown) => error instanceof ApiError && error.status >= 400 && error.status < 500

// ---------------------------------------------------------------------------
// 刚下的待支付单：键已随成功丢弃，同样的请求再点时先看这里
// ---------------------------------------------------------------------------
export interface PlacedOrder<T> {
  /** 同样的请求、且还在有效期内，返回当时记下的值 */
  recall(request: unknown): T | null
  remember(request: unknown, value: T): void
  forget(): void
}

/** 订单 30 分钟未支付即过期（契约门户-03） */
const ORDER_TTL_MS = 30 * 60_000

export function createPlacedOrder<T>(now: () => number = Date.now, ttlMs = ORDER_TTL_MS): PlacedOrder<T> {
  let placed: { fingerprint: string; value: T; at: number } | null = null
  return {
    recall: (request) => (placed && placed.fingerprint === fingerprintOf(request) && now() - placed.at < ttlMs ? placed.value : null),
    remember: (request, value) => {
      placed = { fingerprint: fingerprintOf(request), value, at: now() }
    },
    forget: () => {
      placed = null
    },
  }
}

export function usePlacedOrder<T>(): PlacedOrder<T> {
  const [placed] = useState(() => createPlacedOrder<T>())
  return placed
}
