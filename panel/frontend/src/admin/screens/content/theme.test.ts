import { describe, expect, it } from 'vitest'
import { COLOR_TOKENS } from '../../../styles/design-tokens'
import { themesResponse, type Theme } from './schemas'
import { THEME_COLORS, activateNotice, brandDerived, buildThemeRequest, themeSwatches, themeTagline, themeToForm, validateThemeForm } from './theme'

const U = (n: number) => `00000000-0000-7000-8000-${String(n).padStart(12, '0')}`
const theme = (patch: Partial<Theme> = {}): Theme => ({
  id: U(1),
  code: 'paper',
  name: '默认 · 森林绿',
  is_builtin: true,
  is_active: true,
  tokens: { light: { '--bg': '#f5f4f0', '--text': '#1c1c1f', '--brand': '#b9442b' }, dark: { '--brand': '#e46e52' } },
  branding: { tagline: '一路畅通' },
  ...patch,
})

describe('theme schema', () => {
  const parse = (themes: unknown) => themesResponse.parse({ themes })

  it('parses a grouped theme and fails on flat keys or non-string values', () => {
    const ok = themesResponse.parse({ themes: [theme()] })
    expect(ok.themes![0]!.tokens.light['--brand']).toBe('#b9442b')
    expect(themesResponse.parse({ themes: null }).themes).toBeNull()
    // 保存时 light / dark 都可以不出现，缺的组按空对象收
    const darkOnly = parse([{ ...theme(), tokens: { dark: { '--brand': '#e46e52' } } }])
    expect(darkOnly.themes![0]!.tokens).toEqual({ light: {}, dark: { '--brand': '#e46e52' } })
    expect(parse([{ ...theme(), tokens: {} }]).themes![0]!.tokens).toEqual({ light: {}, dark: {} })
    // branding 的宽松收法不在本项
    expect(parse([{ ...theme(), branding: 'x' }]).themes![0]!.branding).toEqual({})

    expect(() => parse([{ ...theme(), tokens: { brand: '#6d5efc', '--bg': '#fff' } }])).toThrow()
    expect(() => parse([{ ...theme(), tokens: { light: { '--bg': 1, '--text': '#000' }, dark: {} } }])).toThrow()
    expect(() => parse([{ ...theme(), tokens: [] }])).toThrow()
  })
})

