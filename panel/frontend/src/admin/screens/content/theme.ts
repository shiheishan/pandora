import { COLOR_TOKENS, type ColorToken, type TokenGroup } from '../../../styles/design-tokens'
import type { Theme } from './schemas'

export type ThemeMode = 'light' | 'dark'
export const THEME_MODES: readonly ThemeMode[] = ['light', 'dark']

// ---------------------------------------------------------------------------
// 令牌分组：顺序与名单都来自 design-tokens.ts，后端白名单照抄同一份（Go 测试逐条对照）
// ---------------------------------------------------------------------------
const SECTION_LABEL: Readonly<Record<TokenGroup, string>> = {
  neutral: '中性色',
  brand: '品牌色',
  status: '状态色',
  overlay: '叠加与阴影',
  supplement: '补充（危险按钮字色、Toast、后台侧栏）',
}
export const TOKEN_SECTIONS: ReadonlyArray<{ group: TokenGroup; label: string; tokens: readonly ColorToken[] }> = (Object.keys(SECTION_LABEL) as TokenGroup[]).map((group) => ({
  group,
  label: SECTION_LABEL[group],
  tokens: COLOR_TOKENS.filter((t) => t.group === group),
}))

const TOKEN_NAMES: ReadonlySet<string> = new Set(COLOR_TOKENS.map((t) => t.name))

// ---------------------------------------------------------------------------
// 表单
// ---------------------------------------------------------------------------
export interface ThemeForm {
  code: string
  name: string
  siteName: string
  tagline: string
  /** data:image/…;base64,… 或空串 */
  logo: string
  tokens: Record<ThemeMode, Record<string, string>>
}

/** 新建（from = 复制来源，默认是当前生效主题；没有主题时取设计稿默认值）或编辑一个自定义主题 */
export type ThemeTarget = { kind: 'create'; from: Theme | null } | { kind: 'edit'; theme: Theme }

/** 键与后端 422 的 fields 一致 */
export type ThemeErrors = Record<string, string>

const str = (v: unknown) => (typeof v === 'string' ? v : '')

export function siteName(t: Pick<Theme, 'branding'>): string | null {
  const v = str(t.branding.site_name).trim()
  return v || null
}

/**
 * 两组令牌一律补满 43 个：源主题缺的键（旧主题、只存了几个键的自定义主题）用设计稿默认值，
 * 白名单外的键丢掉——保存时整组提交，后端不会再收到不认识的键
 */
function fullTokens(source: Theme | null): ThemeForm['tokens'] {
  const out = { light: {}, dark: {} } as ThemeForm['tokens']
  for (const mode of THEME_MODES) {
    for (const t of COLOR_TOKENS) out[mode][t.name] = source?.tokens[mode][t.name]?.trim() || t[mode]
  }
  return out
}

export function themeToForm(target: ThemeTarget): ThemeForm {
  const source = target.kind === 'edit' ? target.theme : target.from
  const branding = source?.branding ?? {}
  const name = target.kind === 'edit' ? target.theme.name : source ? `${source.name} 副本`.slice(0, 60) : ''
  return {
    code: target.kind === 'edit' ? target.theme.code : '',
    name,
    siteName: str(branding.site_name).trim() || (source ? '' : 'Pandora'),
    tagline: str(branding.tagline),
    logo: str(branding.logo).startsWith('data:image/') ? str(branding.logo) : '',
    tokens: fullTokens(source),
  }
}

// ---------------------------------------------------------------------------
// 校验：与 Go 同一套规则（前端先拦一遍，后端照样会再校验）
// ---------------------------------------------------------------------------
const CODE_PATTERN = /^[a-z][a-z0-9_-]{1,38}$/
const TOKEN_BAD = /[;{}<>\\]/
const runes = (v: string) => [...v].length
export const LOGO_MAX_BYTES = 48 * 1024
const LOGO_TYPES = ['image/png', 'image/jpeg', 'image/webp', 'image/svg+xml']

export function tokenValueError(value: string): string | null {
  const v = value.trim()
  if (!v) return '必填'
  if (v.length > 200 || TOKEN_BAD.test(v)) return '须为 200 字符以内，不含 ; { } < > \\'
  return null
}

/** 上传前的检查；null = 可以读成 data URL */
export function logoProblem(file: { type: string; size: number }): string | null {
  if (!LOGO_TYPES.includes(file.type)) return 'Logo 只支持 PNG、JPEG、WebP 或 SVG'
  if (file.size > LOGO_MAX_BYTES) return 'Logo 不能超过 48KB'
  return null
}

