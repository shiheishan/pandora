// ---------------------------------------------------------------------------
// 登录状态（四种，SessionState）：
// - unknown：开机正在凭 HttpOnly refresh cookie 恢复会话（不闪登录页、不闪首页）
// - signed-in：本页内存里已有 access 令牌
// - signed-out：没有令牌
// - unavailable：开机 refresh 遇到 429、5xx 或断网——不当成未登录、不删登录标志，界面提示重试
//
// 开机：有登录标志（hasSessionHint）→ 调一次 refresh（单飞在 api 里）；无标志 → 直接 signed-out，不调 refresh（匿名不耗限速）。
// refresh 被拒（4xx，非 429）→ 删标志、signed-out。其它标签页登录而本页无令牌 → 再 refresh 一次；该路径被拒不删标志（标志是别的页刚写的）。
// 429、5xx、断网不当成未登录：会话多半还在，删了标志或跳到登录页，等服务恢复后用户反而要重新登录。
// ---------------------------------------------------------------------------
import type { TokenStore } from '../core/token'

export type SessionState = 'unknown' | 'signed-in' | 'signed-out' | 'unavailable'

export interface SessionTracker {
  get(): SessionState
  subscribe(notify: () => void): () => void
  /** 「暂时连不上」时用户点重试：本页没有令牌且没有正在恢复时重新试一次（被拒同样删标志）。 */
  retry(): void
}

export function createSessionTracker(deps: {
  tokens: TokenStore
  /** 即 api.refreshSession */
  refresh: () => Promise<boolean>
  /** false：开机不恢复（门户带快捷/邮件登录令牌时避免与旧会话 refresh 赛跑），直接 signed-out。缺省 true */
  restore?: boolean
}): SessionTracker {
  type Phase = 'restoring' | 'unavailable' | 'idle'

  let phase: Phase = 'idle'
  const listeners = new Set<() => void>()

  const notifyAll = () => listeners.forEach((fn) => fn())

  const setPhase = (next: Phase) => {
    if (phase === next) return
    phase = next
    notifyAll()
  }

  const get = (): SessionState => {
    if (deps.tokens.get() !== null) return 'signed-in'
    if (phase === 'restoring') return 'unknown'
    if (phase === 'unavailable') return 'unavailable'
    return 'signed-out'
  }

  const restore = async (dropHintOnReject: boolean) => {
    if (phase === 'restoring') return
    setPhase('restoring')
    try {
      const ok = await deps.refresh()
      if (ok) {
        setPhase('idle')
        return
      }
      if (dropHintOnReject && deps.tokens.get() === null) deps.tokens.clear()
      setPhase('idle')
    } catch {
      setPhase(deps.tokens.get() !== null ? 'idle' : 'unavailable')
    }
  }

  deps.tokens.subscribe(notifyAll)
  deps.tokens.onPeerSignIn(() => {
    if (deps.tokens.get() === null) void restore(false)
  })

  if (deps.restore !== false && deps.tokens.hasSessionHint()) {
    void restore(true)
  }

  return {
    get,
    subscribe(fn) {
      listeners.add(fn)
      return () => {
        listeners.delete(fn)
      }
    },
    retry() {
      if (deps.tokens.get() === null && phase !== 'restoring') void restore(true)
    },
  }
}
