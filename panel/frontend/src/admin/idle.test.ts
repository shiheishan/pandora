import { describe, expect, it } from 'vitest'
import { startIdleWatch } from './idle'

const HOUR = 60 * 60 * 1000

function harness(timeoutMs = 2 * HOUR) {
  let t = 1_000_000
  const store = new Map<string, string>()
  const listeners = new Map<string, () => void>()
  let tick: (() => void) | null = null
  let idle = 0
  const stop = startIdleWatch({
    timeoutMs,
    onIdle: () => idle++,
    now: () => t,
    storage: { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => void store.set(k, v) },
    target: {
      addEventListener: (type, fn) => void listeners.set(type, fn),
      removeEventListener: (type) => void listeners.delete(type),
    },
    every: (fn) => {
      tick = fn
      return () => (tick = null)
    },
  })
  return {
    advance: (ms: number) => (t += ms),
    act: (type = 'pointerdown') => listeners.get(type)?.(),
    tick: () => tick?.(),
    store,
    listeners,
    get idle() {
      return idle
    },
    get ticking() {
      return tick !== null
    },
    stop,
    now: () => t,
  }
}

describe('startIdleWatch', () => {
  it('signs out once after the timeout without activity, then stops', () => {
    const h = harness()
    h.advance(2 * HOUR - 1)
    h.tick()
    expect(h.idle).toBe(0)
    h.advance(1)
    h.tick()
    expect(h.idle).toBe(1)
    expect(h.ticking).toBe(false)
    expect(h.listeners.size).toBe(0)
  })

  it('user activity postpones the timeout', () => {
    const h = harness()
    h.advance(HOUR)
    h.act('keydown')
    h.advance(HOUR + 30 * 60 * 1000)
    h.tick()
    expect(h.idle).toBe(0)
    h.advance(HOUR)
    h.tick()
    expect(h.idle).toBe(1)
  })

  it('activity in another tab (shared timestamp) counts', () => {
    const h = harness()
    h.advance(90 * 60 * 1000)
    h.store.set('pandora-admin-activity', String(h.now()))
    h.advance(60 * 60 * 1000)
    h.tick()
    expect(h.idle).toBe(0)
  })

  it('first activity after the timeout (wake from sleep) signs out instead of extending', () => {
    const h = harness()
    h.advance(3 * HOUR)
    h.act()
    expect(h.idle).toBe(1)
  })

  it('shares activity with other tabs, throttled', () => {
    const h = harness()
    const first = h.store.get('pandora-admin-activity')
    h.advance(10_000)
    h.act()
    expect(h.store.get('pandora-admin-activity')).toBe(first)
    h.advance(60_000)
    h.act()
    expect(h.store.get('pandora-admin-activity')).toBe(String(h.now()))
  })

  it('stop removes listeners and the timer', () => {
    const h = harness()
    h.stop()
    expect(h.ticking).toBe(false)
    expect(h.listeners.size).toBe(0)
  })
})
