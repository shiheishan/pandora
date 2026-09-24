/**
 * [INPUT]: 依赖 ./api 的 isApiError 判定不可恢复的失败，依赖浏览器 ReadableStream / TextDecoder / setTimeout
 * [OUTPUT]: 对外提供 SseEvent 类型、createSseParser、EventStreamOptions、openEventStream
 * [POS]: core 的实时事件流：fetch 流读 text/event-stream（Bearer 头，不能用 EventSource），断线按 retry 加抖动重连；连接由 api.openStream 提供，事件交给 query.ts 做失效
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { isApiError } from './api'

// ---------------------------------------------------------------------------
// 帧解析：按 WHATWG SSE 规范逐行处理，行尾 CRLF / LF / CR 都认，块边界可以切在
// 任意位置（包括 CR 与 LF 之间）。后端的帧是：首帧 retry: 5000；事件帧
// id / event / data 各一行；每 25 秒一个注释帧 ": ping"。
// ---------------------------------------------------------------------------
export interface SseEvent {
  /** 本连接内的序号，后端不支持 Last-Event-ID 续传，只用于排查。 */
  id?: string
  event: string
  data: string
}

export interface SseParser {
  push(chunk: string): void
  /** 流结束：未以空行收尾的半帧按规范丢弃。 */
  end(): void
}

export function createSseParser(onEvent: (event: SseEvent) => void, onRetry: (ms: number) => void): SseParser {
  let buffer = ''
  let data: string[] = []
  let eventType = ''
  let lastId: string | undefined

  function dispatch() {
    if (data.length > 0) onEvent({ id: lastId, event: eventType || 'message', data: data.join('\n') })
    data = []
    eventType = ''
  }

  function line(text: string) {
    if (text === '') return dispatch()
    if (text.startsWith(':')) return
    const colon = text.indexOf(':')
    const field = colon < 0 ? text : text.slice(0, colon)
    let value = colon < 0 ? '' : text.slice(colon + 1)
    if (value.startsWith(' ')) value = value.slice(1)
    switch (field) {
      case 'data':
        data.push(value)
        break
      case 'event':
        eventType = value
        break
      case 'id':
        if (!value.includes('\0')) lastId = value
        break
      case 'retry':
        if (/^\d+$/.test(value)) onRetry(Number(value))
        break
    }
  }

  return {
    push(chunk) {
      buffer += chunk
      for (;;) {
        const match = /\r\n|\r|\n/.exec(buffer)
        if (!match) break
        // 块恰好以 CR 结尾：可能是 CRLF 的前半，等下一块再判
        if (match[0] === '\r' && match.index === buffer.length - 1) break
        line(buffer.slice(0, match.index))
        buffer = buffer.slice(match.index + match[0].length)
      }
    },
    end() {
      if (buffer.endsWith('\r')) line(buffer.slice(0, -1))
      buffer = ''
      data = []
      eventType = ''
    },
  }
}

// ---------------------------------------------------------------------------
// 重连：流正常结束、网络断开、5xx、429 都等 retry（后端首帧给 5000）加随机抖动后
// 重连，避免所有标签页同一刻扑回来；4xx（401 会话失效、404 没有 ops.notification.read）
// 不会自愈，停下交给界面处理。id 只在一条连接内有意义、没有续传，所以每次重连成功
// 都以 reconnected=true 通知调用方把相关查询全部失效重拉。
// ---------------------------------------------------------------------------
export interface EventStreamOptions {
  /** 建立连接，一般是 (signal) => api.openStream('v1/events', signal)。 */
  connect: (signal: AbortSignal) => Promise<Response>
  onEvent: (event: SseEvent) => void
  /** 每次连上时调用；reconnected=true 表示此前断过，期间的事件已丢失。 */
  onOpen?: (info: { reconnected: boolean }) => void
  /** 不可恢复的失败（4xx），流不再重连。 */
  onStop?: (error: unknown) => void
  /** 首帧 retry 到达之前的重连间隔，默认 5000。 */
  retryMs?: number
  /** 抖动上限，默认 1000：实际间隔 = retry + random() * jitter。 */
  jitterMs?: number
  random?: () => number
  wait?: (ms: number, signal: AbortSignal) => Promise<void>
}

function fatal(error: unknown): boolean {
  return isApiError(error) && error.status >= 400 && error.status < 500 && error.status !== 408 && error.status !== 429
}

function defaultWait(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(done, ms)
    function done() {
      signal.removeEventListener('abort', done)
      clearTimeout(timer)
      resolve()
    }
    signal.addEventListener('abort', done, { once: true })
  })
}

/** 打开一条自动重连的事件流，返回关闭函数。 */
export function openEventStream(options: EventStreamOptions): () => void {
  const controller = new AbortController()
  const { signal } = controller
  const random = options.random ?? Math.random
  const jitter = options.jitterMs ?? 1000
  const wait = options.wait ?? defaultWait
  let retry = options.retryMs ?? 5000

  async function readOnce(res: Response) {
    if (!res.body) throw new Error('event stream response has no body')
    const reader = res.body.getReader()
    const decoder = new TextDecoder()
    const parser = createSseParser(options.onEvent, (ms) => {
      retry = ms
    })
    const cancel = () => void reader.cancel().catch(() => {})
    signal.addEventListener('abort', cancel, { once: true })
    try {
      for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        parser.push(decoder.decode(value, { stream: true }))
      }
      parser.push(decoder.decode())
    } finally {
      parser.end()
      signal.removeEventListener('abort', cancel)
    }
  }

  async function run() {
    for (let attempt = 0; !signal.aborted; attempt++) {
      try {
        const res = await options.connect(signal)
        if (signal.aborted) return
        options.onOpen?.({ reconnected: attempt > 0 })
        await readOnce(res)
      } catch (error) {
        if (signal.aborted) return
        if (fatal(error)) {
          options.onStop?.(error)
          return
        }
      }
      if (signal.aborted) return
      await wait(retry + random() * jitter, signal)
    }
  }

  void run()
  return () => controller.abort()
}
