/**
 * [INPUT]: 依赖 ./theme 的全部导出，依赖 vitest 的 vi.stubGlobal 伪造 document / window
 * [OUTPUT]: 对外提供主题状态的单元测试
 * [POS]: core/theme.ts 的测试，与 tests/theme-boot.test.ts 一起覆盖首帧前后两段主题逻辑
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { THEME_STORAGE_KEY, currentTheme, parseTheme, setTheme, subscribeTheme, toggleTheme } from './theme'

let attrs: Map<string, string>
let stored: Map<string, string>
let storageListener: ((event: StorageEvent) => void) | undefined

beforeEach(() => {
  attrs = new Map([['data-theme', 'light']])
  stored = new Map()
  storageListener = undefined
  vi.stubGlobal('document', {
    documentElement: {
      getAttribute: (k: string) => attrs.get(k) ?? null,
      hasAttribute: (k: string) => attrs.has(k),
      setAttribute: (k: string, v: string) => attrs.set(k, v),
    },
  })
  vi.stubGlobal('window', {
    localStorage: { setItem: (k: string, v: string) => stored.set(k, v) },
    addEventListener: (_: string, fn: (event: StorageEvent) => void) => (storageListener = fn),
    removeEventListener: () => (storageListener = undefined),
  })
})

afterEach(() => vi.unstubAllGlobals())

describe('theme', () => {
  it.each([
    ['dark', 'dark'],
    ['light', 'light'],
    [null, 'light'],
    ['system', 'light'],
  ] as const)('parseTheme(%j) is %s', (input, expected) => {
    expect(parseTheme(input)).toBe(expected)
  })

  it('setTheme writes the attribute and persists the choice', () => {
    setTheme('dark')
    expect(attrs.get('data-theme')).toBe('dark')
    expect(stored.get(THEME_STORAGE_KEY)).toBe('dark')
    expect(currentTheme()).toBe('dark')
  })

  it('toggleTheme flips between the two themes', () => {
    toggleTheme()
    expect(currentTheme()).toBe('dark')
    toggleTheme()
    expect(currentTheme()).toBe('light')
  })

  it('still switches when storage is unavailable', () => {
    vi.stubGlobal('window', {
      localStorage: {
        setItem: () => {
          throw new Error('QuotaExceededError')
        },
      },
    })
    setTheme('dark')
    expect(currentTheme()).toBe('dark')
  })

  it('notifies subscribers and follows other tabs', () => {
    const notify = vi.fn()
    const unsubscribe = subscribeTheme(notify)
    setTheme('dark')
    expect(notify).toHaveBeenCalledTimes(1)
    storageListener?.({ key: THEME_STORAGE_KEY, newValue: 'light' } as StorageEvent)
    expect(currentTheme()).toBe('light')
    expect(notify).toHaveBeenCalledTimes(2)
    storageListener?.({ key: 'unrelated', newValue: 'dark' } as StorageEvent)
    expect(notify).toHaveBeenCalledTimes(2)
    unsubscribe()
    expect(storageListener).toBeUndefined()
  })
})
