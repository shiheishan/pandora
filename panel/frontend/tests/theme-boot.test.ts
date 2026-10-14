import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import type { ConfigEnv, UserConfig } from 'vite'
import { describe, expect, it } from 'vitest'
import { THEME_STORAGE_KEY } from '../src/core/theme'
import { COLOR_TOKENS } from '../src/styles/design-tokens'
import viteConfig, { themeBootFileName } from '../vite.config'

const source = readFileSync(new URL('../src/core/theme-boot.js', import.meta.url), 'utf8')
// appearance.ts 依赖 React 与运行时，这个（node 侧的）测试不 import 它，按源码文本核对键名
const appearanceSource = readFileSync(new URL('../src/portal/appearance.ts', import.meta.url), 'utf8')
const APPEARANCE_CACHE_KEY = 'pandora-portal-appearance'

function run(getItem: (key: string) => string | null, app = 'portal') {
  const attrs: Record<string, string> = { 'data-app': app }
  const props: Record<string, string> = {}
  runInNewContext(source, {
    window: { localStorage: { getItem } },
    document: {
      documentElement: {
        setAttribute: (k: string, v: string) => (attrs[k] = v),
        getAttribute: (k: string) => attrs[k] ?? null,
        style: { setProperty: (k: string, v: string) => (props[k] = v) },
      },
    },
  })
  return { theme: attrs['data-theme'], props }
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
    expect(run(storage(null, cache)).props).toEqual({ '--brand': '#b9442b', '--bg': '#fffaf0' })
    expect(run(storage('dark', cache))).toEqual({ theme: 'dark', props: { '--brand': '#e46e52' } })
  })

  it('never paints the portal colours onto the admin console (same origin, same storage)', () => {
    expect(run(storage(null, cache), 'admin').props).toEqual({})
  })

  it('ignores broken, foreign-shaped or hostile cache entries', () => {
    for (const bad of ['{', 'null', '[]', '{"v":2,"light":{"--brand":"red"}}', '{"v":1,"light":"red"}']) {
      expect(run(storage(null, bad))).toEqual({ theme: 'light', props: {} })
    }
    const hostile = JSON.stringify({ v: 1, light: { color: 'red', '--Brand': 'red', '--ok': 'x'.repeat(201), '--fine': 'blue', '--n': 3 } })
    expect(run(storage(null, hostile)).props).toEqual({ '--fine': 'blue' })
  })

  it('accepts every themeable token name', () => {
    for (const { name } of COLOR_TOKENS) expect(name).toMatch(/^--[a-z0-9-]+$/)
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
