// ---------------------------------------------------------------------------
// 订阅域名探测的门户一侧。门户只领票据、开 hidden iframe；真正的探测与上报在面板下发的
// 独立探测页里做（它的 CSP 只列本人所在组的订阅域名，门户入口页的 CSP 不含任何订阅域名）。
// 全程静默，不显示任何东西，出错不提示。
// ---------------------------------------------------------------------------

import { useEffect } from 'react'
import { z } from 'zod'
import { resolveApiUrl } from '../../../core/api'
import { useApi } from '../../../shell/runtime'

export const PROBE_INTERVAL_MS = 30 * 60_000
export const PROBE_STORAGE_KEY = 'pandora.subProbeAt'
export const PROBE_FRAME_TIMEOUT_MS = 30_000
export const PROBE_DONE_TYPE = 'pandora-sub-probe-done'

const ticketResponseSchema = z.object({ ticket: z.string() })

/** 距离上次探测是否超过间隔（30 分钟）。无记录、损坏、读不了时应探测。 */
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

/** 探测页地址：与 API 同一套相对解析（入口目录下的 v1/），票据放查询参数 t。 */
export function probeFrameURL(ticket: string, base: string): string {
  return resolveApiUrl('v1/sub-probe/frame', base, { t: ticket }).toString()
}

/** 是否是探测页发来的完成消息：来源必须同源、发送者必须是我们开的那个 iframe、data.type 对得上。 */
export function isProbeDone(
  e: { origin: string; source: unknown; data: unknown },
  origin: string,
  frameWindow: unknown,
): boolean {
  return (
    e.origin === origin &&
    frameWindow != null &&
    e.source === frameWindow &&
    typeof e.data === 'object' &&
    e.data !== null &&
    (e.data as { type?: unknown }).type === PROBE_DONE_TYPE
  )
}

/** 门户打开「我的套餐」时，30 分钟一次：领票据，开 hidden iframe 跑探测页，完成或超时后移除。 */
export function useSubDomainProbe(): void {
  const api = useApi()

  useEffect(() => {
    let cancelled = false
    let teardown: () => void = () => {}

    const run = async () => {
      let storage: Storage | null
      try {
        storage = window.localStorage
      } catch {
        storage = null
      }

      const now = Date.now()
      if (!shouldProbe(now, storage)) return

      markProbed(now, storage)

      let ticket: string
      try {
        const data = await api.post('v1/sub-probe/ticket', ticketResponseSchema)
        ticket = data.ticket
      } catch {
        return
      }

      if (cancelled || !ticket) return

      const frame = document.createElement('iframe')
      frame.hidden = true
      frame.setAttribute('aria-hidden', 'true')
      frame.tabIndex = -1
      frame.title = ''
      frame.referrerPolicy = 'no-referrer'
      frame.src = probeFrameURL(ticket, document.baseURI)

      let timer: number | undefined

      // 两个函数互相引用，用函数声明（提升），不靠 const 的先后顺序
      function onMessage(e: MessageEvent) {
        if (isProbeDone(e, window.location.origin, frame.contentWindow)) doTeardown()
      }
      function doTeardown() {
        if (timer !== undefined) {
          window.clearTimeout(timer)
          timer = undefined
        }
        window.removeEventListener('message', onMessage)
        frame.remove()
      }
      teardown = doTeardown

      window.addEventListener('message', onMessage)
      timer = window.setTimeout(doTeardown, PROBE_FRAME_TIMEOUT_MS)
      document.body.appendChild(frame)
    }

    void run()

    return () => {
      cancelled = true
      teardown()
    }
  }, [api])
}
