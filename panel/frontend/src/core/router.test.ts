/**
 * [INPUT]: 依赖 vitest，依赖 ./router 的 parseHash / href / matchPath / navigate / subscribeHash
 * [OUTPUT]: 对外提供 router.ts 的单元测试
 * [POS]: core/router 的单元测试：hash 解析与拼接互逆、模式匹配与解码、navigate 的 push/replace 两条路径；伪造 window.location
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { href, matchPath, navigate, parseHash, subscribeHash } from './router'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('parseHash / href', () => {
  it('normalizes empty and trailing-slash paths', () => {
    expect(parseHash('').path).toBe('/')
    expect(parseHash('#').path).toBe('/')
    expect(parseHash('#/').path).toBe('/')
    expect(parseHash('#/users/').path).toBe('/users')
    expect(parseHash('#orders').path).toBe('/orders')
  })

  it('splits the query after the path', () => {
    const loc = parseHash('#/orders?status=paid&page=2')
    expect(loc.path).toBe('/orders')
    expect(loc.query.get('status')).toBe('paid')
    expect(loc.query.get('page')).toBe('2')
  })

  it('href drops empty values and round-trips through parseHash', () => {
    const link = href('/users/abc', { tab: 'orders', q: '', page: 3, group: undefined })
    expect(link).toBe('#/users/abc?tab=orders&page=3')
    const back = parseHash(link)
    expect(back.path).toBe('/users/abc')
    expect(Object.fromEntries(back.query)).toEqual({ tab: 'orders', page: '3' })
    expect(href('/quick-login/tok')).toBe('#/quick-login/tok')
  })
})

describe('matchPath', () => {
  it('extracts and decodes params', () => {
    expect(matchPath('/users/:id', '/users/abc')).toEqual({ id: 'abc' })
    expect(matchPath('/quick-login/:token', '/quick-login/a%2Fb')).toEqual({ token: 'a/b' })
    expect(matchPath('/', '/')).toEqual({})
  })

  it('rejects different shapes, empty params and malformed escapes', () => {
    expect(matchPath('/users/:id', '/users')).toBeNull()
    expect(matchPath('/users/:id', '/users/a/b')).toBeNull()
    expect(matchPath('/users/:id', '/plans/a')).toBeNull()
    expect(matchPath('/users/:id', '/users/%E0%A4%A')).toBeNull()
  })
})

describe('navigate', () => {
  function stubLocation() {
    const location = { hash: '', replace: vi.fn() }
    const listeners = new Set<() => void>()
    vi.stubGlobal('window', {
      location,
      addEventListener: (_: string, fn: () => void) => listeners.add(fn),
      removeEventListener: (_: string, fn: () => void) => listeners.delete(fn),
    })
    return { location, listeners }
  }

  it('pushes by assigning the hash and replaces without a history entry', () => {
    const { location } = stubLocation()
    navigate('/orders', { query: { status: 'paid' } })
    expect(location.hash).toBe('#/orders?status=paid')
    navigate('/login', { replace: true })
    expect(location.replace).toHaveBeenCalledWith('#/login')
  })

  it('subscribeHash registers and removes a hashchange listener', () => {
    const { listeners } = stubLocation()
    const stop = subscribeHash(() => {})
    expect(listeners.size).toBe(1)
    stop()
    expect(listeners.size).toBe(0)
  })
})