export function validateThemeForm(form: ThemeForm, creating: boolean): ThemeErrors {
  const errors: ThemeErrors = {}
  const code = form.code.trim().toLowerCase()
  if (creating) {
    if (!code) errors.code = '主题标识必填'
    else if (!CODE_PATTERN.test(code)) errors.code = '以小写字母开头，只含小写字母、数字、- 与 _，共 2–39 个字符'
  }
  const name = form.name.trim()
  if (!name || runes(name) > 60) errors.name = '主题名称必填，最多 60 个字'
  const site = form.siteName.trim()
  if (!site) errors['branding.site_name'] = '站点名称必填：门户标题、邮件里的站点名与发件人名都取自生效主题'
  else if (runes(site) > 40) errors['branding.site_name'] = '站点名称最多 40 个字'
  if (runes(form.tagline.trim()) > 80) errors['branding.tagline'] = '标语最多 80 个字'
  for (const mode of THEME_MODES) {
    for (const t of COLOR_TOKENS) {
      const problem = tokenValueError(form.tokens[mode][t.name] ?? '')
      if (problem) errors[`tokens.${mode}.${t.name}`] = problem
    }
  }
  return errors
}

export interface ThemeRequest {
  code: string
  name: string
  create: boolean
  tokens: Record<ThemeMode, Record<string, string>>
  branding: { site_name: string; tagline?: string; logo?: string }
}

export function buildThemeRequest(form: ThemeForm, creating: boolean): { ok: true; body: ThemeRequest } | { ok: false; errors: ThemeErrors } {
  const errors = validateThemeForm(form, creating)
  if (Object.keys(errors).length > 0) return { ok: false, errors }
  const tokens = { light: {}, dark: {} } as ThemeRequest['tokens']
  for (const mode of THEME_MODES) {
    for (const t of COLOR_TOKENS) tokens[mode][t.name] = form.tokens[mode][t.name]!.trim()
  }
  const branding: ThemeRequest['branding'] = { site_name: form.siteName.trim() }
  if (form.tagline.trim()) branding.tagline = form.tagline.trim()
  if (form.logo) branding.logo = form.logo
  return { ok: true, body: { code: form.code.trim().toLowerCase(), name: form.name.trim(), create: creating, tokens, branding } }
}

/** 某一组里有几个令牌报错：分段控件上显示，免得错误藏在没打开的那一组里 */
export function modeErrorCount(errors: ThemeErrors, mode: ThemeMode): number {
  return Object.keys(errors).filter((k) => k.startsWith(`tokens.${mode}.`)).length
}

export function firstErrorMode(errors: ThemeErrors): ThemeMode | null {
  return THEME_MODES.find((m) => modeErrorCount(errors, m) > 0) ?? null
}

// ---------------------------------------------------------------------------
// 预览与卡片
// ---------------------------------------------------------------------------

/**
 * 预览区的 CSS 自定义属性：只放白名单内、取值合法的令牌，挂在预览容器的 style 上（CSSOM，
 * 不受 CSP 约束），作用域就是预览这一棵子树——不污染后台其它地方
 */
export function previewVars(group: Readonly<Record<string, string>>): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [name, value] of Object.entries(group)) {
    if (TOKEN_NAMES.has(name) && tokenValueError(value) === null) out[name] = value.trim()
  }
  return out
}

/** 阴影类令牌的色块用 box-shadow 展示，其余用 background */
export const isShadowToken = (name: string) => name.includes('shadow')

/** 卡片上的三个色块：亮色组的背景、正文、品牌色；缺键回退到当前令牌 */
export function themeSwatches(t: Pick<Theme, 'tokens'>): { bg: string; fg: string; accent: string } {
  const light = t.tokens.light
  return { bg: light['--bg'] ?? 'var(--bg)', fg: light['--text'] ?? 'var(--text)', accent: light['--brand'] ?? 'var(--brand)' }
}

/** 激活确认框：先说后果。站点名跟着生效主题走，切换会改门户标题与邮件里的站点名、发件人名 */
export function activateNotice(active: Theme | undefined, target: Theme): string {
  const from = active ? (siteName(active) ?? 'Pandora') : 'Pandora'
  const to = siteName(target) ?? 'Pandora'
  const site =
    from === to
      ? `站点名称保持「${to}」。`
      : `站点名称会从「${from}」变成「${to}」：门户标题、邮件里的站点名与发件人名都随之改变（发件人名单独设置过的除外）。`
  return `门户所有用户刷新页面后看到「${target.name}」的配色。${site}`
}
