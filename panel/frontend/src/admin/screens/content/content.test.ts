import { describe, expect, it } from 'vitest'
import {
  annActions,
  annBody,
  annFieldErrors,
  annFilterItems,
  annFormFrom,
  annMeta,
  annTargetLabel,
  annWhen,
  articleRows,
  audienceChanged,
  droppedMessage,
  emptyAnnForm,
  emptyKbForm,
  fromLocalInput,
  groupArticles,
  kbBody,
  kbFormFrom,
  knownCategories,
  platformsLabel,
  slotDirty,
  timezoneOptions,
  toLocalInput,
  utcOffsetLabel,
  validateAnn,
  validateKb,
  visibleAnns,
} from './logic'
import { announcementsResponse, pageSchema, slotSaved, type Announcement, type Page } from './schemas'

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

describe('time inputs', () => {
  it('round-trips local inputs and keeps untouched originals verbatim', () => {
    const iso = '2026-09-20T02:00:05Z'
    const local = toLocalInput(iso)
    expect(local).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/)
    expect(fromLocalInput(local, iso)).toBe(iso)
    expect(new Date(fromLocalInput(local)).getTime()).toBe(new Date(iso).getTime() - 5000)
    expect(fromLocalInput('')).toBe('')
    expect(toLocalInput(null)).toBe('')
  })
})

describe('announcements', () => {
  it('labels targets and times like the design', () => {
    expect(annTargetLabel(announcement())).toBe('全部用户')
    expect(annTargetLabel(announcement({ plan_targets: [{ id: U(2), name: '专业版', status: 'active' }], user_group_targets: [{ id: U(3), name: 'VIP' }] }))).toBe('专业版、VIP')
    expect(annWhen(announcement({ status: 'draft' }))).toBe('未发布')
    expect(annWhen(announcement())).toMatch(/^\d{2}-\d{2} 发布$/)
    expect(annWhen(announcement({ status: 'scheduled' }))).toMatch(/^\d{2}-\d{2} \d{2}:\d{2} 发布$/)
    expect(annWhen(announcement({ status: 'withdrawn', publish_at: null }))).toMatch(/^\d{2}-\d{2} 发布$/)
  })

  it('counts filter tabs and puts pinned announcements first', () => {
    const rows = [
      announcement({ id: U(1), status: 'published' }),
      announcement({ id: U(2), status: 'draft' }),
      announcement({ id: U(3), status: 'published', pinned: true }),
      announcement({ id: U(4), status: 'withdrawn' }),
    ]
    expect(annFilterItems(rows)).toEqual([
      { value: 'all', label: '全部', count: 4 },
      { value: 'published', label: '已发布', count: 2 },
      { value: 'scheduled', label: '定时', count: 0 },
      { value: 'draft', label: '草稿', count: 1 },
      { value: 'withdrawn', label: '已撤回', count: 1 },
    ])
    // 置顶在前，其余保持原顺序；别的状态不混进来
    expect(visibleAnns(rows, 'all').map((a) => a.id)).toEqual([U(3), U(1), U(2), U(4)])
    expect(visibleAnns(rows, 'published').map((a) => a.id)).toEqual([U(3), U(1)])
    expect(visibleAnns(rows, 'draft').map((a) => a.id)).toEqual([U(2)])
    expect(visibleAnns(rows, 'scheduled')).toEqual([])
  })

  it('writes the modal head meta per status', () => {
    expect(annMeta(null)).toBe('保存后出现在列表里')
    expect(annMeta(announcement({ status: 'scheduled' }))).toMatch(/^将于 \d{2}-\d{2} \d{2}:\d{2} 发布$/)
    expect(annMeta(announcement())).toMatch(/^发布于 \d{2}-\d{2} \d{2}:\d{2}$/)
    expect(annMeta(announcement({ status: 'draft', publish_at: null }))).toBe('保存后出现在列表里')
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

  it('filters and sorts rows for the table', () => {
    const articles = groupArticles(rows)
    expect(articleRows(articles).map((a) => a.slug)).toEqual(['clash', 'refund', 'misc'])
    expect(articleRows(articles, 'CLASH 新').map((a) => a.slug)).toEqual(['clash'])
    expect(articleRows(articles, 'refund')[0]!.slug).toBe('refund')
    expect(articleRows(articles, '旧稿')).toEqual([])
    expect(knownCategories(articles)).toEqual(['客户端教程', '账单'])
  })

  it('labels platforms for the list column', () => {
    expect(platformsLabel([])).toBe('全部平台')
    expect(platformsLabel(['ios', 'android', 'web'])).toBe('网页 / Android / iOS')
    expect(platformsLabel(['custom'])).toBe('custom')
  })

  it('validates like normalizePublishInput', () => {
    const form = { ...emptyKbForm('kb_article'), slug: 'Bad Slug', title: '标', body: '太短' }
    expect(Object.keys(validateKb(form)).sort()).toEqual(['body', 'slug', 'title'])
    const ok = { ...form, slug: 'clash-verge', title: '标题', body: '正文至少十个字才可以保存' }
    expect(validateKb(ok)).toEqual({})
    expect(validateKb(ok, ['clash-verge']).slug).toBe('这个标识已被其他文章使用')
    expect(validateKb({ ...ok, locale: 'zh_cn' }).locale).toBe('语言格式不正确')
  })

  it('builds the publish body, carries hidden fields from original and omits review when absent', () => {
    const source = page({ target_platforms: ['ios', 'android'], min_client_version: '1.2', max_client_version: '2.0', summary: '旧摘要', target_plan_ids: [U(3)], review_due_at: '2026-12-01T00:00:00Z', body: '正文正文正文正文正文' })
    const form = kbFormFrom(source)
    const body = kbBody(form, 'published', 3, source)
    expect(body).toMatchObject({ slug: 'clash', summary: '旧摘要', target_platforms: ['android', 'ios'], min_client_version: '1.2', max_client_version: '2.0', target_plan_ids: [U(3)], status: 'published', review_due_at: '2026-12-01T00:00:00Z', expected_latest_version: 3 })
    const noReview = page({ body: '正文正文正文正文正文' })
    const fromScratch = kbBody(kbFormFrom(noReview), 'draft', 0, null)
    expect(fromScratch.summary).toBe('')
    expect(fromScratch.min_client_version).toBe('')
    expect('review_due_at' in fromScratch).toBe(false)
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

describe('slots', () => {
  it('saves only real changes and reports dropped fragments', () => {
    expect(slotDirty(undefined, { content: 'a' })).toBe(false)
    expect(slotDirty('a', { content: 'a' })).toBe(false)
    expect(slotDirty('b', { content: 'a' })).toBe(true)
    expect(droppedMessage(null)).toBeNull()
    expect(droppedMessage([])).toBeNull()
    expect(droppedMessage(['<script> 元素已删除', '事件属性'])).toBe('部分内容已被过滤：<script> 元素已删除；事件属性')
    expect(slotSaved.parse({ saved: true, dropped: null }).dropped).toBeNull()
  })
})
