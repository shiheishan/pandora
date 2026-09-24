/**
 * [INPUT]: 依赖 vitest，依赖 ./sse 的 createSseParser / openEventStream，依赖 ./api 与 ./token 做真实建流
 * [OUTPUT]: 对外提供 sse.ts 的单元测试
 * [POS]: core/sse 的验收测试：帧解析（任意块边界、CRLF、注释心跳、retry）、断线按 retry+抖动重连并标记 reconnected、4xx 停止、关闭即中断读取
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it, vi } from 'vitest'
import { ApiError, createApiClient } from './api'
import type { SseEvent } from './sse'
import { createSseParser, openEventStream } from './sse'
import { createTokenStore } from './token'

function parseAll(chunks: string[]) {
  const events: SseEvent[] = []
  const retries: number[] = []
  const parser = createSseParser(
    (e) => events.push(e),
    (ms) => retries.push(ms),
  )
  chunks.forEach((c) => parser.push(c))
  parser.end()
  return { events, retries }
}

const BACKEND_FRAMES =
  'retry: 5000\n\n' +
  'id: 1\nevent: orders.changed\ndata: {"table":"orders","op":"INSERT","id":"o1"}\n\n' +
  ': ping\n\n' +
  'id: 2\nevent: ticket.updated\ndata: {"ticket_id":"t1"}\n\n'

describe('SSE frame parser', () => {
  it('parses the backend frame format and ignores ping comments', () => {
    const { events, retries } = parseAll([BACKEND_FRAMES])
    expect(retries).toEqual([5000])
    expect(events).toEqual([
      { id: '1', event: 'orders.changed', data: '{"table":"orders","op":"INSERT","id":"o1"}' },
      { id: '2', event: 'ticket.updated', data: '{"ticket_id":"t1"}' },
    ])
  })

  it('gives the same result however the bytes are chunked, including CRLF split between chunks', () => {
    const crlf = BACKEND_FRAMES.replace(/\n/g, '\r\n')
    const whole = parseAll([crlf]).events
    for (let size = 1; size <= 7; size++) {
      const chunks: string[] = []
      for (let i = 0; i < crlf.length; i += size) chunks.push(crlf.slice(i, i + size))
      expect(parseAll(chunks).events).toEqual(whole)
    }
    expect(whole).toHaveLength(2)
    expect(parseAll(['data: a\r', '\ndata: b\r\r']).events).toEqual([{ id: undefined, event: 'message', data: 'a\nb' }])
  })

  it('joins multi-line data, defaults the type, and drops an unterminated trailing frame', () => {
    const { events } = parseAll(['data: one\ndata:two\n\n', 'event: x\ndata: half'])
    expect(events).toEqual([{ id: undefined, event: 'message', data: 'one\ntwo' }])
  })

  it('ignores a non-numeric retry and frames without data', () => {
    const { events, retries } = parseAll(['retry: soon\nevent: nothing\n\n'])
    expect(retries).toEqual([])
    expect(events).toEqual([])
  })
})

// ---------------------------------------------------------------------------
// 重连：伪造 connect 返回可控的流
// ---------------------------------------------------------------------------
const encoder = new TextEncoder()

function streamOf(text: string, { close = true } = {}): Response {
  return new Response(
    new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(encoder.encode(text))
        if (close) controller.close()
      },
    }),
    { headers: { 'Content-Type': 'text/event-stream' } },
  )
}

describe('openEventStream', () => {
  it('reconnects after the stream ends or the network drops, waiting retry + jitter, and flags reconnects', async () => {
    const events: string[] = []
    const opens: boolean[] = []
    const delays: number[] = []
    const script: Array<() => Response> = [
      () => streamOf('retry: 3000\n\nevent: orders.changed\ndata: {}\n\n'),
      () => {
        throw new TypeError('network down')
      },
      () => streamOf('event: nodes.changed\ndata: {}\n\n', { close: false }),
    ]
    const connect = vi.fn(async () => {
      const next = script.shift()
      if (!next) throw new Error('no more connections expected')
      return next()
    })

    const close = openEventStream({
      connect,
      onEvent: (e) => events.push(e.event),
      onOpen: ({ reconnected }) => opens.push(reconnected),
      random: () => 0.5,
      jitterMs: 1000,
      wait: async (ms) => {
        delays.push(ms)
      },
    })

    await vi.waitFor(() => expect(events).toEqual(['orders.changed', 'nodes.changed']))
    close()
    expect(connect).toHaveBeenCalledTimes(3)
    expect(opens).toEqual([false, true])
    // 首帧把 retry 改成 3000，之后两次等待都用它：3000 + 0.5 × 1000
    expect(delays).toEqual([3500, 3500])
  })

  it('uses the default 5000 ms before any retry field arrives', async () => {
    const delays: number[] = []
    let calls = 0
    const close = openEventStream({
      connect: async () => {
        calls++
        if (calls === 1) throw new TypeError('offline')
        return streamOf('', { close: false })
      },
      onEvent: () => {},
      random: () => 0,
      wait: async (ms) => {
        delays.push(ms)
      },
    })
    await vi.waitFor(() => expect(calls).toBe(2))
    close()
    expect(delays).toEqual([5000])
  })

  it('stops on a 4xx through the real client: 404 means no ops.notification.read', async () => {
    const tokens = createTokenStore('admin', { storage: null, events: null })
    tokens.set('tok')
    const fetchMock = vi.fn(async () =>
      new Response(JSON.stringify({ error: { code: 'not_found', message: '资源不存在或无权访问' } }), { status: 404 }),
    )
    const api = createApiClient({
      tokens,
      fetch: fetchMock as unknown as typeof fetch,
      baseUrl: () => 'https://panel.example/__p__/',
      retryDelays: [],
    })
    const onStop = vi.fn()
    const wait = vi.fn(async () => {})
    openEventStream({ connect: (signal) => api.openStream('v1/events', signal), onEvent: () => {}, onStop, wait })

    await vi.waitFor(() => expect(onStop).toHaveBeenCalledTimes(1))
    expect(onStop.mock.calls[0]![0]).toMatchObject({ status: 404, code: 'not_found' })
    expect(wait).not.toHaveBeenCalled()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as unknown as [URL, RequestInit]
    expect(String(url)).toBe('https://panel.example/__p__/v1/events')
    const headers = new Headers(init.headers)
    expect(headers.get('Accept')).toBe('text/event-stream')
    expect(headers.get('Authorization')).toBe('Bearer tok')
  })

  it('a 401 stops the stream and clears the session', async () => {
    const tokens = createTokenStore('portal', { storage: null, events: null })
    tokens.set('tok')
    const onUnauthorized = vi.fn()
    const api = createApiClient({
      tokens,
      onUnauthorized,
      fetch: (async () => new Response('', { status: 401 })) as typeof fetch,
      baseUrl: () => 'https://portal.example/',
    })
    const onStop = vi.fn()
    openEventStream({ connect: (signal) => api.openStream('v1/events', signal), onEvent: () => {}, onStop, wait: async () => {} })
    await vi.waitFor(() => expect(onStop).toHaveBeenCalledTimes(1))
    expect(tokens.get()).toBeNull()
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })

  it('keeps retrying on 5xx and 429', async () => {
    const statuses = [503, 429, 500]
    let calls = 0
    const close = openEventStream({
      connect: async () => {
        const status = statuses[calls++]
        if (status) {
          throw new ApiError({ status, code: 'service_unavailable', message: 'x' })
        }
        return streamOf('', { close: false })
      },
      onEvent: () => {},
      wait: async () => {},
    })
    await vi.waitFor(() => expect(calls).toBe(4))
    close()
  })

  it('close() cancels an open stream and never reconnects', async () => {
    let cancelled = false
    const connect = vi.fn(
      async () =>
        new Response(
          new ReadableStream<Uint8Array>({
            cancel() {
              cancelled = true
            },
          }),
        ),
    )
    const wait = vi.fn(async () => {})
    const opened = vi.fn()
    const close = openEventStream({ connect, onEvent: () => {}, onOpen: opened, wait })
    await vi.waitFor(() => expect(opened).toHaveBeenCalled())
    close()
    await vi.waitFor(() => expect(cancelled).toBe(true))
    await new Promise((resolve) => setTimeout(resolve, 10))
    expect(connect).toHaveBeenCalledTimes(1)
    expect(wait).not.toHaveBeenCalled()
  })
})
