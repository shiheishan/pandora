/**
 * [INPUT]: 依赖 vitest，依赖 ./pages、./entry-links、./appearance 的 pickThemeTokens、./queries 的 displayName
 * [OUTPUT]: 对外提供 portal 外框纯逻辑的单元测试
 * [POS]: portal 的单元测试：页面路由、rest 子路由与导航归属、邀请链接取码并抹掉查询串、快捷登录令牌识别、主题令牌白名单、用户名映射；界面交互在浏览器里对 dev/mock-api 验收
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it, vi } from 'vitest'
import { pickThemeTokens } from './appearance'
import { quickLoginLink, quickLoginTokenFromHash, quickLoginTokenFromInput, readStoredInvite, takeInviteFromUrl, INVITE_STORAGE_KEY } from './entry-links'
import { greeting, navLabel, navOwner, resolvePage } from './pages'
import { displayName } from './queries'

describe('pages', () => {
  it('resolves known pages and sends everything else to the overview', () => {
    expect(resolvePage('/orders')).toEqual({ page: 'orders', rest: [], canonical: '/orders' })
    expect(resolvePage('/').canonical).toBe('/overview')
    expect(resolvePage('/nope').canonical).toBe('/overview')
    expect(resolvePage('/nope/abc')).toEqual({ page: 'overview', rest: [], canonical: '/overview' })
    expect(resolvePage('/constructor').canonical).toBe('/overview')
  })

  it('hands the segments after the page to it as rest', () => {
    expect(resolvePage('/orders/ORD20260924-X1')).toEqual({ page: 'orders', rest: ['ORD20260924-X1'], canonical: '/orders/ORD20260924-X1' })
    expect(resolvePage('/tickets/abc/')).toEqual({ page: 'tickets', rest: ['abc'], canonical: '/tickets/abc' })
    expect(resolvePage('/help/%E5%85%A5%E9%97%A8')).toEqual({ page: 'help', rest: ['入门'], canonical: '/help/%E5%85%A5%E9%97%A8' })
    expect(resolvePage('/orders//x').canonical).toBe('/orders/x')
    expect(resolvePage('/orders/%E0%A4%A').canonical).toBe('/orders')
  })

  it('checkout belongs to the plans tab; the orders tab is labelled 订单', () => {
    expect(navOwner('checkout')).toBe('plans')
    expect(navOwner('wallet')).toBe('wallet')
    expect(navLabel('orders')).toBe('订单')
    expect(navLabel('subs')).toBe('我的订阅')
  })

  it('greets by the hour like the design', () => {
    expect(greeting(new Date('2026-09-24T03:00:00'))).toBe('夜深了')
    expect(greeting(new Date('2026-09-24T09:00:00'))).toBe('早上好')
    expect(greeting(new Date('2026-09-24T12:00:00'))).toBe('中午好')
    expect(greeting(new Date('2026-09-24T15:00:00'))).toBe('下午好')
    expect(greeting(new Date('2026-09-24T21:00:00'))).toBe('晚上好')
  })
})

describe('invite links', () => {
  function env(search: string, hash = '') {
    const data = new Map<string, string>()
    return {
      location: { search, pathname: '/', hash },
      history: { replaceState: vi.fn() },
      storage: { getItem: (k: string) => data.get(k) ?? null, setItem: (k: string, v: string) => void data.set(k, v) },
      data,
    }
  }

  it('upper-cases and stores the code, then strips only the invite param', () => {
    const e = env('?invite=k7q2&utm=x', '#/overview')
    expect(takeInviteFromUrl(e)).toBe('K7Q2')
    expect(e.data.get(INVITE_STORAGE_KEY)).toBe('K7Q2')
    expect(e.history.replaceState).toHaveBeenCalledWith(null, '', '/?utm=x#/overview')
    expect(readStoredInvite(e)).toBe('K7Q2')
  })

  it('does nothing without an invite', () => {
    const e = env('?utm=x')
    expect(takeInviteFromUrl(e)).toBeNull()
    expect(e.history.replaceState).not.toHaveBeenCalled()
  })
})

describe('quick-login tokens', () => {
  const token = 'aB3_-xYz09aB3_-xYz09aB3_-xYz09aB3_-xYz09aB3'
  it('reads the token from the hash link', () => {
    expect(quickLoginTokenFromHash(`#/quick-login/${token}`)).toBe(token)
    expect(quickLoginTokenFromHash('#/overview')).toBeNull()
  })

  it('accepts a pasted link or a bare token, rejects junk', () => {
    expect(quickLoginTokenFromInput(`https://portal.example/#/quick-login/${token}`)).toBe(token)
    expect(quickLoginTokenFromInput(`  ${token} `)).toBe(token)
    expect(quickLoginTokenFromInput('https://portal.example/q/abc')).toBeNull()
    expect(quickLoginTokenFromInput('not a token!')).toBeNull()
    expect(quickLoginTokenFromInput('')).toBeNull()
  })

  it('the link the account page builds is the one the login page reads back (hash form, relative to the entry page)', () => {
    const link = quickLoginLink(token, 'https://portal.example/#/account?x=1')
    expect(link).toBe(`https://portal.example/#/quick-login/${token}`)
    expect(quickLoginTokenFromInput(link)).toBe(token)
    expect(quickLoginLink(token, 'https://example.com/portal/index.html#/account')).toBe(`https://example.com/portal/#/quick-login/${token}`)
  })
})

describe('pickThemeTokens', () => {
  it('takes the group for the current theme and drops keys outside the design whitelist', () => {
    const appearance = {
      theme: { tokens: { light: { '--brand': '#b9442b', '--evil': 'red', '--bg': ' ' }, dark: { '--brand': '#e46e52' } }, branding: {} },
      slots: {},
    }
    expect(pickThemeTokens(appearance, 'light')).toEqual([['--brand', '#b9442b']])
    expect(pickThemeTokens(appearance, 'dark')).toEqual([['--brand', '#e46e52']])
    expect(pickThemeTokens({ theme: null, slots: {} }, 'light')).toEqual([])
    expect(pickThemeTokens(undefined, 'dark')).toEqual([])
  })
})

describe('displayName', () => {
  it('prefers display_name, then the email local part', () => {
    expect(displayName({ email: 'zhang.wei@qq.com', display_name: '张伟' })).toBe('张伟')
    expect(displayName({ email: 'zhang.wei@qq.com', display_name: null })).toBe('zhang.wei')
    expect(displayName(undefined)).toBe('')
  })
})
