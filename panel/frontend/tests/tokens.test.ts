/**
 * [INPUT]: 依赖 node:fs 读取 src/styles/*.css，依赖 src/styles/design-tokens.ts 的设计稿原值
 * [OUTPUT]: 对外提供令牌契约测试
 * [POS]: tests 的样式守卫：tokens.css / roles.css 必须与设计稿逐值一致、明暗两组键相同、所有 var() 都有定义、字体只引用包内文件
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import {
  COLOR_TOKENS,
  RADIUS_SCALE,
  ROLE_TOKENS,
  SPACE_SCALE,
  TYPE_SCALE,
  normalizeCssValue,
} from '../src/styles/design-tokens'

const stylesDir = new URL('../src/styles/', import.meta.url)
const read = (name: string) => readFileSync(new URL(name, stylesDir), 'utf8')

type Declarations = Map<string, string>

// 只认本工程的写法：顶层规则 + 一层 @media，不需要通用 CSS 解析器。
// @media 里的覆盖（窄屏边距）单独收集，不混进顶层 :root。
function parseBlocks(css: string): Map<string, Declarations[]> {
  const stripped = css.replace(/\/\*[\s\S]*?\*\//g, '').replace(/@media[^{]*\{(?:[^{}]*\{[^{}]*\})*\s*\}/g, '')
  const blocks = new Map<string, Declarations[]>()
  for (const [, selector, body] of stripped.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const decls: Declarations = new Map()
    for (const [, name, value] of body!.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) decls.set(name!, value!.trim())
    const key = selector!.trim().replace(/\s+/g, ' ')
    blocks.set(key, [...(blocks.get(key) ?? []), decls])
  }
  return blocks
}

const merged = (list: Declarations[] | undefined): Declarations => new Map(list?.flatMap((d) => [...d]) ?? [])

const tokens = parseBlocks(read('tokens.css'))
const light = merged(tokens.get(':root'))
const dark = merged(tokens.get(":root[data-theme='dark']"))
const roles = parseBlocks(read('roles.css'))
const portal = merged(roles.get(":root[data-app='portal']"))
const admin = merged(roles.get(":root[data-app='admin']"))

describe('colour tokens match the design spec', () => {
  it.each(COLOR_TOKENS.map((t) => [t.name, t] as const))('%s', (name, token) => {
    expect(normalizeCssValue(light.get(name) ?? 'missing')).toBe(normalizeCssValue(token.light))
    expect(normalizeCssValue(dark.get(name) ?? 'missing')).toBe(normalizeCssValue(token.dark))
  })

  it('declares exactly the same colour keys in light and dark', () => {
    const themed = (d: Declarations) => [...d.keys()].filter((k) => COLOR_TOKENS.some((t) => t.name === k)).sort()
    const darkKeys = [...dark.keys()].sort()
    expect(darkKeys).toEqual(themed(light))
    expect(darkKeys).toEqual(COLOR_TOKENS.map((t) => t.name).sort())
  })
})

describe('scale tokens match the design spec', () => {
  it('spacing and radii', () => {
    for (const t of [...SPACE_SCALE, ...RADIUS_SCALE]) expect(light.get(t.name), t.name).toBe(t.value)
  })

  it('type scale sizes and tracking', () => {
    for (const t of TYPE_SCALE) {
      expect(light.get(`--fs-${t.name}`), t.name).toBe(t.size)
      if (t.tracking !== '0') expect(light.get(`--ls-${t.name}`), t.name).toBe(t.tracking)
    }
  })
})

describe('role tokens', () => {
  it.each(ROLE_TOKENS.map((t) => [t.name, t] as const))('%s', (name, token) => {
    expect(portal.get(name)).toBe(token.portal)
    expect(admin.get(name)).toBe(token.admin)
  })

  it('portal and admin declare the same role keys', () => {
    expect([...admin.keys()].sort()).toEqual([...portal.keys()].sort())
    expect([...portal.keys()].sort()).toEqual(ROLE_TOKENS.map((t) => t.name).sort())
  })
})

describe('stylesheets stay self-contained', () => {
  const files = readdirSync(stylesDir).filter((f) => f.endsWith('.css'))
  const declared = new Set(
    files.flatMap((f) => [...read(f).matchAll(/(--[\w-]+)\s*:/g)].map((m) => m[1]!)),
  )

  it.each(files)('%s only references declared custom properties', (file) => {
    for (const [, name] of read(file).matchAll(/var\((--[\w-]+)/g)) expect(declared, `${file}: ${name}`).toContain(name)
  })

  it.each(files)('%s loads nothing from other origins', (file) => {
    // CSP 是 font-src / style-src 'self'：外链字体或 @import 远程样式上线后会被浏览器拒绝
    expect(read(file)).not.toMatch(/url\(\s*['"]?(https?:)?\/\//)
    expect(read(file)).not.toMatch(/@import\s+['"]?(https?:)?\/\//)
  })

  it('every @font-face source is a bundled file', () => {
    const urls = [...read('fonts.css').matchAll(/url\('([^']+)'\)/g)].map((m) => m[1]!)
    expect(urls.length).toBe(4)
    for (const url of urls) expect(existsSync(new URL(url, stylesDir)), url).toBe(true)
    expect(existsSync(new URL('fonts/OFL.txt', stylesDir))).toBe(true)
  })
})
