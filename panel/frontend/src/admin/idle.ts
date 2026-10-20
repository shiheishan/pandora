// ---------------------------------------------------------------------------
// 后台空闲登出：2 小时没有任何操作就退出登录（产品规则：后台会话空闲 2 小时失效）。
// 服务端按会话 last_seen 判空闲，但后台页面有定时轮询（仪表盘、任务），开着的标签页
// 会一直刷新 last_seen，所以前端另记「人的操作」：点击、按键、滚轮、触摸。
// 多个后台标签页经 localStorage 共享最近操作时间，在任一页操作都算；
// 一页登出会清令牌，TokenStore 经 BroadcastChannel 广播 signed-out，其余页跟着回到登录页（最近操作时间不是秘密，照旧放 localStorage）。服务端的空闲判断兜底关掉的标签页。
// ---------------------------------------------------------------------------

export const ADMIN_IDLE_MS = 2 * 60 * 60 * 1000

const ACTIVITY_KEY = 'pandora-admin-activity'
const ACTIVITY_EVENTS = ['pointerdown', 'keydown', 'wheel', 'touchstart'] as const
/** 写 localStorage 的节流间隔：频繁操作时不每次都写。 */
const SHARE_EVERY_MS = 60_000
/** 检查间隔：误差最多一分钟。 */
const CHECK_EVERY_MS = 60_000

interface StorageLike {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
}

interface TargetLike {
  addEventListener(type: string, listener: () => void, options?: AddEventListenerOptions): void
  removeEventListener(type: string, listener: () => void, options?: EventListenerOptions): void
}

interface IdleWatchOptions {
  timeoutMs: number
  onIdle: () => void
  now?: () => number
  storage?: StorageLike | null
  target?: TargetLike | null
  every?: (fn: () => void, ms: number) => () => void
}

function defaultStorage(): StorageLike | null {
  try {
    return window.localStorage
  } catch {
    return null
  }
}

function defaultEvery(fn: () => void, ms: number): () => void {
  const timer = setInterval(fn, ms)
  return () => clearInterval(timer)
}

/** 开始计空闲，返回停止函数；超时后调一次 onIdle 并自行停止。 */
export function startIdleWatch(options: IdleWatchOptions): () => void {
  const now = options.now ?? Date.now
  const storage = options.storage === undefined ? defaultStorage() : options.storage
  const target = options.target === undefined ? (typeof window === 'undefined' ? null : window) : options.target
  const every = options.every ?? defaultEvery

  let last = now()
  let shared = 0
  let stopped = false

  const share = () => {
    shared = last
    try {
      storage?.setItem(ACTIVITY_KEY, String(last))
    } catch {
      // 存储不可用时只按本页计
    }
  }
  const lastActivity = () => {
    let other: number
    try {
      other = Number(storage?.getItem(ACTIVITY_KEY) ?? 0) || 0
    } catch {
      other = 0
    }
    return Math.max(last, other)
  }
  const expired = () => now() - lastActivity() >= options.timeoutMs

  const stop = () => {
    if (stopped) return
    stopped = true
    cancel()
    for (const type of ACTIVITY_EVENTS) target?.removeEventListener(type, onActivity, { capture: true })
  }
  const fire = () => {
    stop()
    options.onIdle()
  }
  // 电脑睡眠醒来后第一下操作：先判是否已经超时，超时就登出，不因这一下续上
  function onActivity() {
    if (stopped) return
    if (expired()) return fire()
    last = now()
    if (last - shared >= SHARE_EVERY_MS) share()
  }
  function check() {
    if (!stopped && expired()) fire()
  }

  share()
  for (const type of ACTIVITY_EVENTS) target?.addEventListener(type, onActivity, { capture: true, passive: true })
  const cancel = every(check, CHECK_EVERY_MS)
  return stop
}
