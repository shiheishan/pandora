/**
 * [INPUT]: 依赖 @tanstack/react-query 的 QueryClient，依赖 ./api 的 ApiError / isApiError
 * [OUTPUT]: 对外提供 REALTIME_TOPICS 与 RealtimeTopic、createQueryClient、shouldRetryQuery、createRealtimeInvalidator；为 react-query 登记 ApiError 为默认错误类型、queryMeta.topics
 * [POS]: core 的服务端状态层：页面的读用 useQuery、写用 useMutation，都经 api.ts 取数；sse.ts 的事件经这里的失效器变成按 topic 的查询失效
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { QueryClient } from '@tanstack/react-query'
import type { ApiError } from './api'
import { isApiError } from './api'

// ---------------------------------------------------------------------------
// 实时 topic：platform/realtime/listener.go 的 topicFor。事件只是「哪块该刷新」
// 的信号，不带业务内容；查询在 meta.topics 里声明自己关心哪些 topic，
// 失效器按它精确失效，页面之间互不知道对方。
// ---------------------------------------------------------------------------
export const REALTIME_TOPICS = [
  'orders.changed',
  'subscriptions.changed',
  'tickets.changed',
  'plans.changed',
  'nodes.changed',
  'announcements.changed',
  'data.changed',
  'ticket.updated',
] as const
export type RealtimeTopic = (typeof REALTIME_TOPICS)[number]

declare module '@tanstack/react-query' {
  interface Register {
    defaultError: ApiError | Error
    queryMeta: { topics?: readonly RealtimeTopic[] }
  }
}

// ---------------------------------------------------------------------------
// 重试：网络失败 api.ts 已经按退避重发过，这里只对 5xx 再补一次；
// 4xx（含 404 无权限、401 会话失效）重试没有意义。写操作一律不在这一层重试：
// 重发要复用幂等键，由 api.ts 在同一次调用内完成。
// ---------------------------------------------------------------------------
export function shouldRetryQuery(failureCount: number, error: unknown): boolean {
  return failureCount < 1 && isApiError(error) && error.status >= 500
}

export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: shouldRetryQuery,
        staleTime: 30_000,
      },
      mutations: { retry: false },
    },
  })
}

// ---------------------------------------------------------------------------
// 失效器：同一 topic 2 秒节流——节点每次上报流量都会给全租户推一条
// subscriptions.changed（quota_balances 没有 user_id），不节流就是一场刷新风暴。
// 首个事件立即失效，窗口内再来的合并成窗口结束时的一次。
// 重连时事件已丢失、id 不能续传，直接失效全部查询（react-query 只重拉活跃的）。
// ---------------------------------------------------------------------------
export interface RealtimeInvalidator {
  onEvent(topic: string): void
  onReconnect(): void
  dispose(): void
}

export function createRealtimeInvalidator(
  client: QueryClient,
  options: { throttleMs?: number; setTimer?: typeof setTimeout; clearTimer?: typeof clearTimeout } = {},
): RealtimeInvalidator {
  const throttleMs = options.throttleMs ?? 2000
  const setTimer = options.setTimer ?? setTimeout
  const clearTimer = options.clearTimer ?? clearTimeout
  const windows = new Map<string, { timer: ReturnType<typeof setTimeout>; pending: boolean }>()

  const invalidate = (topic: string) =>
    void client.invalidateQueries({
      predicate: (query) => query.meta?.topics?.some((t) => t === topic) === true,
    })

  function open(topic: string) {
    const timer = setTimer(() => {
      const slot = windows.get(topic)
      windows.delete(topic)
      if (slot?.pending) {
        invalidate(topic)
        open(topic)
      }
    }, throttleMs)
    windows.set(topic, { timer, pending: false })
  }

  return {
    onEvent(topic) {
      const slot = windows.get(topic)
      if (slot) {
        slot.pending = true
        return
      }
      invalidate(topic)
      open(topic)
    },
    onReconnect() {
      void client.invalidateQueries()
    },
    dispose() {
      windows.forEach((w) => clearTimer(w.timer))
      windows.clear()
    },
  }
}
