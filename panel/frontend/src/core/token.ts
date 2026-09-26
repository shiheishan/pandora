/**
 * [INPUT]: 依赖浏览器 localStorage 与 window 的 storage 事件（均可注入，便于测试）
 * [OUTPUT]: 对外提供 TokenStore 接口、createTokenStore、tokenStorageKey
 * [POS]: core 的访问令牌存储，api.ts 从这里读 Bearer、reauth 后换新、401 时清空；第 ⑥ 步的登录态订阅它
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// ---------------------------------------------------------------------------
// 令牌放 localStorage：刷新保持登录态，且两个网关同源——后台只是 nginx 前缀，
// 所以键必须按入口区分，门户与后台的令牌互不覆盖（它们本来也互不通用）。
// 后端没有 refresh 接口，这里只存 access_token；public 登录回的 refresh_token 不存。
// 另一个标签页登录、退出或 reauth 换了令牌，storage 事件让本页跟上。
// ---------------------------------------------------------------------------
export type AppName = 'admin' | 'portal'

export function tokenStorageKey(app: AppName): string {
  return `pandora-${app}-token`
}

export interface TokenStore {
  get(): string | null
  set(token: string): void
  clear(): void
  /** 令牌变化（本页或其它标签页）时通知；返回取消订阅。 */
  subscribe(notify: () => void): () => void
}

interface StorageLike {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem(key: string): void
}

interface EventTargetLike {
  addEventListener(type: 'storage', listener: (event: Event) => void): void
  removeEventListener(type: 'storage', listener: (event: Event) => void): void
}

export interface TokenStoreOptions {
  storage?: StorageLike | null
  events?: EventTargetLike | null
}

function defaultStorage(): StorageLike | null {
  try {
    return window.localStorage
  } catch {
    return null
  }
}

export function createTokenStore(app: AppName, options: TokenStoreOptions = {}): TokenStore {
  const key = tokenStorageKey(app)
  const storage = options.storage === undefined ? defaultStorage() : options.storage
  const events = options.events === undefined ? (typeof window === 'undefined' ? null : window) : options.events
  const listeners = new Set<() => void>()
  // 存储不可用（隐私模式、被禁用）时退回内存：本页可用，只是刷新后要重新登录
  let memory: string | null = null

  const read = (): string | null => {
    try {
      return storage ? storage.getItem(key) : memory
    } catch {
      return memory
    }
  }
  const notify = () => listeners.forEach((fn) => fn())
  const write = (token: string | null) => {
    memory = token
    try {
      if (storage) {
        if (token === null) storage.removeItem(key)
        else storage.setItem(key, token)
      }
    } catch {
      // 写不进去就只留在内存
    }
    notify()
  }
  // key 为 null 表示另一个页面 clear() 了整个存储
  const onStorage = (event: Event) => {
    const changed = (event as StorageEvent).key
    if (changed === key || changed === null) notify()
  }

  return {
    get: () => read() || null,
    set: (token) => write(token),
    clear: () => {
      if (read() !== null) write(null)
    },
    subscribe(fn) {
      if (listeners.size === 0) events?.addEventListener('storage', onStorage)
      listeners.add(fn)
      return () => {
        listeners.delete(fn)
        if (listeners.size === 0) events?.removeEventListener('storage', onStorage)
      }
    },
  }
}
