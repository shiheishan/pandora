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

/** 一轮探测要用到的外部依赖（抽出来便于单测）。 */
export interface ProbeDeps {
  storage: Pick<Storage, 'getItem' | 'setItem'> | null
  now: () => number
  fetchTicket: () => Promise<string>
  /** 开探测页；探测页结束（完成消息或超时）时调用 onClosed。 */
  openFrame: (ticket: string, onClosed: () => void) => void
}

// 同一页面里同时只跑一轮：「我的套餐」快速切走又切回、开发态 StrictMode 重复挂载时不重复领票据。
let probeInFlight = false

/**
 * 跑一轮探测：到间隔才领票据；领到票据（含空串，表示没资格）后才记探测时间——领票失败（断网、限流、5xx）不记，
 * 下次打开照常再试，不会因为一次失败白等 30 分钟。票据非空才开探测页。
 */
export async function runSubDomainProbe(deps: ProbeDeps): Promise<void> {
  if (probeInFlight || !shouldProbe(deps.now(), deps.storage)) return
  probeInFlight = true
  let ticket: string
  try {
    ticket = await deps.fetchTicket()
  } catch {
    probeInFlight = false
    return
  }
  markProbed(deps.now(), deps.storage)
  if (!ticket) {
    probeInFlight = false
    return
  }
  deps.openFrame(ticket, () => {
    probeInFlight = false
  })
}

/** 开 hidden iframe 跑探测页；收到完成消息或超时后自己移除（与组件生命周期无关）。 */
function openProbeFrame(ticket: string, onClosed: () => void): void {
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
    if (isProbeDone(e, window.location.origin, frame.contentWindow)) close()
  }
  function close() {
    if (timer !== undefined) {
      window.clearTimeout(timer)
      timer = undefined
    }
    window.removeEventListener('message', onMessage)
    frame.remove()
    onClosed()
  }

  window.addEventListener('message', onMessage)
  timer = window.setTimeout(close, PROBE_FRAME_TIMEOUT_MS)
  document.body.appendChild(frame)
}

/**
 * 门户打开「我的套餐」时，30 分钟一次：领票据，开 hidden iframe 跑探测页。
 * 离开页面不打断已经开始的一轮：票据已领、探测时间已记，中途移除只会白白浪费这一轮；探测页最多 30 秒后自己移除。
 */
export function useSubDomainProbe(): void {
  const api = useApi()

  useEffect(() => {
    let storage: Storage | null
    try {
      storage = window.localStorage
    } catch {
      storage = null
    }
    void runSubDomainProbe({
      storage,
      now: Date.now,
      fetchTicket: async () => (await api.post('v1/sub-probe/ticket', ticketResponseSchema)).ticket,
      openFrame: openProbeFrame,
    })
  }, [api])
}
