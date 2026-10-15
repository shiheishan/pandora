// ---------------------------------------------------------------------------
// 订阅域名探测与上报（xb-subdomain）
//
// 这是为订阅域名调权重做的后台探测，不显示任何东西。
// 为什么直接用 fetch 访问别的源而不走 api：
// 探测的就是这些订阅域名本身能不能连上，请求不带凭据、不带 Referer，只看状态码。
// 被 CSP 拦下的不上报，免得把「本页不允许连」当成「域名连不上」。
// ---------------------------------------------------------------------------

import { useEffect } from 'react'
import { z } from 'zod'
import { noContent } from '../../../core/api'
import { useApi } from '../../../shell/runtime'

export const PROBE_PATH = '/v1/sub-probe'
export const PROBE_TIMEOUT_MS = 5000
export const PROBE_INTERVAL_MS = 30 * 60_000
export const PROBE_STORAGE_KEY = 'pandora.subProbeAt'

export type ProbeOutcome = 'ok' | 'fail' | 'blocked'
export interface ProbeResult {
  base_url: string
  ok: boolean
}

export interface ProbeDeps {
  fetch: typeof fetch
  timeoutMs: number
  wasBlocked: (base: string) => boolean
}

const domainsResponseSchema = z.object({
  domains: z.array(z.string()),
})

/** 对单个基础 URL 发起极小探测请求，判定 ok / fail / blocked。 */
export async function probeDomain(base: string, deps: ProbeDeps): Promise<ProbeOutcome> {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), deps.timeoutMs)
  try {
    const res = await deps.fetch(base + PROBE_PATH, {
      mode: 'cors',
      credentials: 'omit',
      cache: 'no-store',
      referrerPolicy: 'no-referrer',
      signal: controller.signal,
    })
    return res.status === 204 ? 'ok' : 'fail'
  } catch {
    // 违规事件（securitypolicyviolation）可能异步派发，让出事件循环宏任务以捕获
    await new Promise((r) => setTimeout(r, 0))
    return deps.wasBlocked(base) ? 'blocked' : 'fail'
  } finally {
    clearTimeout(timer)
  }
}

/** 并行探测所有域名，丢弃 blocked 项，保留顺序。 */
export async function probeAll(bases: readonly string[], deps: ProbeDeps): Promise<ProbeResult[]> {
  const outcomes = await Promise.all(bases.map((b) => probeDomain(b, deps)))
  const results: ProbeResult[] = []
  for (let i = 0; i < bases.length; i++) {
    const outcome = outcomes[i]
    if (outcome === 'ok') {
      results.push({ base_url: bases[i]!, ok: true })
    } else if (outcome === 'fail') {
      results.push({ base_url: bases[i]!, ok: false })
    }
  }
  return results
}

/** 距离上次探测是否超过间隔（30 分钟）。无记录或损坏时应探测。 */
export function shouldProbe(now: number, storage: Pick<Storage, 'getItem'> | null): boolean {
  if (!storage) return true
  try {
    const item = storage.getItem(PROBE_STORAGE_KEY)
    if (!item) return true
    const parsed = Number(item)
    if (!Number.isFinite(parsed) || parsed <= 0) return true
    return now - parsed >= PROBE_INTERVAL_MS
  } catch {
    return true
  }
}

/** 记录本次探测的时间戳。异常静默。 */
export function markProbed(now: number, storage: Pick<Storage, 'setItem'> | null): void {
  if (!storage) return
  try {
    storage.setItem(PROBE_STORAGE_KEY, String(now))
  } catch {
    // 忽略写入异常（无痕模式、QuotaExceeded 等）
  }
}

/** 门户打开「我的套餐」时自动探测所在组的订阅域名并静默上报结果。 */
export function useSubDomainProbe(): void {
  const api = useApi()

  useEffect(() => {
    let cancelled = false

    const run = async () => {
      let storage: Storage | null = null
      try {
        storage = window.localStorage
      } catch {
        storage = null
      }

      const now = Date.now()
      if (!shouldProbe(now, storage)) return

      let domains: string[] = []
      try {
        const data = await api.get('v1/me/subscription-domains', domainsResponseSchema)
        domains = data.domains
      } catch {
        return
      }

      if (cancelled || domains.length === 0) return

      // 先标记已探测，避免并发与短期内重复探测
      markProbed(now, storage)

      const blockedSet = new Set<string>()
      const onViolation = (e: SecurityPolicyViolationEvent) => {
        if (e.blockedURI) blockedSet.add(e.blockedURI)
      }

      document.addEventListener('securitypolicyviolation', onViolation)

      try {
        const deps: ProbeDeps = {
          fetch: window.fetch.bind(window),
          timeoutMs: PROBE_TIMEOUT_MS,
          wasBlocked: (base) => [...blockedSet].some((u) => u.startsWith(base)),
        }
        const results = await probeAll(domains, deps)
        if (cancelled || results.length === 0) return

        await api.post('v1/me/subscription-domains/probe', noContent, { body: { results } })
      } catch {
        // 全过程静默，不报错
      } finally {
        document.removeEventListener('securitypolicyviolation', onViolation)
      }
    }

    void run()

    return () => {
      cancelled = true
    }
  }, [api])
}
