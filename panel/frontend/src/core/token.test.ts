import { describe, expect, it, vi } from 'vitest'
import { authChannelName, createTokenStore, sessionHintKey } from './token'

function memoryStorage() {
  const data = new Map<string, string>()
  return {
    data,
    getItem: (k: string) => data.get(k) ?? null,
    setItem: (k: string, v: string) => void data.set(k, v),
    removeItem: (k: string) => void data.delete(k),
  }
}

function eventHub() {
  const listeners = new Set<(e: Event) => void>()
  return {
    listeners,
    addEventListener: (_: 'storage', fn: (e: Event) => void) => void listeners.add(fn),
    removeEventListener: (_: 'storage', fn: (e: Event) => void) => void listeners.delete(fn),
    fire: (key: string | null) => listeners.forEach((fn) => fn({ key } as unknown as Event)),
  }
}

function channelHub() {
  const sent: unknown[] = []
  const endpoints = new Set<{
    listeners: Set<(e: MessageEvent) => void>
    channel: {
      postMessage: (message: unknown) => void
      addEventListener: (type: 'message', listener: (e: MessageEvent) => void) => void
    }
  }>()

  return {
    sent,
    end() {
      const listeners = new Set<(e: MessageEvent) => void>()
      const self = {
        listeners,
        channel: {
          postMessage: (message: unknown) => {
            sent.push(message)
            for (const ep of endpoints) {
              if (ep !== self) {
                ep.listeners.forEach((fn) => fn({ data: message } as MessageEvent))
              }
            }
          },
          addEventListener: (_type: 'message', listener: (e: MessageEvent) => void) => {
            listeners.add(listener)
          },
        },
      }
      endpoints.add(self)
      return self.channel
    },
  }
}

