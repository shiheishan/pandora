import { describe, expect, it, vi } from 'vitest'
import { parseHash } from '../core/router'
import { APPEARANCE_CACHE_KEY, pickThemeTokens, portalBranding, readThemeCache, themeCacheValue, writeThemeCache } from './appearance'
import {
  authTabFromPath,
  isHomePath,
  LOGIN_PATH,
  mailLoginTokenFromHash,
  quickLoginLink,
  quickLoginTokenFromHash,
  quickLoginTokenFromInput,
  readStoredInvite,
  REGISTER_PATH,
  takeInviteFromUrl,
  INVITE_STORAGE_KEY,
} from './entry-links'
import { hasServerHome, setHomeHidden, HOME_ATTR } from './home'
import { HIDDEN_TITLE_PAGES, PAGES, SIDE_GROUPS, TAB_PAGES, WIDE_PAGES, resolvePage, sideOwner, tabLabel, tabOwner } from './pages'
import { displayName } from './queries'

describe('pages', () => {
  it('resolves known pages and sends everything else to 我的套餐', () => {
    expect(resolvePage('/orders')).toEqual({ page: 'orders', rest: [], canonical: '/orders' })
    expect(resolvePage('/').canonical).toBe('/subs')
    expect(resolvePage('/overview').canonical).toBe('/subs')
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

  it('桌面没有「我的」页，落回我的套餐', () => {
    expect(resolvePage('/me')).toEqual({ page: 'me', rest: [], canonical: '/me' })
    expect(resolvePage('/me', { desktop: true })).toEqual({ page: 'subs', rest: [], canonical: '/subs' })
    expect(resolvePage('/orders', { desktop: true }).page).toBe('orders')
  })

  it('侧栏三组、底部标签栏四项（样稿 v5）', () => {
    expect(SIDE_GROUPS.map((g) => g.map((p) => PAGES[p]))).toEqual([
      ['我的套餐', '选购', '钱包'],
      ['订单', '邀请返利'],
      ['消息', '工单支持', '帮助中心', '账号安全'],
    ])
    expect(TAB_PAGES.map(tabLabel)).toEqual(['套餐', '选购', '钱包', '我的'])
  })

  it('高亮：确认页都不亮，手机上「我的」收下其余页面', () => {
    expect(sideOwner('checkout')).toBeNull()
    expect(sideOwner('me')).toBeNull()
    expect(sideOwner('orders')).toBe('orders')
    expect(tabOwner('checkout')).toBeNull()
    expect(tabOwner('wallet')).toBe('wallet')
    expect(tabOwner('orders')).toBe('me')
    expect(tabOwner('account')).toBe('me')
  })

  it('列宽与隐藏标题', () => {
    expect(WIDE_PAGES.has('plans')).toBe(true)
    expect(WIDE_PAGES.has('checkout')).toBe(true)
    expect(WIDE_PAGES.has('subs')).toBe(false)
    expect(HIDDEN_TITLE_PAGES.has('me')).toBe(true)
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

describe('mail-login tokens', () => {
  it('reads the token from the hash link', () => {
    expect(mailLoginTokenFromHash('#/mail-login/abc_DEF-1')).toBe('abc_DEF-1')
    expect(mailLoginTokenFromHash('#/quick-login/x')).toBeNull()
    expect(mailLoginTokenFromHash('#/subs')).toBeNull()
    expect(mailLoginTokenFromHash('')).toBeNull()
  })
})

describe('home entry', () => {
  it('maps hash paths to auth tabs and home path', () => {
    expect(authTabFromPath(parseHash('#/login').path)).toBe('login')
    expect(authTabFromPath(parseHash('#/register').path)).toBe('reg')
    expect(authTabFromPath(parseHash('#/register/').path)).toBe('reg')
    expect(authTabFromPath(parseHash('#/subs').path)).toBeNull()
    expect(authTabFromPath(parseHash('#/').path)).toBeNull()
    expect(authTabFromPath(parseHash('').path)).toBeNull()

    expect(isHomePath(parseHash('').path)).toBe(true)
    expect(isHomePath(parseHash('#').path)).toBe(true)
    expect(isHomePath(parseHash('#/').path)).toBe(true)
    expect(isHomePath(parseHash('#/login').path)).toBe(false)
    expect(isHomePath(parseHash('#/subs').path)).toBe(false)

    expect(LOGIN_PATH).toBe('/login')
    expect(REGISTER_PATH).toBe('/register')
  })

  it('detects server home fragment and toggles data-home', () => {
    expect(hasServerHome({ getElementById: (id: string) => (id === 'ph-home' ? ({} as HTMLElement) : null) })).toBe(true)
    expect(hasServerHome({ getElementById: () => null })).toBe(false)

    const root = {
      setAttribute: vi.fn(),
      removeAttribute: vi.fn(),
    }
    setHomeHidden(true, root)
    expect(root.setAttribute).toHaveBeenCalledWith(HOME_ATTR, 'off')
    setHomeHidden(false, root)
    expect(root.removeAttribute).toHaveBeenCalledWith(HOME_ATTR)
  })
})

describe('pickThemeTokens', () => {
  it('takes the group for the current theme and drops keys outside the design whitelist', () => {
    const appearance = {
      site_name: 'Pandora',
      theme: { tokens: { light: { '--brand': '#b9442b', '--evil': 'red', '--bg': ' ' }, dark: { '--brand': '#e46e52' } }, branding: {} },
      slots: {},
    }
    expect(pickThemeTokens(appearance, 'light')).toEqual([['--brand', '#b9442b']])
    expect(pickThemeTokens(appearance, 'dark')).toEqual([['--brand', '#e46e52']])
    expect(pickThemeTokens({ site_name: 'Pandora', theme: null, slots: {} }, 'light')).toEqual([])
    expect(pickThemeTokens(undefined, 'dark')).toEqual([])
  })
})

describe('portalBranding', () => {
  const with_ = (site_name: string, branding: Record<string, string> = {}) => ({ site_name, theme: { tokens: {}, branding }, slots: {} })
  it('keeps the design wordmark for the default site name and follows a switched theme otherwise', () => {
    expect(portalBranding(with_('Pandora'))).toEqual({ siteName: null, tagline: null, logo: null })
    expect(portalBranding(undefined)).toEqual({ siteName: null, tagline: null, logo: null })
    expect(portalBranding(with_(' 夜海加速 ', { tagline: '一路畅通' }))).toEqual({ siteName: '夜海加速', tagline: '一路畅通', logo: null })
    // 默认站点名但带了 Logo：照样画 Logo + 站点名
    expect(portalBranding(with_('Pandora', { logo: 'data:image/png;base64,AAAA' })).siteName).toBe('Pandora')
    // 外链 Logo 不认（CSP 只放 data: 图片）
    expect(portalBranding(with_('X', { logo: 'https://evil.test/a.png' })).logo).toBeNull()
    // 主题 branding 里残留的 site_name 不起作用
    expect(portalBranding({ site_name: 'Pandora', theme: { tokens: {}, branding: { site_name: '旧名' } as Record<string, string> }, slots: {} } as never).siteName).toBeNull()
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
    site_name: 'Pandora',
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
    writeThemeCache({ site_name: 'Pandora', theme: null, slots: {} }, storage)
    expect(storage.data.has(APPEARANCE_CACHE_KEY)).toBe(false)
    expect(themeCacheValue({ site_name: 'Pandora', theme: null, slots: {} })).toBeNull()
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
