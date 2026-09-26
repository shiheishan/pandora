/**
 * [INPUT]: 依赖 vitest，依赖 ./token 的 createTokenStore / tokenStorageKey
 * [OUTPUT]: 对外提供 token.ts 的单元测试
 * [POS]: core/token 的单元测试：两个入口的键互不覆盖、存储不可用时退回内存、本页与跨标签页变化都通知订阅者
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it, vi } from 'vitest'
import { createTokenStore, tokenStorageKey } from './token'

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

describe('createTokenStore', () => {
  it('keeps admin and portal tokens apart on the shared origin', () => {
    const storage = memoryStorage()
    const admin = createTokenStore('admin', { storage, events: null })
    const portal = createTokenStore('portal', { storage, events: null })
    admin.set('a')
    portal.set('p')
    expect([admin.get(), portal.get()]).toEqual(['a', 'p'])
    expect(storage.data.get(tokenStorageKey('admin'))).toBe('a')
    admin.clear()
    expect([admin.get(), portal.get()]).toEqual([null, 'p'])
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
    const store = createTokenStore('admin', { storage: broken, events: null })
    store.set('t')
    expect(store.get()).toBe('t')
    store.clear()
    expect(store.get()).toBeNull()
  })

  it('notifies on local changes and on other tabs, and detaches when unsubscribed', () => {
    const hub = eventHub()
    const store = createTokenStore('portal', { storage: memoryStorage(), events: hub })
    const notify = vi.fn()
    const stop = store.subscribe(notify)
    store.set('t')
    hub.fire(tokenStorageKey('portal'))
    hub.fire(tokenStorageKey('admin'))
    hub.fire(null)
    expect(notify).toHaveBeenCalledTimes(3)
    stop()
    expect(hub.listeners.size).toBe(0)
  })
})
