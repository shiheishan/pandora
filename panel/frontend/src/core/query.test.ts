/**
 * [INPUT]: 依赖 vitest，依赖 @tanstack/react-query 的 QueryClient，依赖 ./query 与 ./api 的 ApiError
 * [OUTPUT]: 对外提供 query.ts 的单元测试
 * [POS]: core/query 的单元测试：只对 5xx 补一次重试、实时失效按 meta.topics 精确命中、同 topic 2 秒节流合并、重连失效全部
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { QueryClient } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from './api'
import { createRealtimeInvalidator, shouldRetryQuery } from './query'

afterEach(() => {
  vi.useRealTimers()
})

describe('shouldRetryQuery', () => {
  const err = (status: number) => new ApiError({ status, code: 'internal_error', message: 'x' })

  it('retries a 5xx once and never a 4xx or a non-API error', () => {
    expect(shouldRetryQuery(0, err(503))).toBe(true)
    expect(shouldRetryQuery(1, err(503))).toBe(false)
    expect(shouldRetryQuery(0, err(404))).toBe(false)
    expect(shouldRetryQuery(0, err(401))).toBe(false)
    expect(shouldRetryQuery(0, new Error('boom'))).toBe(false)
  })
})

describe('createRealtimeInvalidator', () => {
  function seeded() {
    const client = new QueryClient()
    const cache = client.getQueryCache()
    cache.build(client, { queryKey: ['orders'], meta: { topics: ['orders.changed'] } }).setData(1)
    cache.build(client, { queryKey: ['nodes'], meta: { topics: ['nodes.changed', 'data.changed'] } }).setData(1)
    cache.build(client, { queryKey: ['static'] }).setData(1)
    const stale = (key: string) => cache.find({ queryKey: [key] })!.state.isInvalidated
    const reset = () => cache.getAll().forEach((q) => q.setState({ isInvalidated: false }))
    return { client, stale, reset }
  }

  it('invalidates only queries that declared the topic', () => {
    const { client, stale } = seeded()
    const inv = createRealtimeInvalidator(client)
    inv.onEvent('orders.changed')
    expect([stale('orders'), stale('nodes'), stale('static')]).toEqual([true, false, false])
    inv.dispose()
  })

  it('throttles a topic to one invalidation per window, with a trailing one for events inside it', () => {
    vi.useFakeTimers()
    const { client, stale, reset } = seeded()
    const spy = vi.spyOn(client, 'invalidateQueries')
    const inv = createRealtimeInvalidator(client, { throttleMs: 2000 })

    inv.onEvent('nodes.changed')
    inv.onEvent('nodes.changed')
    inv.onEvent('nodes.changed')
    expect(spy).toHaveBeenCalledTimes(1)
    reset()

    vi.advanceTimersByTime(2000)
    expect(spy).toHaveBeenCalledTimes(2)
    expect(stale('nodes')).toBe(true)

    // 尾随那次又开了一个窗口；窗口内安静则到期不再触发
    vi.advanceTimersByTime(2000)
    expect(spy).toHaveBeenCalledTimes(2)

    // 不同 topic 互不节流
    inv.onEvent('nodes.changed')
    inv.onEvent('orders.changed')
    expect(spy).toHaveBeenCalledTimes(4)
    inv.dispose()
  })

  it('a reconnect invalidates everything, since events were lost', () => {
    const { client, stale } = seeded()
    createRealtimeInvalidator(client).onReconnect()
    expect([stale('orders'), stale('nodes'), stale('static')]).toEqual([true, true, true])
  })
})