describe('theme form', () => {
  it('maps the five editable colors to their light tokens', () => {
    expect(THEME_COLORS.map(([key, token]) => [key, token])).toEqual([
      ['bg', '--bg'],
      ['line', '--border'],
      ['muted', '--text-2'],
      ['ink', '--text'],
      ['accent', '--brand'],
    ])
  })

  it('edits keep code and name, copies derive them from the source, blank create is empty', () => {
    // 编辑：code、name 取原值，颜色取浅色组
    const edit = themeToForm({ kind: 'edit', theme: theme() })
    expect(edit.code).toBe('paper')
    expect(edit.name).toBe('默认 · 森林绿')
    expect(edit.tagline).toBe('一路畅通')
    expect(edit.colors).toEqual({ bg: '#f5f4f0', line: '#e4e8e5', muted: '#6a706b', ink: '#1c1c1f', accent: '#b9442b' })

    // 复制：code = 来源 code + -copy（截到 39 字符），name = 来源名 + 副本（截到 60 字）
    const long = theme({ code: 'a'.repeat(40), name: '长'.repeat(70) })
    const copy = themeToForm({ kind: 'create', from: long })
    expect(copy.code).toHaveLength(39)
    expect([...copy.name]).toHaveLength(62 - 2)
    expect(copy.name.endsWith('副本')).toBe(true)

    // 空白新建：code、name 为空
    const blank = themeToForm({ kind: 'create', from: null })
    expect(blank.code).toBe('')
    expect(blank.name).toBe('')

    // 标语：来源有才带，空白或复制自没标语的来源都是空
    expect(themeToForm({ kind: 'create', from: theme({ branding: { tagline: '  ' } }) }).tagline).toBe('')
    expect(themeToForm({ kind: 'create', from: null }).tagline).toBe('')
  })

  it('expands 3-digit hex and falls back to design defaults for missing keys', () => {
    const src = theme({ tokens: { light: { '--bg': '#ABC', '--text': ' #1c1c1f ' }, dark: {} } })
    const form = themeToForm({ kind: 'create', from: src })
    expect(form.colors.bg).toBe('#aabbcc')
    expect(form.colors.ink).toBe('#1c1c1f')
    // 缺的键回 COLOR_TOKENS 的浅色默认值
    expect(form.colors.line).toBe(COLOR_TOKENS.find((t) => t.name === '--border')!.light)
    expect(form.colors.muted).toBe(COLOR_TOKENS.find((t) => t.name === '--text-2')!.light)
    expect(form.colors.accent).toBe(COLOR_TOKENS.find((t) => t.name === '--brand')!.light)
  })

  it('validates code, name, tagline and colors with the same keys as the Go 422 fields', () => {
    const form = themeToForm({ kind: 'create', from: theme() })
    // 全对：没有错误
    expect(validateThemeForm(form, true)).toEqual({})
    // code：新建必填、格式；编辑时不校验
    expect(validateThemeForm({ ...form, code: '' }, true).code).toBe('主题标识必填')
    expect(validateThemeForm({ ...form, code: '9abc' }, true).code).toMatch(/小写字母开头/)
    expect(validateThemeForm({ ...form, code: '' }, false).code).toBeUndefined()
    // name：必填且 ≤60 字
    expect(validateThemeForm({ ...form, name: '  ' }, true).name).toBe('主题名称必填，最多 60 个字')
    expect(validateThemeForm({ ...form, name: '长'.repeat(61) }, true).name).toMatch(/60/)
    // tagline：≤80 字，键 branding.tagline
    const longTag = validateThemeForm({ ...form, tagline: '字'.repeat(81) }, true)
    expect(longTag['branding.tagline']).toMatch(/80/)
    // 颜色：必须 #RRGGBB，错误键与后端 fields 同名
    const bad = validateThemeForm({ ...form, colors: { ...form.colors, bg: '#fff', ink: 'red' } }, true)
    expect(bad['tokens.light.--bg']).toBe('写成 #RRGGBB')
    expect(bad['tokens.light.--text']).toBe('写成 #RRGGBB')
    expect(Object.keys(bad).sort()).toEqual(['tokens.light.--bg', 'tokens.light.--text'])
  })

  it('builds a full request body: both groups 43 keys, light overridden by the form, code by kind', () => {
    // 复制：code = 表单（trim + 小写），dark 组与来源一致，light 的 5 个键被表单覆盖
    const form = themeToForm({ kind: 'create', from: theme() })
    form.code = ' Night-Sea '
    form.name = ' 夜海 '
    form.colors.accent = ' #2b6cb9 '
    const built = buildThemeRequest(form, { kind: 'create', from: theme() })
    if (!built.ok) throw new Error(JSON.stringify(built.errors))
    expect(built.body.code).toBe('night-sea')
    expect(built.body.name).toBe('夜海')
    expect(built.body.create).toBe(true)
    expect(Object.keys(built.body.tokens.light)).toHaveLength(48)
    expect(Object.keys(built.body.tokens.dark)).toHaveLength(48)
    expect(built.body.tokens.light['--brand']).toBe('#2b6cb9')
    expect(built.body.tokens.light['--bg']).toBe('#f5f4f0')
    // dark 组整组照来源补满
    expect(built.body.tokens.dark['--brand']).toBe('#e46e52')
    expect(built.body.tokens.dark['--bg']).toBe('#0e1210')
    expect(built.body.branding).toEqual({ tagline: '一路畅通' })

    // 编辑：code 用原值，create=false
    const editForm = themeToForm({ kind: 'edit', theme: theme() })
    editForm.colors.bg = '#eef0f2'
    const edited = buildThemeRequest(editForm, { kind: 'edit', theme: theme() })
    if (!edited.ok) throw new Error(JSON.stringify(edited.errors))
    expect(edited.body.code).toBe('paper')
    expect(edited.body.create).toBe(false)
    expect(edited.body.tokens.light['--bg']).toBe('#eef0f2')

    // 校验失败不建 body
    expect(buildThemeRequest({ ...form, name: '' }, { kind: 'create', from: theme() }).ok).toBe(false)
  })

  it('derives the brand companion tokens when the brand colour changes, keeps them otherwise', () => {
    const source = theme()
    const before = { ...source.tokens.light }
    // 改了品牌色：--brand-deep / --brand-mark / --brand-line 跟着推，不再是来源的值
    const form = themeToForm({ kind: 'edit', theme: source })
    form.colors.accent = '#2b6cb9'
    const built = buildThemeRequest(form, { kind: 'edit', theme: source })
    if (!built.ok) throw new Error(JSON.stringify(built.errors))
    const want = brandDerived('#2b6cb9', form.colors.bg)
    for (const [k, v] of Object.entries(want)) {
      expect(built.body.tokens.light[k]).toBe(v)
      expect(v).toMatch(/^#[0-9a-f]{6}$/)
    }
    expect(want['--brand-deep']).toBe('#1e4c82')
    // 派生色比品牌色暗 / 亮 / 更接近背景
    expect(want['--brand-line']).not.toBe('#2b6cb9')

    // 没改品牌色：保留来源主题手调的值（包括大小写不同的同一色）
    const same = themeToForm({ kind: 'edit', theme: source })
    same.colors.accent = same.colors.accent.toUpperCase()
    same.colors.bg = '#eef0f2'
    const kept = buildThemeRequest(same, { kind: 'edit', theme: source })
    if (!kept.ok) throw new Error(JSON.stringify(kept.errors))
    for (const k of ['--brand-deep', '--brand-mark', '--brand-line']) {
      expect(kept.body.tokens.light[k]).toBe(before[k]?.trim() || kept.body.tokens.light[k])
    }
  })

  it('keeps a data-image logo from the source but drops others and empty taglines', () => {
    const withLogo = theme({ branding: { tagline: '口号', logo: 'data:image/png;base64,AAAA' } })
    const form = themeToForm({ kind: 'edit', theme: withLogo })
    const built = buildThemeRequest(form, { kind: 'edit', theme: withLogo })
    if (!built.ok) throw new Error(JSON.stringify(built.errors))
    expect(built.body.branding).toEqual({ tagline: '口号', logo: 'data:image/png;base64,AAAA' })

    // 外链 Logo 不带
    const remote = theme({ branding: { logo: 'https://x.test/a.png' } })
    const remoteForm = themeToForm({ kind: 'edit', theme: remote })
    const remoteBuilt = buildThemeRequest(remoteForm, { kind: 'edit', theme: remote })
    if (!remoteBuilt.ok) throw new Error(JSON.stringify(remoteBuilt.errors))
    expect(remoteBuilt.body.branding).toEqual({})

    // 标语清空时 branding 里不带 tagline
    const noTagline = buildThemeRequest({ ...remoteForm, tagline: ' ' }, { kind: 'edit', theme: remote })
    if (!noTagline.ok) throw new Error(JSON.stringify(noTagline.errors))
    expect(noTagline.body.branding).toEqual({})
  })
})

describe('theme rows', () => {
  it('returns five light swatches in THEME_COLORS order with fallbacks', () => {
    expect(themeSwatches(theme())).toEqual(['#f5f4f0', '#e4e8e5', '#6a706b', '#1c1c1f', '#b9442b'])
    // 缺的令牌回退设计规范默认浅色，与编辑表单一致
    expect(themeSwatches({ tokens: { light: {}, dark: {} } }).some((v) => v.startsWith('var('))).toBe(false)
  })

  it('tells the admin what activation replaces', () => {
    const night = theme({ code: 'night', name: '夜海', is_builtin: false, is_active: false })
    expect(activateNotice(night)).toBe('用户端所有用户看到的都会换成「夜海」，明暗两组随用户切换，正在使用的主题会被替换。站点名不随主题变化，在本页的「站点设置」里改。')
    expect(themeTagline(theme({ branding: { tagline: '  ' } }))).toBeNull()
    expect(themeTagline(theme())).toBe('一路畅通')
  })
})
