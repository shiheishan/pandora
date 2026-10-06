// ---------------------------------------------------------------------------
// api 客户端在 React 之外创建，而对话框在 React 之内：用一个可订阅的小仓库接起来。
// api.ts 已保证并发的多个被拦请求只调一次 request()，这里再兜一层：
// 已有待决请求时复用同一个 Promise。
// ---------------------------------------------------------------------------
export interface ReauthController {
  request(): Promise<boolean>
  isPending(): boolean
  subscribe(notify: () => void): () => void
  resolve(ok: boolean): void
}

export function createReauthController(): ReauthController {
  let pending: { promise: Promise<boolean>; settle: (ok: boolean) => void } | null = null
  const listeners = new Set<() => void>()
  const notify = () => listeners.forEach((fn) => fn())

  return {
    request() {
      if (!pending) {
        let settle!: (ok: boolean) => void
        const promise = new Promise<boolean>((resolve) => {
          settle = resolve
        })
        pending = { promise, settle }
        notify()
      }
      return pending.promise
    },
    isPending: () => pending !== null,
    subscribe(fn) {
      listeners.add(fn)
      return () => listeners.delete(fn)
    },
    resolve(ok) {
      const current = pending
      pending = null
      current?.settle(ok)
      notify()
    },
  }
}
