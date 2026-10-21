import { COLOR_TOKENS } from '../../../styles/design-tokens'
import type { Theme } from './schemas'

// ---------------------------------------------------------------------------
// 编辑器里能改的 5 个浅色颜色：键是表单字段名，令牌名与后端 422 的 fields 键对应
// ---------------------------------------------------------------------------
export const THEME_COLORS = [
  ['bg', '--bg', '背景'],
  ['line', '--border', '分隔线'],
  ['muted', '--text-2', '次要文字'],
  ['ink', '--text', '主文字'],
  ['accent', '--brand', '品牌色'],
] as const
export type ColorKey = (typeof THEME_COLORS)[number][0]

/** 令牌名 → 设计规范默认浅色（缺键回退用） */
const LIGHT_DEFAULT: Readonly<Record<string, string>> = Object.fromEntries(COLOR_TOKENS.map((t) => [t.name, t.light]))

/** 新建（from = 复制来源，null 是空白新建）或编辑一个自定义主题 */
export type ThemeTarget = { kind: 'create'; from: Theme | null } | { kind: 'edit'; theme: Theme }

export interface ThemeForm {
  code: string
  name: string
  tagline: string
  colors: Record<ColorKey, string>
}

/** 键与后端 422 的 fields 一致 */
export type ThemeErrors = Record<string, string>

const str = (v: unknown) => (typeof v === 'string' ? v : '')

/** 生效主题的标语：去空白后为空就是没设 */
export function themeTagline(t: Pick<Theme, 'branding'>): string | null {
  const v = str(t.branding.tagline).trim()
  return v || null
}

/** 3 位十六进制展开成 6 位并转小写：#abc → #aabbcc；其它原样返回 */
function expandHex(value: string): string {
  const v = value.trim()
  if (/^#[0-9a-fA-F]{3}$/.test(v)) return `#${[...v.slice(1)].map((c) => c + c).join('')}`.toLowerCase()
  return v
}

/** 来源主题（编辑 = theme，复制 = from，空白新建 = null） */
const sourceOf = (target: ThemeTarget): Theme | null => (target.kind === 'edit' ? target.theme : target.from)

export function themeToForm(target: ThemeTarget): ThemeForm {
  const source = sourceOf(target)
  const branding = source?.branding ?? {}
  let code = ''
  let name = ''
  if (target.kind === 'edit') {
    code = target.theme.code
    name = target.theme.name
  } else if (source) {
    code = `${source.code}-copy`.slice(0, 39)
    name = `${[...source.name].slice(0, 57).join('')} 副本`
  }
  const colors = {} as Record<ColorKey, string>
  for (const [key, token] of THEME_COLORS) {
    colors[key] = expandHex(source?.tokens.light[token] ?? LIGHT_DEFAULT[token] ?? '')
  }
  return { code, name, tagline: str(branding.tagline).trim(), colors }
}

// ---------------------------------------------------------------------------
// 校验：与 Go 同一套规则（前端先拦一遍，后端照样会再校验）
// ---------------------------------------------------------------------------
const CODE_PATTERN = /^[a-z][a-z0-9_-]{1,38}$/
const COLOR_PATTERN = /^#[0-9a-fA-F]{6}$/
const runes = (v: string) => [...v].length

export function validateThemeForm(form: ThemeForm, creating: boolean): ThemeErrors {
  const errors: ThemeErrors = {}
  const code = form.code.trim().toLowerCase()
  if (creating) {
    if (!code) errors.code = '主题标识必填'
    else if (!CODE_PATTERN.test(code)) errors.code = '以小写字母开头，只含小写字母、数字、- 与 _，共 2–39 个字符'
  }
  const name = form.name.trim()
  if (!name || runes(name) > 60) errors.name = '主题名称必填，最多 60 个字'
  if (runes(form.tagline.trim()) > 80) errors['branding.tagline'] = '标语最多 80 个字'
  for (const [key, token] of THEME_COLORS) {
    if (!COLOR_PATTERN.test(form.colors[key].trim())) errors[`tokens.light.${token}`] = '写成 #RRGGBB'
  }
  return errors
}

interface ThemeRequest {
  code: string
  name: string
  create: boolean
  tokens: Record<'light' | 'dark', Record<string, string>>
  branding: { tagline?: string; logo?: string }
}

/**
 * 两组令牌一律补满 48 个：来源主题缺的键（旧主题、只存了几个键的自定义主题）用设计稿默认值，
 * 白名单外的键丢掉——保存时整组提交，后端不会再收到不认识的键
 */
function fullTokens(source: Theme | null): ThemeRequest['tokens'] {
  const out = { light: {}, dark: {} } as ThemeRequest['tokens']
  for (const mode of ['light', 'dark'] as const) {
    for (const t of COLOR_TOKENS) out[mode][t.name] = source?.tokens[mode][t.name]?.trim() || t[mode]
  }
  return out
}

// ---------------------------------------------------------------------------
// 品牌色的派生色：门户主按钮是 --brand → --brand-deep 渐变、hot 按钮再加 --brand-mark，
// 主卡描边用 --brand-line。编辑器只让改 --brand，改了就按它推出这三个（浅色组），
// 免得换了品牌色、按钮还是半截森林绿。没改品牌色时保留来源主题手调的值。
// ---------------------------------------------------------------------------
type Rgb = readonly [number, number, number]

const hexToRgb = (hex: string): Rgb => {
  const n = Number.parseInt(hex.slice(1), 16)
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255]
}

