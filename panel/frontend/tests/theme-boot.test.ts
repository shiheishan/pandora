/**
 * [INPUT]: 依赖 node:fs / node:vm 执行 src/core/theme-boot.js，依赖 src/core/theme.ts 的 THEME_STORAGE_KEY，依赖 vite.config.ts 的配置函数与 themeBootFileName
 * [OUTPUT]: 对外提供主题引导脚本与构建配置的测试
 * [POS]: tests 的首帧守卫：引导脚本在各种存储状态下都写出合法主题、与 theme.ts 用同一个键；showcase 不可能被构建进产物
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import type { ConfigEnv, UserConfig } from 'vite'
import { describe, expect, it } from 'vitest'
import { THEME_STORAGE_KEY } from '../src/core/theme'
import viteConfig, { themeBootFileName } from '../vite.config'

const source = readFileSync(new URL('../src/core/theme-boot.js', import.meta.url), 'utf8')

function boot(getItem: (key: string) => string | null): string | undefined {
  const attrs: Record<string, string> = {}
  runInNewContext(source, {
    window: { localStorage: { getItem } },
    document: { documentElement: { setAttribute: (k: string, v: string) => (attrs[k] = v) } },
  })
  return attrs['data-theme']
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
})
