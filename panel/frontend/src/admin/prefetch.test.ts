import { describe, expect, it, vi } from 'vitest'
import { MODULES, type ModuleKey } from './modules'
import { SCREEN_CHUNKS, prefetchScreenForHash } from './prefetch'
import { SCREENS } from './screens'

function fakeChunks(fail = false) {
  const calls: ModuleKey[] = []
  const chunks = Object.fromEntries(
    (Object.keys(MODULES) as ModuleKey[]).map((key) => [
      key,
      vi.fn(() => {
        calls.push(key)
        return fail ? Promise.reject(new Error('offline')) : Promise.resolve({})
      }),
    ]),
  ) as unknown as Record<ModuleKey, () => Promise<unknown>>
  return { chunks, calls }
}

describe('prefetchScreenForHash', () => {
  it('covers exactly the modules the page table lazy-loads', () => {
    expect(Object.keys(SCREEN_CHUNKS).sort()).toEqual(Object.keys(MODULES).sort())
    expect(Object.keys(SCREEN_CHUNKS).sort()).toEqual(Object.keys(SCREENS).sort())
  })

  it.each([
    ['#/users/list/0b6c-uuid', 'users'],
    ['#/nodes/nodes?q=sg', 'nodes'],
    ['#/billing', 'billing'],
    ['#/tickets', 'tickets'],
    ['', 'dash'],
    ['#/', 'dash'],
    ['#/no-such-module', 'dash'],
  ] as const)('loads the chunk for %j once', (hash, module) => {
    const { chunks, calls } = fakeChunks()
    expect(prefetchScreenForHash(hash, chunks)).toBe(module)
    expect(calls).toEqual([module])
  })

  it('swallows a failed download (the lazy page retries when it renders)', async () => {
    const { chunks } = fakeChunks(true)
    expect(() => prefetchScreenForHash('#/users', chunks)).not.toThrow()
    await Promise.resolve()
  })
})
