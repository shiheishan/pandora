/**
 * 品牌色派生（浅色组三个 + 深色组九个令牌）。
 *
 * 为什么在前端：颜色令牌的唯一来源是 design-tokens.ts。后台保存主题时按新品牌色推导并写入请求体；
 * 门户加载外观时再算一次，旧主题没有深色推导字段也能对齐；两边调用同一个函数，结果一致。
 *
 * 深色组推导：色相随浅色品牌色偏移、饱和度按缺省品牌色比例缩放、亮度锁在 design-tokens 缺省深色值
 * 的同一侧（up 不低于缺省、down 不高于缺省），深色下对比度不低于缺省深色组；缺省品牌色时恒等于 COLOR_TOKENS 的 dark。
 */
import { COLOR_TOKENS } from './design-tokens'

type Rgb = readonly [number, number, number]

const UP_TOKENS = [
  '--brand',
  '--brand-hover',
  '--brand-ink',
  '--brand-deep',
  '--brand-line',
  '--brand-mark',
] as const

const DOWN_TOKENS = ['--brand-soft', '--brand-tint', '--on-brand'] as const

type DarkBrandKey = (typeof UP_TOKENS)[number] | (typeof DOWN_TOKENS)[number]

function token(name: string): { light: string; dark: string } {
  const row = COLOR_TOKENS.find((t) => t.name === name)
  if (!row) throw new Error(`未知颜色令牌: ${name}`)
  return row
}

/** 解析 #rgb / #rrggbb（大小写不限），非法返回 null */
function parseHex(value: string): Rgb | null {
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

const rgbToHex = (c: Rgb): string =>
  `#${c.map((v) => Math.round(Math.min(255, Math.max(0, v))).toString(16).padStart(2, '0')).join('')}`

/** 按比例 t（0–1）从 a 混向 b */
const mix = (a: Rgb, b: Rgb, t: number): Rgb => [a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t, a[2] + (b[2] - a[2]) * t]

function rgbToHsl([r, g, b]: Rgb): Rgb {
  const [rn, gn, bn] = [r / 255, g / 255, b / 255]
  const max = Math.max(rn, gn, bn)
  const min = Math.min(rn, gn, bn)
  const l = (max + min) / 2
  if (max === min) return [0, 0, l]
  const d = max - min
  const s = l > 0.5 ? d / (2 - max - min) : d / (max + min)
  const h = max === rn ? (gn - bn) / d + (gn < bn ? 6 : 0) : max === gn ? (bn - rn) / d + 2 : (rn - gn) / d + 4
  return [h / 6, s, l]
}

function hslToRgb([h, s, l]: Rgb): Rgb {
  if (s === 0) return [l * 255, l * 255, l * 255]
  const q = l < 0.5 ? l * (1 + s) : l + s - l * s
  const p = 2 * l - q
  const hue = (t: number) => {
    const x = t < 0 ? t + 1 : t > 1 ? t - 1 : t
    if (x < 1 / 6) return p + (q - p) * 6 * x
    if (x < 1 / 2) return q
    if (x < 2 / 3) return p + (q - p) * (2 / 3 - x) * 6
    return p
  }
  return [hue(h + 1 / 3) * 255, hue(h) * 255, hue(h - 1 / 3) * 255]
}

function clamp01(v: number): number {
  return Math.min(1, Math.max(0, v))
}

/** WCAG 2 相对亮度 */
function luminance(c: Rgb): number {
  const linear = (v: number) => {
    const x = v / 255
    return x <= 0.04045 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4
  }
  const [r, g, b] = c
  return 0.2126 * linear(r) + 0.7152 * linear(g) + 0.0722 * linear(b)
}

/** 浅色组：由品牌色与背景色推出 --brand-deep（压暗 30%）、--brand-mark（提亮提纯）、--brand-line（混向背景 70%）。两个参数都是 #rrggbb。 */
export function brandDerived(brand: string, bg: string): Record<'--brand-deep' | '--brand-mark' | '--brand-line', string> {
  const b = parseHex(brand)!
  const bgRgb = parseHex(bg)!
  const [h, s, l] = rgbToHsl(b)
  return {
    '--brand-deep': rgbToHex(mix(b, [0, 0, 0], 0.3)),
    '--brand-mark': rgbToHex(hslToRgb([h, Math.min(1, s + 0.12), Math.min(0.62, l + 0.25)])),
    '--brand-line': rgbToHex(mix(b, bgRgb, 0.7)),
  }
}

/**
 * 深色组：由浅色组的品牌色推出深色组跟着品牌色走的 9 个令牌。品牌色不是 #rgb / #rrggbb（大小写不限）时返回 null，调用方保留原值。
 * 缺省品牌色（design-tokens 里 --brand 的浅色值）得到的就是 design-tokens 的缺省深色值。
 */
export function darkBrandTokens(lightBrand: string): Record<string, string> | null {
  const brand = parseHex(lightBrand)
  if (!brand) return null

  const base = parseHex(token('--brand').light)!
  const [h0, s0] = rgbToHsl(base)
  const [hb, sb] = rgbToHsl(brand)
  const dh = hb - h0
  const ratio = s0 > 0 ? sb / s0 : 1

  const out: Partial<Record<DarkBrandKey, string>> = {}

  const derive = (key: DarkBrandKey, direction: 'up' | 'down'): string => {
    const def = parseHex(token(key).dark)!
    const [ht, st, lt] = rgbToHsl(def)
    const h = ((ht + dh) % 1 + 1) % 1
    const s = clamp01(st * ratio)
    const target = luminance(def)

    const hexAt = (l: number) => rgbToHex(hslToRgb([h, s, l]))
    const ok = (l: number): boolean => {
      const parsed = parseHex(hexAt(l))
      if (!parsed) return false
      const L = luminance(parsed)
      return direction === 'up' ? L >= target : L <= target
    }

    if (ok(lt)) return hexAt(lt)

    if (direction === 'up') {
      let lo = lt
      let hi = 1
      for (let i = 0; i < 40; i++) {
        const mid = (lo + hi) / 2
        if (ok(mid)) hi = mid
        else lo = mid
      }
      return hexAt(hi)
    }

    let lo = 0
    let hi = lt
    for (let i = 0; i < 40; i++) {
      const mid = (lo + hi) / 2
      if (ok(mid)) lo = mid
      else hi = mid
    }
    return hexAt(lo)
  }

  for (const key of UP_TOKENS) out[key] = derive(key, 'up')
  for (const key of DOWN_TOKENS) out[key] = derive(key, 'down')

  return out as Record<string, string>
}