describe('createTokenStore', () => {
  it('never writes the token to storage', () => {
    const storage = memoryStorage()
    const store = createTokenStore('portal', { storage, channel: null, events: null })
    store.set('secret-token')
    for (const value of storage.data.values()) {
      expect(value).not.toContain('secret-token')
    }
    expect(storage.data.get(sessionHintKey('portal'))).toBe('1')
    store.clear()
    expect(storage.data.has(sessionHintKey('portal'))).toBe(false)
    expect(store.get()).toBeNull()
  })

  it('keeps admin and portal hints apart on the shared origin', () => {
    const storage = memoryStorage()
    const admin = createTokenStore('admin', { storage, channel: null, events: null })
    const portal = createTokenStore('portal', { storage, channel: null, events: null })
    admin.set('a')
    expect(storage.data.get(sessionHintKey('admin'))).toBe('1')
    expect(storage.data.get(sessionHintKey('portal'))).toBeUndefined()
    portal.set('p')
    expect(storage.data.get(sessionHintKey('portal'))).toBe('1')
    admin.clear()
    expect(storage.data.has(sessionHintKey('admin'))).toBe(false)
    expect(storage.data.get(sessionHintKey('portal'))).toBe('1')
    expect(sessionHintKey('admin')).not.toBe(sessionHintKey('portal'))
    expect(authChannelName('admin')).not.toBe(authChannelName('portal'))
  })

  it('hasSessionHint reads the stored hint (a reload of a signed-in tab)', () => {
    const storage = memoryStorage()
    storage.setItem(sessionHintKey('admin'), '1')
    const store = createTokenStore('admin', { storage, channel: null, events: null })
    expect(store.hasSessionHint()).toBe(true)
    expect(store.get()).toBeNull()
  })

  it('falls back to memory when storage throws', () => {
    const broken = {
      getItem: () => {
        throw new Error('denied')
      },
      setItem: () => {
        throw new Error('denied')
      },
      removeItem: () => {
        throw new Error('denied')
      },
    }
    const store = createTokenStore('admin', { storage: broken, channel: null, events: null })
    store.set('t')
    expect(store.get()).toBe('t')
    expect(store.hasSessionHint()).toBe(true)
    store.clear()
    expect(store.get()).toBeNull()
    expect(store.hasSessionHint()).toBe(false)
  })

  it('broadcasts signed-in / signed-out only on transitions and never the token', () => {
    const hub = channelHub()
    const a = createTokenStore('portal', { storage: memoryStorage(), channel: hub.end(), events: null })
    // 另一个标签页：只当收件端，不用它的返回值
    createTokenStore('portal', { storage: memoryStorage(), channel: hub.end(), events: null })

    a.set('t1')
    expect(hub.sent).toEqual([{ type: 'signed-in' }])

    a.set('t2')
    expect(hub.sent).toEqual([{ type: 'signed-in' }])

    a.clear()
    expect(hub.sent).toEqual([{ type: 'signed-in' }, { type: 'signed-out' }])

    a.clear()
    expect(hub.sent).toEqual([{ type: 'signed-in' }, { type: 'signed-out' }])

    const serialized = JSON.stringify(hub.sent)
    expect(serialized).not.toContain('t1')
    expect(serialized).not.toContain('t2')
  })

  it('a peer signing out clears this tab without echoing', () => {
    const hub = channelHub()
    const storeA = createTokenStore('portal', { storage: memoryStorage(), channel: hub.end(), events: null })
    const storeB = createTokenStore('portal', { storage: memoryStorage(), channel: hub.end(), events: null })
    storeA.set('token-a')
    storeB.set('token-b')
    hub.sent.length = 0

    const notifyB = vi.fn()
    storeB.subscribe(notifyB)

    storeA.clear()
    expect(storeB.get()).toBeNull()
    expect(notifyB).toHaveBeenCalledTimes(1)
    expect(hub.sent).toEqual([{ type: 'signed-out' }])
  })

  it('a peer signing in wakes only a signed-out tab', () => {
    const hub = channelHub()
    const storeA = createTokenStore('portal', { storage: memoryStorage(), channel: hub.end(), events: null })
    const storeB = createTokenStore('portal', { storage: memoryStorage(), channel: hub.end(), events: null })
    storeB.set('token-b')

    const onSignInA = vi.fn()
    const onSignInB = vi.fn()
    storeA.onPeerSignIn(onSignInA)
    storeB.onPeerSignIn(onSignInB)

    const storeC = createTokenStore('portal', { storage: memoryStorage(), channel: hub.end(), events: null })
    storeC.set('token-c')

    expect(onSignInA).toHaveBeenCalledTimes(1)
    expect(onSignInB).not.toHaveBeenCalled()
  })

  it('ignores foreign messages', () => {
    const hub = channelHub()
    const peer = hub.end()
    const store = createTokenStore('portal', { storage: memoryStorage(), channel: hub.end(), events: null })
    store.set('my-token')

    const notify = vi.fn()
    const onSignIn = vi.fn()
    store.subscribe(notify)
    store.onPeerSignIn(onSignIn)

    peer.postMessage('hello')
    peer.postMessage({ type: 'token', token: 'x' })
    peer.postMessage(null)
    peer.postMessage({ type: 'unknown' })

    expect(notify).not.toHaveBeenCalled()
    expect(onSignIn).not.toHaveBeenCalled()
    expect(store.get()).toBe('my-token')
  })

  it('falls back to storage events without BroadcastChannel', () => {
    const storage = memoryStorage()
    const hub = eventHub()
    const store = createTokenStore('portal', { storage, channel: null, events: hub })
    store.set('tok-b')

    const notify = vi.fn()
    const onSignIn = vi.fn()
    store.subscribe(notify)
    store.onPeerSignIn(onSignIn)

    storage.removeItem(sessionHintKey('portal'))
    hub.fire(sessionHintKey('portal'))
    expect(store.get()).toBeNull()
    expect(notify).toHaveBeenCalledTimes(1)
    expect(onSignIn).not.toHaveBeenCalled()

    storage.setItem(sessionHintKey('portal'), '1')
    hub.fire(sessionHintKey('portal'))
    expect(onSignIn).toHaveBeenCalledTimes(1)

    hub.fire('pandora-theme')
    expect(notify).toHaveBeenCalledTimes(1)
    expect(onSignIn).toHaveBeenCalledTimes(1)
  })

  it('unsubscribe stops notifications', () => {
    const hub = channelHub()
    const store = createTokenStore('portal', { storage: memoryStorage(), channel: hub.end(), events: null })
    const notify = vi.fn()
    const stopSubscribe = store.subscribe(notify)

    store.set('t1')
    expect(notify).toHaveBeenCalledTimes(1)
    stopSubscribe()
    store.set('t2')
    expect(notify).toHaveBeenCalledTimes(1)

    const peer = createTokenStore('portal', { storage: memoryStorage(), channel: hub.end(), events: null })
    const onSignIn = vi.fn()
    const stopPeerSignIn = store.onPeerSignIn(onSignIn)

    store.clear()
    peer.set('p1')
    expect(onSignIn).toHaveBeenCalledTimes(1)

    stopPeerSignIn()
    peer.clear()
    peer.set('p2')
    expect(onSignIn).toHaveBeenCalledTimes(1)
  })
})
