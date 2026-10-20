import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import type { ConfigEnv, UserConfig } from 'vite'
import { describe, expect, it } from 'vitest'
import { THEME_STORAGE_KEY } from '../src/core/theme'
import { tokenStorageKey } from '../src/core/token'
import { COLOR_TOKENS } from '../src/styles/design-tokens'
import viteConfig, { themeBootFileName } from '../vite.config'

const source = readFileSync(new URL('../src/core/theme-boot.js', import.meta.url), 'utf8')
// appearance.ts 依赖 React 与运行时，这个（node 侧的）测试不 import 它，按源码文本核对键名
const appearanceSource = readFileSync(new URL('../src/portal/appearance.ts', import.meta.url), 'utf8')
const APPEARANCE_CACHE_KEY = 'pandora-portal-appearance'

function run(
  getItem: (key: string) => string | null,
  app = 'portal',
  loc: { hash?: string; search?: string } = {},
) {
  const attrs: Record<string, string> = { 'data-app': app }
  const props: Record<string, string> = {}
  runInNewContext(source, {
    window: {
      localStorage: { getItem },
      location: { hash: loc.hash ?? '', search: loc.search ?? '' },
    },
    document: {
      documentElement: {
        setAttribute: (k: string, v: string) => (attrs[k] = v),
        getAttribute: (k: string) => attrs[k] ?? null,
        style: { setProperty: (k: string, v: string) => (props[k] = v) },
      },
    },
  })
  return { theme: attrs['data-theme'], props, home: attrs['data-home'] }
}

function boot(getItem: (key: string) => string | null): string | undefined {
  return run(getItem).theme
}

describe('theme-boot.js', () => {
  it('reads the same storage key as theme.ts', () => {
    expect(source).toContain(`'${THEME_STORAGE_KEY}'`)
  })

  it('applies the stored dark theme', () => {
    expect(boot((k) => (k === THEME_STORAGE_KEY ? 'dark' : null))).toBe('dark')
  })

  it.each([null, 'light', 'Dark', 'purple', ''])('falls back to light for %j', (stored) => {
    expect(boot(() => stored)).toBe('light')
  })

  it('falls back to light when storage throws', () => {
    expect(
      boot(() => {
        throw new Error('SecurityError')
      }),
    ).toBe('light')
  })
})

describe('theme-boot.js cached portal theme', () => {
  // 与 appearance.ts 的 themeCacheValue 同形：{ v: 1, light, dark }（那边的单测核对它写出的形状）
  const cache = JSON.stringify({ v: 1, light: { '--brand': '#b9442b', '--bg': '#fffaf0' }, dark: { '--brand': '#e46e52' } })
  const storage = (theme: string | null, appearance: string | null) => (key: string) =>
    key === THEME_STORAGE_KEY ? theme : key === APPEARANCE_CACHE_KEY ? appearance : null

  it('reads the same cache key appearance.ts writes', () => {
    expect(appearanceSource).toContain(`APPEARANCE_CACHE_KEY = '${APPEARANCE_CACHE_KEY}'`)
    expect(source).toContain(`'${APPEARANCE_CACHE_KEY}'`)
  })

  it('applies the cached colours of the current theme before the first frame', () => {
    const { props } = run(storage(null, cache))
    expect(props).toEqual({ '--brand': '#b9442b', '--bg': '#fffaf0' })
    const dark = run(storage('dark', cache))
    expect(dark).toEqual({ theme: 'dark', props: { '--brand': '#e46e52' }, home: undefined })
  })

  it('never paints the portal colours onto the admin console (same origin, same storage)', () => {
    expect(run(storage(null, cache), 'admin').props).toEqual({})
  })

  it('ignores broken, foreign-shaped or hostile cache entries', () => {
    for (const bad of ['{', 'null', '[]', '{"v":2,"light":{"--brand":"red"}}', '{"v":1,"light":"red"}']) {
      const { theme, props } = run(storage(null, bad))
      expect({ theme, props }).toEqual({ theme: 'light', props: {} })
    }
    const hostile = JSON.stringify({ v: 1, light: { color: 'red', '--Brand': 'red', '--ok': 'x'.repeat(201), '--fine': 'blue', '--n': 3 } })
    expect(run(storage(null, hostile)).props).toEqual({ '--fine': 'blue' })
  })

  it('accepts every themeable token name', () => {
    for (const { name } of COLOR_TOKENS) expect(name).toMatch(/^--[a-z0-9-]+$/)
  })
})

describe('theme-boot.js portal home', () => {
  const portalKey = tokenStorageKey('portal')
  const withToken = (token: string | null) => (key: string) => (key === portalKey ? token : null)

  it('reads the same portal token key as token.ts', () => {
    expect(source).toContain(`'${portalKey}'`)
  })

  it.each(['', '#', '#/'] as const)('portal without token and empty hash %j leaves home unset', (hash) => {
    expect(run(withToken(null), 'portal', { hash }).home).toBeUndefined()
  })

  it('portal with token sets data-home off', () => {
    expect(run(withToken('x'), 'portal').home).toBe('off')
  })

  it.each(['#/login', '#/register', '#/subs'] as const)('portal with hash %s sets data-home off', (hash) => {
    expect(run(withToken(null), 'portal', { hash }).home).toBe('off')
  })

  it('portal with invite query sets data-home off', () => {
    expect(run(withToken(null), 'portal', { search: '?invite=ABC' }).home).toBe('off')
  })

  it('admin entry does not set data-home even with token and hash', () => {
    expect(run(withToken('x'), 'admin', { hash: '#/login' }).home).toBeUndefined()
  })

  it('still hides home by the address when storage throws; theme stays light', () => {
    const throwing = () => {
      throw new Error('SecurityError')
    }
    expect(run(throwing, 'portal', { hash: '#/login' })).toMatchObject({ theme: 'light', home: 'off' })
    expect(run(throwing, 'portal').home).toBeUndefined()
  })
})

describe('vite config', () => {
  const config = viteConfig as (env: ConfigEnv) => UserConfig

  it('refuses to build the dev-only showcase', () => {
    expect(() => config({ mode: 'showcase', command: 'build' })).toThrow(/dev-only/)
    expect(() => config({ mode: 'showcase', command: 'serve' })).not.toThrow()
  })

  it('refuses unknown modes', () => {
    expect(() => config({ mode: 'production', command: 'build' })).toThrow(/unknown mode/)
  })

  it('emits the boot script under assets/ with a content hash', () => {
    expect(themeBootFileName).toMatch(/^assets\/theme-boot-[0-9a-f]{8}\.js$/)
  })

  it('never inlines assets (font-src and img-src do not allow every data: use)', () => {
    expect(config({ mode: 'portal', command: 'build' }).build?.assetsInlineLimit).toBe(0)
  })

  it('mounts the dev mock API only while serving, never in a build', () => {
    const names = (env: ConfigEnv) => (config(env).plugins ?? []).flat().map((p) => (p && typeof p === 'object' && 'name' in p ? p.name : null))
    expect(names({ mode: 'admin', command: 'serve' })).toContain('pandora-mock-api')
    expect(names({ mode: 'portal', command: 'serve' })).toContain('pandora-mock-api')
    expect(names({ mode: 'admin', command: 'build' })).not.toContain('pandora-mock-api')
    expect(names({ mode: 'portal', command: 'build' })).not.toContain('pandora-mock-api')
  })
})
