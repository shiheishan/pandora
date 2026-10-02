/**
 * [INPUT]: 依赖 vitest，依赖 ./theme 的全部纯函数，依赖 ./schemas 的 themesResponse，依赖 ../../../styles/design-tokens 的 COLOR_TOKENS
 * [OUTPUT]: 无（测试文件）
 * [POS]: admin/screens/content 主题逻辑的单元测试：旧数据宽松解析、另存为 / 编辑的表单初值（补满 43 键、丢白名单外）、与 Go 同规则的校验与 fields 键、请求体、分组错误计数、预览变量只放合法令牌、激活确认文案；界面交互在浏览器里对 dev 假后端验收
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it } from 'vitest'
import { COLOR_TOKENS } from '../../../styles/design-tokens'
import { themesResponse, type Theme } from './schemas'
import {
  TOKEN_SECTIONS,
  activateNotice,
  buildThemeRequest,
  firstErrorMode,
  isShadowToken,
  logoProblem,
  modeErrorCount,
  previewVars,
  siteName,
  themeSwatches,
  themeToForm,
  tokenValueError,
  validateThemeForm,
} from './theme'

const U = (n: number) => `00000000-0000-7000-8000-${String(n).padStart(12, '0')}`
const theme = (patch: Partial<Theme> = {}): Theme => ({
  id: U(1),
  code: 'paper',
  name: '默认 · 纸白',
  is_builtin: true,
  is_active: true,
  tokens: { light: { '--bg': '#f5f4f0', '--text': '#1c1c1f', '--brand': '#b9442b' }, dark: { '--brand': '#e46e52' } },
  branding: { site_name: 'Pandora' },
  custom_css: '',
  ...patch,
})

describe('theme schema', () => {
  it('parses the R19 shape and tolerates legacy rows instead of failing the whole list', () => {
    const ok = themesResponse.parse({ themes: [theme()] })
    expect(ok.themes![0]!.tokens.light['--brand']).toBe('#b9442b')
    expect(themesResponse.parse({ themes: null }).themes).toBeNull()
    // 00075 之前的自定义主题：扁平旧键、缺组、非字符串值、branding 不是对象
    const legacy = themesResponse.parse({ themes: [{ ...theme(), tokens: { brand: '#6d5efc', light: { '--bg': 1, '--text': '#000' } }, branding: 'x' }] })
    expect(legacy.themes![0]!.tokens).toEqual({ light: { '--text': '#000' }, dark: {} })
    expect(legacy.themes![0]!.branding).toEqual({})
    expect(themesResponse.parse({ themes: [{ ...theme(), tokens: [] }] }).themes![0]!.tokens).toEqual({ light: {}, dark: {} })
  })
})

describe('theme form', () => {
  it('groups all 43 design tokens in design-tokens order', () => {
    expect(TOKEN_SECTIONS.flatMap((s) => s.tokens.map((t) => t.name))).toEqual(COLOR_TOKENS.map((t) => t.name))
    expect(TOKEN_SECTIONS.map((s) => s.group)).toEqual(['neutral', 'brand', 'status', 'overlay', 'supplement'])
  })

  it('copies the source theme (branding included) when saving as, filling missing keys with design defaults', () => {
    const src = theme({ branding: { site_name: '星河', tagline: '一路畅通', logo: 'https://x.test/a.png', color: 'red' } })
    const form = themeToForm({ kind: 'create', from: src })
    expect(form.code).toBe('')
    expect(form.name).toBe('默认 · 纸白 副本')
    expect([form.siteName, form.tagline, form.logo]).toEqual(['星河', '一路畅通', ''])
    expect(Object.keys(form.tokens.light)).toHaveLength(43)
    expect(form.tokens.light['--brand']).toBe('#b9442b')
    expect(form.tokens.dark['--brand']).toBe('#e46e52')
    expect(form.tokens.dark['--bg']).toBe('#111113')
    // 没有任何主题时：设计稿默认值，站点名 Pandora
    const blank = themeToForm({ kind: 'create', from: null })
    expect([blank.name, blank.siteName]).toEqual(['', 'Pandora'])
    // 编辑：code 原样，白名单外的键不进表单
    const edit = themeToForm({ kind: 'edit', theme: theme({ code: 'night', is_builtin: false, tokens: { light: { '--nope': '#fff' }, dark: {} } }) })
    expect(edit.code).toBe('night')
    expect(edit.tokens.light['--nope']).toBeUndefined()
  })

  it('validates with the same rules and field keys as the Go side', () => {
    expect(tokenValueError('#fff')).toBeNull()
    expect(tokenValueError('0 1px 2px rgba(0,0,0,.1)')).toBeNull()
    for (const bad of ['', '  ', '#fff;color:red', '}body{', '<b>', 'a\\b', 'x'.repeat(201)]) expect(tokenValueError(bad)).not.toBeNull()

    const form = themeToForm({ kind: 'create', from: theme() })
    form.tokens.dark['--text'] = 'a{b'
    form.siteName = ' '
    form.tagline = '字'.repeat(81)
    const errors = validateThemeForm(form, true)
    expect(Object.keys(errors).sort()).toEqual(['branding.site_name', 'branding.tagline', 'code', 'tokens.dark.--text'])
    expect(validateThemeForm({ ...form, code: '9abc' }, true).code).toMatch(/小写字母开头/)
    // 编辑时 code 只读，不再校验
    expect(validateThemeForm({ ...form, code: '' }, false).code).toBeUndefined()
    expect(modeErrorCount(errors, 'dark')).toBe(1)
    expect(modeErrorCount(errors, 'light')).toBe(0)
    expect(firstErrorMode(errors)).toBe('dark')
    expect(firstErrorMode({ name: 'x' })).toBeNull()
  })

  it('builds a full request body with branding and the create flag', () => {
    const form = themeToForm({ kind: 'create', from: theme() })
    form.code = ' Night-Sea '
    form.name = ' 夜海 '
    form.siteName = '夜海加速'
    form.tokens.light['--brand'] = ' #2b6cb9 '
    const built = buildThemeRequest(form, true)
    if (!built.ok) throw new Error(JSON.stringify(built.errors))
    expect(built.body.code).toBe('night-sea')
    expect(built.body.name).toBe('夜海')
    expect(built.body.create).toBe(true)
    expect(built.body.branding).toEqual({ site_name: '夜海加速' })
    expect(built.body.tokens.light['--brand']).toBe('#2b6cb9')
    expect(Object.keys(built.body.tokens.dark)).toHaveLength(43)
    const withLogo = buildThemeRequest({ ...form, tagline: '口号', logo: 'data:image/png;base64,AAAA' }, false)
    expect(withLogo.ok && withLogo.body.branding).toEqual({ site_name: '夜海加速', tagline: '口号', logo: 'data:image/png;base64,AAAA' })
    expect(withLogo.ok && withLogo.body.create).toBe(false)
  })

  it('checks logo files before reading them', () => {
    expect(logoProblem({ type: 'image/png', size: 1024 })).toBeNull()
    expect(logoProblem({ type: 'image/svg+xml', size: 48 * 1024 })).toBeNull()
    expect(logoProblem({ type: 'image/gif', size: 10 })).toMatch(/PNG/)
    expect(logoProblem({ type: 'image/png', size: 48 * 1024 + 1 })).toMatch(/48KB/)
  })
})

describe('theme preview and cards', () => {
  it('scopes preview variables to valid whitelisted tokens only', () => {
    expect(previewVars({ '--bg': ' #fff ', '--brand': 'a;b', '--radius-lg': '12px', bg: '#000' })).toEqual({ '--bg': '#fff' })
    expect(isShadowToken('--shadow-segment')).toBe(true)
    expect(isShadowToken('--brand')).toBe(false)
    expect(themeSwatches(theme())).toEqual({ bg: '#f5f4f0', fg: '#1c1c1f', accent: '#b9442b' })
    expect(themeSwatches({ tokens: { light: {}, dark: {} } })).toEqual({ bg: 'var(--bg)', fg: 'var(--text)', accent: 'var(--brand)' })
  })

  it('tells the admin the site name changes with the active theme', () => {
    const night = theme({ code: 'night', name: '夜海', is_builtin: false, is_active: false, branding: { site_name: '夜海加速' } })
    expect(activateNotice(theme(), night)).toBe('门户所有用户刷新页面后看到「夜海」的配色。站点名称会从「Pandora」变成「夜海加速」：门户标题、邮件里的站点名与发件人名都随之改变（发件人名单独设置过的除外）。')
    expect(activateNotice(theme(), theme({ name: '同名' }))).toContain('站点名称保持「Pandora」')
    expect(siteName(theme({ branding: { site_name: '  ' } }))).toBeNull()
  })
})
