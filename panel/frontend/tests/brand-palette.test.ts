import { describe, expect, it } from 'vitest'
import { COLOR_TOKENS } from '../src/styles/design-tokens'
import { brandDerived, darkBrandTokens } from '../src/styles/brand-palette'

const DARK_BRAND_KEYS = [
  '--brand',
  '--brand-hover',
  '--brand-ink',
  '--brand-deep',
  '--brand-line',
  '--brand-mark',
  '--brand-soft',
  '--brand-tint',
  '--on-brand',
] as const

function parseHexLocal(value: string): [number, number, number] | null {
  const raw = value.trim()
  let hex = raw
  if (/^#[0-9a-f]{3}$/i.test(raw)) {
    const [, a, b, c] = raw.match(/^#([0-9a-f])([0-9a-f])([0-9a-f])$/i)!
    hex = `#${a}${a}${b}${b}${c}${c}`
  }
  if (!/^#[0-9a-f]{6}$/i.test(hex)) return null
  const n = Number.parseInt(hex.slice(1), 16)
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255]
}

function relLuminance(hex: string): number {
  const c = parseHexLocal(hex)
  if (!c) return 0
  const linear = (v: number) => {
    const x = v / 255
    return x <= 0.04045 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * linear(c[0]) + 0.7152 * linear(c[1]) + 0.0722 * linear(c[2])
}

/** WCAG 对比度 */
function contrast(a: string, b: string): number {
  const L1 = relLuminance(a)
  const L2 = relLuminance(b)
  const lighter = Math.max(L1, L2)
  const darker = Math.min(L1, L2)
  return (lighter + 0.05) / (darker + 0.05)
}

function tokenDark(name: string): string {
  const row = COLOR_TOKENS.find((t) => t.name === name)
  if (!row) throw new Error(name)
  return row.dark
}

function tokenLight(name: string): string {
  const row = COLOR_TOKENS.find((t) => t.name === name)
  if (!row) throw new Error(name)
  return row.light
}

function defaultDarkBrandMap(): Record<string, string> {
  const m: Record<string, string> = {}
  for (const key of DARK_BRAND_KEYS) m[key] = tokenDark(key)
  return m
}

const SAMPLE_BRANDS = [
  '#b9442b',
  '#7c3aed',
  '#2563eb',
  '#facc15',
  '#000000',
  '#ffffff',
  '#808080',
  '#e11d48',
  '#0891b2',
  '#abc',
]

describe('darkBrandTokens', () => {
  const defaultLightBrand = tokenLight('--brand')

  it('缺省品牌色与 COLOR_TOKENS 深色值一致（含大写输入）', () => {
    const derived = darkBrandTokens(defaultLightBrand)
    expect(derived).not.toBeNull()
    for (const key of DARK_BRAND_KEYS) {
      expect(derived![key]).toBe(tokenDark(key))
    }
    const upper = darkBrandTokens(defaultLightBrand.toUpperCase())
    expect(upper).not.toBeNull()
    for (const key of DARK_BRAND_KEYS) {
      expect(upper![key]).toBe(tokenDark(key))
    }
  })

  it('非法输入返回 null', () => {
    for (const bad of ['red', 'rgb(1,2,3)', '', '#12345', '#ggg']) {
      expect(darkBrandTokens(bad)).toBeNull()
    }
  })

  it('输出形状与确定性', () => {
    for (const brand of SAMPLE_BRANDS) {
      const once = darkBrandTokens(brand)
      expect(once).not.toBeNull()
      expect(Object.keys(once!).sort()).toEqual([...DARK_BRAND_KEYS].sort())
      for (const key of DARK_BRAND_KEYS) {
        expect(once![key]).toMatch(/^#[0-9a-f]{6}$/)
      }
      expect(darkBrandTokens(brand)).toEqual(once)
    }
  })

  it('对比度不低于缺省深色组（样例品牌色）', () => {
    const def = defaultDarkBrandMap()
    const bg = tokenDark('--bg')
    const surface = tokenDark('--surface')

    const pairs: [string, string, string, string][] = [
      ['--brand', '--bg', '--brand', bg],
      ['--brand-ink', '--surface', '--brand-ink', surface],
      ['--on-brand', '--brand', '--on-brand', def['--brand']],
      ['--on-brand', '--brand-deep', '--on-brand', def['--brand-deep']],
      ['--brand-ink', '--brand-soft', '--brand-ink', def['--brand-soft']],
      ['--brand', '--brand-tint', '--brand', def['--brand-tint']],
    ]

    for (const brand of SAMPLE_BRANDS) {
      const derived = darkBrandTokens(brand)!
      for (const [fgKey, bgKey, defFgKey, defBgVal] of pairs) {
        const defRatio = contrast(def[defFgKey], defBgVal)
        const newBg =
          bgKey === '--bg' ? bg : bgKey === '--surface' ? surface : derived[bgKey]
        const newRatio = contrast(derived[fgKey], newBg)
        expect(newRatio).toBeGreaterThanOrEqual(defRatio - 1e-9)
      }
    }
  })

  it('色相跟着品牌色走（蓝色样例）', () => {
    const blue = '#2563eb'
    const derived = darkBrandTokens(blue)!
    const defBrand = tokenDark('--brand')
    expect(derived['--brand']).not.toBe(defBrand)
    const rgb = parseHexLocal(derived['--brand'])!
    const [, g, b] = rgb
    expect(b).toBeGreaterThan(g)
  })
})

describe('brandDerived', () => {
  it('推出三个浅色派生色', () => {
    const brand = '#2b6cb9'
    const out = brandDerived(brand, '#f4f5f2')
    for (const key of ['--brand-deep', '--brand-mark', '--brand-line'] as const) {
      expect(out[key]).toMatch(/^#[0-9a-f]{6}$/)
    }
    const brandRgb = parseHexLocal(brand)!
    const deepRgb = parseHexLocal(out['--brand-deep'])!
    expect(deepRgb[0]).toBeLessThanOrEqual(brandRgb[0])
    expect(deepRgb[1]).toBeLessThanOrEqual(brandRgb[1])
    expect(deepRgb[2]).toBeLessThanOrEqual(brandRgb[2])
  })
})
