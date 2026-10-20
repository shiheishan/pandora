// ---------------------------------------------------------------------------
// 访问令牌只保存在内存中（闭包变量）：
// 后台与门户同源（后台只是反代前缀），若存在 localStorage 中，门户任何一处 XSS 都能读走后台令牌。
// 刷新令牌保存在 HttpOnly cookie 中（Path=/v1/auth），前端 JavaScript 无法触碰。
// 登录标志（pandora-<app>-session = "1"）不含任何秘密，仅用于应用启动时判断是否需要尝试调用 refresh 恢复会话，避免匿名访客耗费刷新限速。
// 跨标签页同步使用 BroadcastChannel（或退回 storage 事件），只广播 signed-in 与 signed-out 事件，绝不跨页传输令牌；
// 收到 signed-in 的标签页由自身调用 refreshSession 换取本页独立的 access 令牌。
// 两个入口的标志键与频道名均按入口（admin / portal）严格区分。
// ---------------------------------------------------------------------------

export type AppName = 'admin' | 'portal'

/** 登录标志的 localStorage 键：值恒为 "1"，不含秘密；theme-boot.js 首帧前也读它。 */
export function sessionHintKey(app: AppName): string {
  return `pandora-${app}-session`
}

/** 跨标签页广播的频道名。 */
export function authChannelName(app: AppName): string {
  return `pandora-${app}-auth`
}

export interface TokenStore {
  get(): string | null
  /** 写内存令牌并写登录标志；原来没有令牌时向其它标签页广播 signed-in。每次调用都通知订阅者。 */
  set(token: string): void
  /** 清内存令牌并删登录标志；原来有令牌时通知订阅者并广播 signed-out。原来就没有令牌时只删标志，不通知、不广播。 */
  clear(): void
  /** 有没有登录标志：开机据此决定要不要试一次 refresh。 */
  hasSessionHint(): boolean
  /** 本页令牌变化（本页 set / clear，或收到其它标签页 signed-out 后清掉）时通知；返回取消订阅。 */
  subscribe(notify: () => void): () => void
  /** 其它标签页登录了、而本页此刻没有令牌时通知；返回取消订阅。 */
  onPeerSignIn(fn: () => void): () => void
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

interface ChannelLike {
  postMessage(message: unknown): void
  addEventListener(type: 'message', listener: (event: MessageEvent) => void): void
}

interface TokenStoreOptions {
  /** 登录标志存哪；缺省 window.localStorage，取不到为 null（只记在内存） */
  storage?: StorageLike | null
  /** 跨标签页广播；缺省在浏览器里（typeof window !== 'undefined' 且有 BroadcastChannel）new BroadcastChannel(authChannelName(app))，否则 null */
  channel?: ChannelLike | null
  /** channel 为 null 时退回监听 storage 事件的目标；缺省 window（没有 window 为 null） */
  events?: EventTargetLike | null
}

function defaultStorage(): StorageLike | null {
  if (typeof window === 'undefined') return null
  try {
    return window.localStorage
  } catch {
    return null
  }
}

function defaultChannel(app: AppName): ChannelLike | null {
  if (typeof window === 'undefined' || typeof BroadcastChannel === 'undefined') {
    return null
  }
  try {
    return new BroadcastChannel(authChannelName(app))
  } catch {
    return null
  }
}

export function createTokenStore(app: AppName, options: TokenStoreOptions = {}): TokenStore {
  const hintKey = sessionHintKey(app)
  const storage = options.storage === undefined ? defaultStorage() : options.storage
  const channel = options.channel === undefined ? defaultChannel(app) : options.channel
  const events = options.events === undefined ? (typeof window === 'undefined' ? null : window) : options.events

  let token: string | null = null
  let hintMemory = false

  const subscribers = new Set<() => void>()
  const peerSignInListeners = new Set<() => void>()

  const readHint = (): boolean => {
    try {
      return storage ? storage.getItem(hintKey) === '1' : hintMemory
    } catch {
      return hintMemory
    }
  }

  const writeHint = (on: boolean) => {
    hintMemory = on
    try {
      if (storage) {
        if (on) storage.setItem(hintKey, '1')
        else storage.removeItem(hintKey)
      }
    } catch {
      // 存储写入失败则保留在内存标记中
    }
  }

  const notifySubscribers = () => subscribers.forEach((fn) => fn())
  const notifyPeerSignIn = () => peerSignInListeners.forEach((fn) => fn())

  const broadcast = (type: 'signed-in' | 'signed-out') => {
    try {
      channel?.postMessage({ type })
    } catch {
      // 广播失败不中断流程
    }
  }

  // 跨标签页广播监听（若提供了 channel）
  channel?.addEventListener('message', (event: MessageEvent) => {
    const data = event.data
    if (!data || typeof data !== 'object') return
    const type = (data as { type?: unknown }).type
    if (type === 'signed-out') {
      if (token !== null) {
        token = null
        hintMemory = false
        notifySubscribers()
      }
    } else if (type === 'signed-in') {
      if (token === null) {
        notifyPeerSignIn()
      }
    }
  })

  // 当 channel 为 null 且 events 存在时，退回监听 storage 事件
  if (!channel && events) {
    const onStorage = (event: Event) => {
      const key = (event as StorageEvent).key
      if (key === hintKey || key === null) {
        const hasHint = readHint()
        if (!hasHint && token !== null) {
          token = null
          hintMemory = false
          notifySubscribers()
        } else if (hasHint && token === null) {
          notifyPeerSignIn()
        }
      }
    }
    events.addEventListener('storage', onStorage)
  }

  return {
    get: () => token,
    set: (newToken: string) => {
      const hadToken = token !== null
      token = newToken
      writeHint(true)
      if (!hadToken) {
        broadcast('signed-in')
      }
      notifySubscribers()
    },
    clear: () => {
      const hadToken = token !== null
      token = null
      writeHint(false)
      if (hadToken) {
        notifySubscribers()
        broadcast('signed-out')
      }
    },
    hasSessionHint: () => readHint(),
    subscribe: (notify: () => void) => {
      subscribers.add(notify)
      return () => {
        subscribers.delete(notify)
      }
    },
    onPeerSignIn: (fn: () => void) => {
      peerSignInListeners.add(fn)
      return () => {
        peerSignInListeners.delete(fn)
      }
    },
  }
}
