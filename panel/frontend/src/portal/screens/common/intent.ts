/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../core/intent 的幂等键（转出）
 * [OUTPUT]: 对外提供 usePlacedOrder、createPlacedOrder、PlacedOrder、recallPayable；转出 core/intent 的 useIntentKey、createIntentKey、IntentKey、endsIntent
 * [POS]: portal/screens/common 的写操作约定：幂等键实现在 core/intent（按请求指纹给键，keyFor 取键、成功或 4xx 业务拒绝后 reset，与后台同一份），这里转出并加下单防重复。下单类动作成功后键已丢弃，usePlacedOrder 记住刚下的待支付单，同样的请求在有效期内再点就重开这张单的支付，不再下第二张（否则余额抵扣会冻结两次）；重开前经 recallPayable 问一次它还能不能付，已取消、超时或已支付就 forget() 并按新请求下单
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'

// 幂等键本身收在 core/intent（两个入口共用一份，第 4 阶段 ④），门户各页照旧从这里取
export { createIntentKey, endsIntent, useIntentKey, type IntentKey } from '../../../core/intent'

const fingerprintOf = (request: unknown) => JSON.stringify(request)

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

/**
 * 同样的请求再点时先调它：记下的单还能付就返回它（调用方重开支付）；已不能付——在别处取消、
 * 服务端先一步超时、已在另一个标签页付掉——就 forget() 并返回 null，调用方按新请求下单。
 * payable 由调用方给（查订单详情），查询本身断网或 5xx 时应答「能」，交给支付弹窗报错与重试。
 */
export async function recallPayable<T>(placed: PlacedOrder<T>, request: unknown, payable: (value: T) => Promise<boolean>): Promise<T | null> {
  const value = placed.recall(request)
  if (value === null) return null
  if (await payable(value)) return value
  placed.forget()
  return null
}
