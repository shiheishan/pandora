/**
 * [INPUT]: 依赖 react 的 context / useSyncExternalStore / effect，依赖 @tanstack/react-query 的 QueryClientProvider，依赖 core 的 api / token / query / sse，依赖 ui 的 ToastProvider
 * [OUTPUT]: 对外提供 AppRuntime 类型、createAppRuntime、RuntimeProvider、useRuntime、useApi、useSignedIn、signOut、useRealtime 与 RealtimeStatus
 * [POS]: shell 的运行时：把 core 的无状态原语组装成每个入口一份的单例（令牌、api 客户端、QueryClient），经 context 交给外框与页面；登录态即「令牌是否存在」
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { QueryClientProvider, type QueryClient } from '@tanstack/react-query'
import { createContext, useContext, useEffect, useRef, useState, useSyncExternalStore, type ReactNode } from 'react'
import { createApiClient, isApiError, noContent, type ApiClient } from '../core/api'
import { createQueryClient, createRealtimeInvalidator } from '../core/query'
import { openEventStream, type SseEvent } from '../core/sse'
import { createTokenStore, type AppName, type TokenStore } from '../core/token'
import { ToastProvider } from '../ui'

// ---------------------------------------------------------------------------
// 每个入口一份运行时：两个网关的令牌互不通用，后台还要注入 reauth 对话框，
// 所以 core 不持有单例，由入口在 main.tsx 里建好传进来。
// 令牌被清空（退出、任一请求 401、另一个标签页退出）时一并清掉查询缓存，
// 下一个登录的人看不到上一个人的数据。
// ---------------------------------------------------------------------------
export interface AppRuntime {
  app: AppName
  tokens: TokenStore
  api: ApiClient
  queryClient: QueryClient
}

export function createAppRuntime(app: AppName, options: { requestReauth?: () => Promise<boolean> } = {}): AppRuntime {
  const tokens = createTokenStore(app)
  const queryClient = createQueryClient()
  const api = createApiClient({ tokens, requestReauth: options.requestReauth })
  tokens.subscribe(() => {
    if (tokens.get() === null) queryClient.clear()
  })
  return { app, tokens, api, queryClient }
}

const RuntimeContext = createContext<AppRuntime | null>(null)

export function RuntimeProvider({ runtime, children }: { runtime: AppRuntime; children: ReactNode }) {
  return (
    <RuntimeContext.Provider value={runtime}>
      <QueryClientProvider client={runtime.queryClient}>
        <ToastProvider>{children}</ToastProvider>
      </QueryClientProvider>
    </RuntimeContext.Provider>
  )
}

export function useRuntime(): AppRuntime {
  const runtime = useContext(RuntimeContext)
  if (!runtime) throw new Error('useRuntime must be used inside <RuntimeProvider>')
  return runtime
}

export const useApi = (): ApiClient => useRuntime().api

/** 有令牌即视为已登录；令牌失效由第一个 401 清掉，界面随之回到登录页。 */
export function useSignedIn(): boolean {
  const { tokens } = useRuntime()
  return useSyncExternalStore(tokens.subscribe, () => tokens.get() !== null, () => false)
}

/** 退出：先请求后端吊销当前会话（失败也不拦），再清本地令牌。 */
export async function signOut(runtime: AppRuntime): Promise<void> {
  try {
    await runtime.api.post('v1/auth/logout', noContent)
  } catch {
    // 会话已失效或网络断开：本地照样退出
  }
  runtime.tokens.clear()
}

// ---------------------------------------------------------------------------
// 实时事件：登录后连 GET v1/events，事件交给失效器按 meta.topics 刷新查询，
// 同时转给调用方（后台顶栏的「实时事件」胶囊要列出来）。
// unavailable = 4xx 停止（后台没有 ops.notification.read 时是 404），胶囊隐藏。
// ---------------------------------------------------------------------------
export type RealtimeStatus = 'off' | 'connecting' | 'open' | 'reconnecting' | 'unavailable'

export function useRealtime(enabled: boolean, onEvent?: (event: SseEvent) => void): RealtimeStatus {
  const { api, queryClient } = useRuntime()
  const [status, setStatus] = useState<RealtimeStatus>('connecting')
  const listener = useRef(onEvent)
  useEffect(() => {
    listener.current = onEvent
  })

  useEffect(() => {
    if (!enabled) return
    const invalidator = createRealtimeInvalidator(queryClient)
    let opened = false
    const close = openEventStream({
      connect: async (signal) => {
        setStatus(opened ? 'reconnecting' : 'connecting')
        return api.openStream('v1/events', signal)
      },
      onOpen: ({ reconnected }) => {
        opened = true
        setStatus('open')
        if (reconnected) invalidator.onReconnect()
      },
      onEvent: (event) => {
        invalidator.onEvent(event.event)
        listener.current?.(event)
      },
      onStop: (error) => setStatus(isApiError(error) && error.status !== 401 ? 'unavailable' : 'off'),
    })
    return () => {
      close()
      invalidator.dispose()
      setStatus('off')
    }
  }, [enabled, api, queryClient])

  return enabled ? status : 'off'
}
