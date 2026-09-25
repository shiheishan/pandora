/**
 * [INPUT]: 依赖 react 的 useRef，依赖 ../../../core/api 的 newIdempotencyKey
 * [OUTPUT]: 对外提供 useIntentKey
 * [POS]: portal/screens/common 的幂等键约定（契约 1.5：一次用户意图一个键，重试与重放复用）：按请求指纹给键，同样的请求（双击、失败重试、关弹窗后再点）拿到同一个键、后端回放同一结果，改了任何参数才换新键；结账、充值、兑换礼品卡共用
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useRef } from 'react'
import { newIdempotencyKey } from '../../../core/api'

export function useIntentKey(): (request: unknown) => string {
  const last = useRef<{ fingerprint: string; key: string } | null>(null)
  return (request) => {
    const fingerprint = JSON.stringify(request)
    if (last.current?.fingerprint !== fingerprint) last.current = { fingerprint, key: newIdempotencyKey() }
    return last.current.key
  }
}