const rgbToHex = (c: Rgb): string => `#${c.map((v) => Math.round(Math.min(255, Math.max(0, v))).toString(16).padStart(2, '0')).join('')}`

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

/** 由品牌色与背景色推出 --brand-deep（压暗）、--brand-mark（提亮提纯）、--brand-line（混向背景） */
export function brandDerived(brand: string, bg: string): Record<'--brand-deep' | '--brand-mark' | '--brand-line', string> {
  const b = hexToRgb(brand)
  const [h, s, l] = rgbToHsl(b)
  return {
    '--brand-deep': rgbToHex(mix(b, [0, 0, 0], 0.3)),
    '--brand-mark': rgbToHex(hslToRgb([h, Math.min(1, s + 0.12), Math.min(0.62, l + 0.25)])),
    '--brand-line': rgbToHex(mix(b, hexToRgb(bg), 0.7)),
  }
}

export function buildThemeRequest(form: ThemeForm, target: ThemeTarget): { ok: true; body: ThemeRequest } | { ok: false; errors: ThemeErrors } {
  const creating = target.kind === 'create'
  const errors = validateThemeForm(form, creating)
  if (Object.keys(errors).length > 0) return { ok: false, errors }
  const source = sourceOf(target)
  const tokens = fullTokens(source)
  const brandBefore = expandHex(tokens.light['--brand'] ?? '')
  for (const [key, token] of THEME_COLORS) tokens.light[token] = form.colors[key].trim()
  const brand = expandHex(form.colors.accent.trim())
  if (brand !== brandBefore) Object.assign(tokens.light, brandDerived(brand, expandHex(form.colors.bg.trim())))
  const branding: ThemeRequest['branding'] = {}
  if (form.tagline.trim()) branding.tagline = form.tagline.trim()
  // 界面不再提供 Logo，但来源主题已有 Logo（data:image/）原样带上，不因保存丢掉
  const logo = str(source?.branding.logo)
  if (logo.startsWith('data:image/')) branding.logo = logo
  return { ok: true, body: { code: creating ? form.code.trim().toLowerCase() : target.theme.code, name: form.name.trim(), create: creating, tokens, branding } }
}

/** 主题列表行上的 5 个色块：浅色组取值，缺键回退当前界面令牌 */
export function themeSwatches(t: Pick<Theme, 'tokens'>): string[] {
  return THEME_COLORS.map(([, token]) => t.tokens.light[token] ?? LIGHT_DEFAULT[token] ?? `var(${token})`)
}

/** 激活确认框：先说后果。站点名只认系统设置（site.name），不随生效主题变化，在「站点设置」里改 */
export function activateNotice(target: Theme): string {
  return `用户端所有用户看到的都会换成「${target.name}」，明暗两组随用户切换，正在使用的主题会被替换。站点名不随主题变化，在本页的「站点设置」里改。`
}
