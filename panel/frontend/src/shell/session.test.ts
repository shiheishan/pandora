import { describe, expect, it, vi } from 'vitest'
import type { TokenStore } from '../core/token'
import { createSessionTracker, type SessionState } from './session'

function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function fakeTokens(options: { hint?: boolean; token?: string | null } = {}) {
  let token: string | null = options.token ?? null
  let hint = options.hint ?? false
  let clearCalls = 0
  const subscribers = new Set<() => void>()
  const peerListeners = new Set<() => void>()

  const store: TokenStore & { peerSignIn: () => void; clearCalls: number; hint: boolean } = {
    get: () => token,
    set: (t) => {
      token = t
      hint = true
      subscribers.forEach((fn) => fn())
    },
    clear: () => {
      clearCalls++
      const hadToken = token !== null
      token = null
      hint = false
      if (hadToken) subscribers.forEach((fn) => fn())
    },
    hasSessionHint: () => hint,
    subscribe(fn) {
      subscribers.add(fn)
      return () => subscribers.delete(fn)
    },
    onPeerSignIn(fn) {
      peerListeners.add(fn)
      return () => peerListeners.delete(fn)
    },
    peerSignIn() {
      peerListeners.forEach((fn) => fn())
    },
    get clearCalls() {
      return clearCalls
    },
    get hint() {
      return hint
    },
  }
  return store
}

async function flushMicrotasks() {
  await Promise.resolve()
}

async function waitForState(get: () => SessionState, want: SessionState) {
  for (let i = 0; i < 50; i++) {
    await flushMicrotasks()
    if (get() === want) return
  }
  expect(get()).toBe(want)
}

describe('createSessionTracker', () => {
  it('without a hint it is signed-out at once and never refreshes', async () => {
    const tokens = fakeTokens({ hint: false })
    const refresh = vi.fn(() => Promise.resolve(true))
    const session = createSessionTracker({ tokens, refresh })
    expect(session.get()).toBe('signed-out')
    await flushMicrotasks()
    expect(refresh).not.toHaveBeenCalled()
  })

  it('with a hint it starts unknown and becomes signed-in when refresh succeeds', async () => {
    const tokens = fakeTokens({ hint: true })
    const d = deferred<boolean>()
    const refresh = vi.fn(async () => {
      const ok = await d.promise
      if (ok) tokens.set('t')
      return ok
    })
    const session = createSessionTracker({ tokens, refresh })
    expect(session.get()).toBe('unknown')
    expect(refresh).toHaveBeenCalledTimes(1)
    d.resolve(true)
    await waitForState(() => session.get(), 'signed-in')
  })

  it('a rejected refresh drops the hint and signs out', async () => {
    const tokens = fakeTokens({ hint: true })
    const refresh = vi.fn(() => Promise.resolve(false))
    const session = createSessionTracker({ tokens, refresh })
    await waitForState(() => session.get(), 'signed-out')
    expect(tokens.hint).toBe(false)
    expect(tokens.clearCalls).toBe(1)
  })

  it('429 / 5xx / offline make it unavailable and keep the hint', async () => {
    const tokens = fakeTokens({ hint: true })
    const refresh = vi.fn(() => Promise.reject(new Error('offline')))
    const session = createSessionTracker({ tokens, refresh })
    await waitForState(() => session.get(), 'unavailable')
    expect(tokens.hint).toBe(true)
    expect(tokens.clearCalls).toBe(0)
  })

  it('retry from unavailable goes back to unknown and can succeed', async () => {
    const tokens = fakeTokens({ hint: true })
    const first = deferred<boolean>()
    const refresh = vi
      .fn()
      .mockImplementationOnce(() => first.promise)
      .mockImplementationOnce(() => {
        tokens.set('t')
        return Promise.resolve(true)
      })
    const session = createSessionTracker({ tokens, refresh })
    first.reject(new Error('503'))
    await waitForState(() => session.get(), 'unavailable')
    session.retry()
    expect(session.get()).toBe('unknown')
    await waitForState(() => session.get(), 'signed-in')
    expect(refresh).toHaveBeenCalledTimes(2)
  })

  it('restore: false skips the boot refresh even with a hint', async () => {
    const tokens = fakeTokens({ hint: true })
    const refresh = vi.fn(() => Promise.resolve(true))
    const session = createSessionTracker({ tokens, refresh, restore: false })
    expect(session.get()).toBe('signed-out')
    await flushMicrotasks()
    expect(refresh).not.toHaveBeenCalled()
    expect(tokens.hint).toBe(true)
  })

  it('a peer sign-in refreshes a signed-out tab; a rejection there keeps the hint', async () => {
    const tokens = fakeTokens({ hint: true })
    const refresh = vi.fn(() => Promise.resolve(false))
    const session = createSessionTracker({ tokens, refresh, restore: false })
    expect(refresh).not.toHaveBeenCalled()
    tokens.peerSignIn()
    expect(refresh).toHaveBeenCalledTimes(1)
    expect(session.get()).toBe('unknown')
    await waitForState(() => session.get(), 'signed-out')
    expect(tokens.hint).toBe(true)
    expect(tokens.clearCalls).toBe(0)
  })

  it('a peer sign-in is ignored while this tab is signed in', async () => {
    const tokens = fakeTokens({ hint: true, token: 'already' })
    const refresh = vi.fn(() => Promise.resolve(true))
    const session = createSessionTracker({ tokens, refresh, restore: false })
    expect(session.get()).toBe('signed-in')
    tokens.peerSignIn()
    await flushMicrotasks()
    expect(refresh).not.toHaveBeenCalled()
  })

  it('subscribers hear phase changes and token changes', async () => {
    const tokens = fakeTokens({ hint: true })
    const refresh = vi.fn(() => Promise.resolve(false))
    const session = createSessionTracker({ tokens, refresh })
    const notify = vi.fn()
    session.subscribe(notify)
    await waitForState(() => session.get(), 'signed-out')
    expect(notify.mock.calls.length).toBeGreaterThan(0)
    tokens.set('x')
    expect(session.get()).toBe('signed-in')
    expect(notify.mock.calls.length).toBeGreaterThan(1)
  })
})
