import { describe, expect, it, vi } from 'vitest'
import { APPEARANCE_CACHE_KEY, pickThemeTokens, portalBranding, readThemeCache, themeCacheValue, writeThemeCache } from './appearance'
import { quickLoginLink, quickLoginTokenFromHash, quickLoginTokenFromInput, readStoredInvite, takeInviteFromUrl, INVITE_STORAGE_KEY } from './entry-links'
import { greeting, MENU_PAGES, NAV_PAGES, navLabel, navOwner, resolvePage } from './pages'
import { displayName } from './queries'

describe('pages', () => {
  it('resolves known pages and sends everything else to 我的套餐', () => {
    expect(resolvePage('/orders')).toEqual({ page: 'orders', rest: [], canonical: '/orders' })
    expect(resolvePage('/').canonical).toBe('/subs')
    expect(resolvePage('/nope').canonical).toBe('/subs')
    expect(resolvePage('/nope/abc')).toEqual({ page: 'subs', rest: [], canonical: '/subs' })
    expect(resolvePage('/constructor').canonical).toBe('/subs')
    expect(resolvePage('/subs/abc/traffic')).toEqual({ page: 'subs', rest: ['abc', 'traffic'], canonical: '/subs/abc/traffic' })
  })

  it('hands the segments after the page to it as rest', () => {
    expect(resolvePage('/orders/ORD20260924-X1')).toEqual({ page: 'orders', rest: ['ORD20260924-X1'], canonical: '/orders/ORD20260924-X1' })
    expect(resolvePage('/tickets/abc/')).toEqual({ page: 'tickets', rest: ['abc'], canonical: '/tickets/abc' })
    expect(resolvePage('/help/%E5%85%A5%E9%97%A8')).toEqual({ page: 'help', rest: ['入门'], canonical: '/help/%E5%85%A5%E9%97%A8' })
    expect(resolvePage('/orders//x').canonical).toBe('/orders/x')
    expect(resolvePage('/orders/%E0%A4%A').canonical).toBe('/orders')
  })

  it('导航只有「我的套餐 / 选购 / 钱包」三项（用户 10-07 拍板），概览、订单等收进菜单；确认页不高亮', () => {
    expect(NAV_PAGES.map(navLabel)).toEqual(['我的套餐', '选购', '钱包'])
    expect(MENU_PAGES).toEqual(['overview', 'orders', 'referral', 'tickets', 'help', 'account'])
    expect(navOwner('checkout')).toBeNull()
    expect(navOwner('wallet')).toBe('wallet')
    expect(navLabel('orders')).toBe('订单')
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

describe('portalBranding', () => {
  const with_ = (branding: Record<string, string>) => ({ theme: { tokens: {}, branding }, slots: {} })
  it('keeps the design wordmark for the default site name and follows a switched theme otherwise', () => {
    expect(portalBranding(with_({ site_name: 'Pandora' }))).toEqual({ siteName: null, tagline: null, logo: null })
    expect(portalBranding(undefined)).toEqual({ siteName: null, tagline: null, logo: null })
    expect(portalBranding(with_({ site_name: ' 夜海加速 ', tagline: '一路畅通' }))).toEqual({ siteName: '夜海加速', tagline: '一路畅通', logo: null })
    // 默认站点名但带了 Logo：照样画 Logo + 站点名
    expect(portalBranding(with_({ site_name: 'Pandora', logo: 'data:image/png;base64,AAAA' })).siteName).toBe('Pandora')
    // 外链 Logo 不认（CSP 只放 data: 图片）
    expect(portalBranding(with_({ site_name: 'X', logo: 'https://evil.test/a.png' })).logo).toBeNull()
  })
})

describe('displayName', () => {
  it('prefers display_name, then the email local part', () => {
    expect(displayName({ email: 'zhang.wei@qq.com', display_name: '张伟' })).toBe('张伟')
    expect(displayName({ email: 'zhang.wei@qq.com', display_name: null })).toBe('zhang.wei')
    expect(displayName(undefined)).toBe('')
  })
})

describe('theme cache', () => {
  function memoryStorage() {
    const data = new Map<string, string>()
    return {
      data,
      getItem: (k: string) => data.get(k) ?? null,
      setItem: (k: string, v: string) => void data.set(k, v),
      removeItem: (k: string) => void data.delete(k),
    }
  }
  const custom = {
    theme: { tokens: { light: { '--brand': '#b9442b', 'not-a-token': 'red' }, dark: { '--brand': '#e46e52' } }, branding: { logo: 'data:image/png;base64,AAAA' } },
    slots: {},
  }

  it('stores only whitelisted colours of both themes, never the logo', () => {
    const storage = memoryStorage()
    writeThemeCache(custom, storage)
    const raw = storage.data.get(APPEARANCE_CACHE_KEY)!
    expect(JSON.parse(raw)).toEqual({ v: 1, light: { '--brand': '#b9442b' }, dark: { '--brand': '#e46e52' } })
    expect(raw).not.toContain('data:image')
    expect(readThemeCache('dark', storage)).toEqual([['--brand', '#e46e52']])
  })

  it('clears the cache when the default theme is back in effect', () => {
    const storage = memoryStorage()
    writeThemeCache(custom, storage)
    writeThemeCache({ theme: null, slots: {} }, storage)
    expect(storage.data.has(APPEARANCE_CACHE_KEY)).toBe(false)
    expect(themeCacheValue({ theme: null, slots: {} })).toBeNull()
  })

  it('reads nothing from a missing, broken or tampered cache', () => {
    const storage = memoryStorage()
    expect(readThemeCache('light', storage)).toEqual([])
    storage.setItem(APPEARANCE_CACHE_KEY, '{')
    expect(readThemeCache('light', storage)).toEqual([])
    storage.setItem(APPEARANCE_CACHE_KEY, JSON.stringify({ v: 1, light: { color: 'red', '--brand': 3, '--bg': '#fff' } }))
    expect(readThemeCache('light', storage)).toEqual([['--bg', '#fff']])
    expect(readThemeCache('light', null)).toEqual([])
  })
})
