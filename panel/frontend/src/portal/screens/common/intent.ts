/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../core/api 的 newIdempotencyKey
 * [OUTPUT]: 对外提供 useIntentKey、createIntentKey 与 IntentKey
 * [POS]: portal/screens/common 的幂等键约定（契约 1.5：一次用户意图一个键，重试与重放复用，动作结束后丢弃）：按请求指纹给键，同样的请求（双击、失败重试、关弹窗后再点）拿到同一个键、后端回放同一结果，改了任何参数才换新键；reset() 让下一次同样的请求也换新键（后端给了明确的业务拒绝，或动作已成功结束）；结账、充值、兑换礼品卡、佣金转余额与提现共用
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { newIdempotencyKey } from '../../../core/api'

export type IntentKey = ((request: unknown) => string) & { reset(): void }

/** 组件外也能用的版本（单元测试即如此）；useIntentKey 给每个组件实例一份稳定的 */
export function createIntentKey(newKey: () => string = newIdempotencyKey): IntentKey {
  let last: { fingerprint: string; key: string } | null = null
  const next = (request: unknown) => {
    const fingerprint = JSON.stringify(request)
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
