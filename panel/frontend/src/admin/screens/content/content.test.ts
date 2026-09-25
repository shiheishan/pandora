/**
 * [INPUT]: 依赖 vitest，依赖 ../../../core/api 的 ApiError，依赖 ./logic，依赖 ./schemas
 * [OUTPUT]: 无（测试文件）
 * [POS]: admin/screens/content 纯函数层与 schema 边界的单元测试：幂等键丢弃口径、时间输入互转、公告文字 / 校验 / 请求体 / 按钮取舍、知识库聚合 / 分组 / 校验 / 请求体 / 受众改动、时区选项、插槽脏判断与过滤提示、主题预览；界面交互在浏览器里对 dev 假后端验收
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it } from 'vitest'
import { ApiError } from '../../../core/api'
import {
  annActions,
  annBody,
  annFieldErrors,
  annFormFrom,
  annTargetLabel,
  annWhen,
  audienceChanged,
  compareVersion,
  droppedMessage,
  dropsIntent,
  emptyAnnForm,
  emptyKbForm,
  fromDateInput,
  fromLocalInput,
  groupArticles,
  groupByCategory,
  kbBody,
  kbFormFrom,
  knownCategories,
  slotDirty,
  themePreview,
  timezoneOptions,
  toLocalInput,
  utcOffsetLabel,
  validateAnn,
  validateKb,
} from './logic'
import { announcementsResponse, pageSchema, slotSaved, themesResponse, type Announcement, type Page } from './schemas'

const U = (n: number) => `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`

const announcement = (patch: Partial<Announcement> = {}): Announcement => ({
  id: U(1),
  title: '维护通知',
  body: '今晚维护',
  severity: 'info',
  pinned: false,
  status: 'published',
  version: 3,
  target_plan_ids: [],
  plan_targets: [],
  target_user_group_ids: [],
  user_group_targets: [],
  publish_at: '2026-09-20T02:00:05Z',
  expires_at: null,
  created_at: '2026-09-19T00:00:00Z',
  ...patch,
})

const page = (patch: Partial<Page> = {}): Page => ({
  id: U(10),
  slug: 'clash',
  kind: 'kb_article',
  version: 1,
  title: 'Clash',
  locale: 'zh-CN',
  sanitizer_version: 'plain-v1',
  target_platforms: [],
  target_plan_ids: [],
  visibility: 'authenticated',
  status: 'published',
  created_at: '2026-09-01T00:00:00Z',
  updated_at: '2026-09-01T00:00:00Z',
  latest_version: 1,
  ...patch,
})

describe('dropsIntent', () => {
  it('drops the key on 4xx business rejections only', () => {
    expect(dropsIntent(new ApiError({ status: 409, code: 'conflict', message: 'x' }))).toBe(true)
    expect(dropsIntent(new ApiError({ status: 422, code: 'validation_failed', message: 'x' }))).toBe(true)
    expect(dropsIntent(new ApiError({ status: 403, code: 'reauth_required', message: 'x' }))).toBe(false)
    expect(dropsIntent(new ApiError({ status: 0, code: 'network_error', message: 'x' }))).toBe(false)
    expect(dropsIntent(new ApiError({ status: 503, code: 'service_unavailable', message: 'x' }))).toBe(false)
    expect(dropsIntent(new Error('x'))).toBe(false)
  })
})

describe('time inputs', () => {
  it('round-trips local inputs and keeps untouched originals verbatim', () => {
    const iso = '2026-09-20T02:00:05Z'
    const local = toLocalInput(iso)
    expect(local).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/)
    expect(fromLocalInput(local, iso)).toBe(iso)
    expect(new Date(fromLocalInput(local)).getTime()).toBe(new Date(iso).getTime() - 5000)
    expect(fromLocalInput('')).toBe('')
    expect(toLocalInput(null)).toBe('')
    expect(fromDateInput('2026-10-01')).toBe(new Date('2026-10-01T00:00').toISOString())
    expect(fromDateInput('')).toBe('')
  })
})

describe('announcements', () => {
  it('labels targets and times like the design', () => {
    expect(annTargetLabel(announcement())).toBe('全部用户')
    expect(annTargetLabel(announcement({ plan_targets: [{ id: U(2), name: '专业版', status: 'active' }], user_group_targets: [{ id: U(3), name: 'VIP' }] }))).toBe('专业版、VIP')
    expect(annWhen(announcement({ status: 'draft' }))).toBe('草稿')
    expect(annWhen(announcement())).toMatch(/^\d{2}-\d{2}$/)
    expect(annWhen(announcement({ status: 'scheduled' }))).toMatch(/^\d{2}-\d{2} \d{2}:\d{2}$/)
    expect(annWhen(announcement({ status: 'withdrawn', publish_at: null }))).toMatch(/^\d{2}-\d{2}$/)
  })

  it('validates with the Go limits and the expiry rule', () => {
    const now = new Date('2026-09-24T00:00:00')
    expect(validateAnn({ ...emptyAnnForm(), title: '一', body: '正' }, now)).toEqual({ title: expect.any(String), body: expect.any(String) })
    const ok = { ...emptyAnnForm(), title: '标题', body: '正文' }
    expect(validateAnn(ok, now)).toEqual({})
    expect(validateAnn({ ...ok, expiresAt: '2026-09-23T10:00' }, now).expiresAt).toBe('下线时间必须晚于现在')
    expect(validateAnn({ ...ok, publishAt: '2026-10-01T10:00', expiresAt: '2026-10-01T09:00' }, now).expiresAt).toBe('下线时间必须晚于发布时间')
    expect(validateAnn({ ...ok, publishAt: '2026-10-01T10:00', expiresAt: '2026-10-02T09:00' }, now)).toEqual({})
  })

  it('builds a full-overwrite body that sends untouched times back verbatim', () => {
    const a = announcement({ target_plan_ids: [U(5), U(4)] })
    const body = annBody({ ...annFormFrom(a), title: '  新标题  ' }, true, a)
    expect(body).toEqual({
      title: '新标题',
      body: '今晚维护',
      severity: 'info',
      pinned: false,
      target_plan_ids: [U(4), U(5)],
      target_user_group_ids: [],
      publish_at: '2026-09-20T02:00:05Z',
      expires_at: '',
      publish: true,
      expected_version: 3,
    })
    expect(annBody(emptyAnnForm(), false, null).expected_version).toBe(0)
    expect(annFieldErrors({ target_user_group_ids: '坏', title: '短' })).toEqual({ targets: '坏', title: '短' })
  })

  it('offers the buttons each status allows', () => {
    const now = new Date('2026-09-24T00:00:00')
    expect(annActions('new', '', now)).toMatchObject({ primary: '发布', draft: true, withdraw: false })
    expect(annActions('draft', '2026-10-01T10:00', now)).toMatchObject({ primary: '定时发布', draft: true })
    expect(annActions('scheduled', '2026-10-01T10:00', now)).toMatchObject({ primary: '保存修改', draft: true, withdraw: true, scheduleLocked: false })
    expect(annActions('scheduled', '', now).primary).toBe('立即发布')
    expect(annActions('published', '2026-09-01T10:00', now)).toMatchObject({ primary: '保存修改', draft: false, withdraw: true, scheduleLocked: true })
    expect(annActions('withdrawn', '', now)).toMatchObject({ primary: null, draft: false, withdraw: false, readOnly: true })
  })

  it('accepts the Go list shape and rejects unknown statuses', () => {
    const raw = { announcements: [{ ...announcement(), publish_at: null }], plans: [], user_groups: [] }
    expect(announcementsResponse.parse(raw).announcements).toHaveLength(1)
    expect(() => announcementsResponse.parse({ ...raw, announcements: [{ ...announcement(), status: 'live' }] })).toThrow()
    expect(() => announcementsResponse.parse({ announcements: [], plans: [] })).toThrow()
  })
})

describe('knowledge base', () => {
  const rows: Page[] = [
    page({ id: U(11), slug: 'clash', version: 3, status: 'draft', latest_version: 3, title: 'Clash 新', category: '客户端教程' }),
    page({ id: U(12), slug: 'clash', version: 2, status: 'published', latest_version: 3, category: '客户端教程' }),
    page({ id: U(13), slug: 'clash', version: 1, status: 'archived', latest_version: 3, category: '客户端教程' }),
    page({ id: U(14), slug: 'refund', version: 2, status: 'archived', latest_version: 2, title: '退款', category: '账单' }),
    page({ id: U(15), slug: 'refund', version: 1, status: 'archived', latest_version: 2, title: '退款', category: '账单' }),
    page({ id: U(16), slug: 'misc', version: 1, latest_version: 1, title: 'A 杂项' }),
  ]

  it('folds versions into articles', () => {
    const [clash, refund, misc] = groupArticles(rows)
    expect(clash).toMatchObject({ slug: 'clash', archived: false })
    expect(clash!.latest.id).toBe(U(11))
    expect(clash!.published?.id).toBe(U(12))
    expect(clash!.rows.map((r) => r.version)).toEqual([3, 2, 1])
    expect(refund).toMatchObject({ archived: true, published: null })
    expect(misc!.latest.id).toBe(U(16))
  })

  it('groups by category with uncategorized last and searches the latest title and slug', () => {
    const articles = groupArticles(rows)
    expect(groupByCategory(articles).map((g) => g.cat)).toEqual(['客户端教程', '账单', '未分类'])
    expect(groupByCategory(articles, 'CLASH 新').map((g) => g.cat)).toEqual(['客户端教程'])
    expect(groupByCategory(articles, 'refund')[0]!.items[0]!.slug).toBe('refund')
    expect(groupByCategory(articles, '旧稿')).toEqual([])
    expect(knownCategories(articles)).toEqual(['客户端教程', '账单'])
  })

  it('validates like normalizePublishInput', () => {
    const form = { ...emptyKbForm('kb_article'), slug: 'Bad Slug', title: '标', body: '太短' }
    expect(Object.keys(validateKb(form)).sort()).toEqual(['body', 'slug', 'title'])
    const ok = { ...form, slug: 'clash-verge', title: '标题', body: '正文至少十个字才可以保存' }
    expect(validateKb(ok)).toEqual({})
    expect(validateKb(ok, ['clash-verge']).slug).toBe('这个标识已被其他文章使用')
    expect(validateKb({ ...ok, minClient: 'v2.0', maxClient: '1.9.9' }).max_client_version).toBe('最高客户端版本不能低于最低版本')
    expect(validateKb({ ...ok, minClient: 'x' }).min_client_version).toBe('最低客户端版本格式不正确')
    expect(validateKb({ ...ok, locale: 'zh_cn' }).locale).toBe('语言格式不正确')
    expect(compareVersion('v1.10', '1.9')).toBe(1)
    expect(compareVersion('1', '1.0.0')).toBe(0)
  })

  it('builds the publish body, carries the audience and omits an empty review date', () => {
    const source = page({ target_platforms: ['ios', 'android'], min_client_version: '1.2', target_plan_ids: [U(3)], review_due_at: '2026-12-01T00:00:00Z', body: '正文正文正文正文正文' })
    const form = kbFormFrom(source)
    const body = kbBody(form, 'published', 3, source)
    expect(body).toMatchObject({ slug: 'clash', target_platforms: ['android', 'ios'], min_client_version: '1.2', max_client_version: '', target_plan_ids: [U(3)], status: 'published', review_due_at: '2026-12-01T00:00:00Z', expected_latest_version: 3 })
    expect('review_due_at' in kbBody({ ...form, reviewDue: '' }, 'draft', 3)).toBe(false)
    expect(audienceChanged(form, source)).toBe(false)
    expect(audienceChanged({ ...form, platforms: ['ios'] }, source)).toBe(true)
    expect(audienceChanged({ ...form, visibility: 'internal' }, source)).toBe(true)
    expect(audienceChanged({ ...form, title: '改标题不算' }, source)).toBe(false)
  })

  it('accepts omitempty fields as absent and rejects unknown kinds', () => {
    expect(pageSchema.parse(page()).category).toBeUndefined()
    expect(() => pageSchema.parse({ ...page(), kind: 'faq' })).toThrow()
  })
})

describe('site timezone', () => {
  it('pins Shanghai first and keeps an unlisted current value', () => {
    const at = new Date('2026-01-15T00:00:00Z')
    expect(utcOffsetLabel('Asia/Shanghai', at)).toBe('UTC+8')
    expect(utcOffsetLabel('UTC', at)).toBe('UTC')
    expect(utcOffsetLabel('America/New_York', at)).toBe('UTC−5')
    expect(utcOffsetLabel('Not/AZone', at)).toBeNull()
    const opts = timezoneOptions('Asia/Shanghai', at)
    expect(opts[0]).toEqual({ value: 'Asia/Shanghai', label: '中国 · 上海（Asia/Shanghai） UTC+8' })
    const extra = timezoneOptions('Asia/Kathmandu', at)
    expect(extra[0]).toEqual({ value: 'Asia/Kathmandu', label: 'Asia/Kathmandu UTC+5:45' })
    expect(extra).toHaveLength(opts.length + 1)
  })
})

describe('slots and theme', () => {
  it('saves only real changes and reports dropped fragments', () => {
    expect(slotDirty(undefined, { content: 'a' })).toBe(false)
    expect(slotDirty('a', { content: 'a' })).toBe(false)
    expect(slotDirty('b', { content: 'a' })).toBe(true)
    expect(droppedMessage(null)).toBeNull()
    expect(droppedMessage([])).toBeNull()
    expect(droppedMessage(['<script> 元素已删除', '事件属性'])).toBe('部分内容已被过滤：<script> 元素已删除；事件属性')
    expect(slotSaved.parse({ saved: true, dropped: null }).dropped).toBeNull()
  })

  it('previews the light tokens and parses the R19 theme shape', () => {
    const parsed = themesResponse.parse({ themes: [{ id: U(20), code: 'paper', name: '默认 · 纸白', is_builtin: true, is_active: true, tokens: { light: { '--bg': '#f5f4f0', '--text': '#1c1c1f', '--brand': '#b9442b' }, dark: {} }, branding: { site_name: 'Pandora' }, custom_css: '' }] })
    expect(themePreview(parsed.themes![0]!)).toEqual({ bg: '#f5f4f0', fg: '#1c1c1f', accent: '#b9442b' })
    expect(themePreview({ tokens: { light: {}, dark: {} } })).toEqual({ bg: 'var(--bg)', fg: 'var(--text)', accent: 'var(--brand)' })
    expect(themesResponse.parse({ themes: null }).themes).toBeNull()
    expect(() => themesResponse.parse({ themes: [{ ...parsed.themes![0]!, tokens: { '--bg': '#fff' } }] })).toThrow()
  })
})
