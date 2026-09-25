/**
 * [INPUT]: 依赖 vitest，依赖 ./mock-helpers，依赖 ../dev/mock-api 的 MOCK_ACCOUNTS，依赖 ../src/admin/screens/content/schemas 的公告 / 内容页 / 主题 / 插槽 / 站点时区 schema
 * [OUTPUT]: 对外提供内容与外观（后台-08）假接口的测试
 * [POS]: tests 的内容假后端守卫：对只读账号整块 404、公告草稿 → 定时 → 发布 → 撤回的状态机与版本冲突、知识库保存新版本归档同受众旧发布版与重复归档、内置主题 43 键、插槽净化与空内容 dropped 为 null、站点时区校验
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { announcementsResponse, annSaved, pageArchived, pageResponse, pageSaved, pagesResponse, siteSettingsSchema, slotSaved, slotsResponse, themesResponse } from '../src/admin/screens/content/schemas'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · admin content', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
  })
  afterAll(() => close(server))

  const call = (method: string, path: string, body?: unknown, key?: string) => mockFetch(base, auth, method, path, body, key)
  const anns = async () => announcementsResponse.parse(await (await call('GET', '/v1/announcements')).json())
  const draft = { title: '测试公告', body: '测试正文', severity: 'notice', pinned: false, target_plan_ids: [], target_user_group_ids: [], publish_at: '', expires_at: '', publish: false, expected_version: 0 }

  it('hides the content module from the viewer', async () => {
    const res = await fetch(`${base}/v1/auth/login`, { method: 'POST', body: JSON.stringify(MOCK_ACCOUNTS.viewer) })
    const viewer = { Authorization: `Bearer ${((await res.json()) as { access_token: string }).access_token}` }
    for (const path of ['/v1/announcements', '/v1/content-pages', '/v1/themes', '/v1/slots', '/v1/settings/site']) expect((await fetch(`${base}${path}`, { headers: viewer })).status, path).toBe(404)
  })

  it('walks an announcement from draft to scheduled, published and withdrawn', async () => {
    const data = await anns()
    expect(data.announcements.map((a) => a.status)).toEqual(expect.arrayContaining(['draft', 'scheduled', 'published', 'withdrawn']))
    expect(data.user_groups.length).toBeGreaterThan(0)
    expect((await call('POST', '/v1/announcements', { ...draft, audience: 'all' }, 'a-0')).status).toBe(400)
    expect(await (await call('POST', '/v1/announcements', { ...draft, title: '短' }, 'a-1')).json()).toMatchObject({ error: { fields: { title: expect.any(String) } } })
    expect(await (await call('POST', '/v1/announcements', { ...draft, publish_at: '2026-10-01 10:00' }, 'a-2')).json()).toMatchObject({ error: { message: '时间必须是带时区的 RFC3339 格式' } })
    expect(await (await call('POST', '/v1/announcements', { ...draft, target_user_group_ids: ['00000000-0000-4000-8000-000000000000'] }, 'a-3')).json()).toMatchObject({ error: { fields: { target_user_group_ids: expect.any(String) } } })

    // 编辑是全量覆盖：之后每次都要带上用户组
    const mine = { ...draft, target_user_group_ids: [data.user_groups[0]!.id] }
    const created = annSaved.parse(await (await call('POST', '/v1/announcements', mine, 'a-4')).json())
    expect(created).toMatchObject({ status: 'draft', version: 1 })
    const future = new Date(Date.now() + 86_400_000).toISOString()
    const scheduled = annSaved.parse(await (await call('POST', `/v1/announcements/${created.id}`, { ...mine, publish: true, publish_at: future, expected_version: 1 }, 'a-5')).json())
    expect(scheduled).toMatchObject({ status: 'scheduled', version: 2 })
    expect((await call('POST', `/v1/announcements/${created.id}`, { ...mine, publish: true, expected_version: 1 }, 'a-6')).status).toBe(409)
    const published = annSaved.parse(await (await call('POST', `/v1/announcements/${created.id}`, { ...mine, publish: true, expected_version: 2 }, 'a-7')).json())
    expect(published.status).toBe('published')
    const row = (await anns()).announcements.find((a) => a.id === created.id)!
    expect(row).toMatchObject({ user_group_targets: [{ id: data.user_groups[0]!.id }], version: 3 })
    expect(row.publish_at).not.toBeNull()
    expect(await (await call('POST', `/v1/announcements/${created.id}`, { ...mine, expected_version: 3 }, 'a-8')).json()).toMatchObject({ error: { code: 'conflict', message: '已发布公告只能保持发布状态；如需下线请使用撤回' } })
    expect((await call('POST', `/v1/announcements/${created.id}/withdraw`, { expected_version: 3 }, 'a-9')).status).toBe(200)
    expect(await (await call('POST', `/v1/announcements/${created.id}/withdraw`, { expected_version: 4 }, 'a-10')).json()).toMatchObject({ error: { message: '这条公告已经撤回过了' } })
    expect(await (await call('POST', `/v1/announcements/${created.id}`, { ...mine, publish: true, expected_version: 4 }, 'a-11')).json()).toMatchObject({ error: { message: '已撤回公告不可重新编辑，请新建公告' } })
    expect((await call('POST', '/v1/announcements/not-a-uuid', { ...draft, expected_version: 1 }, 'a-12')).status).toBe(404)
  })

  it('saves knowledge-base versions, archiving the same-audience published one', async () => {
    const list = async () => pagesResponse.parse(await (await call('GET', '/v1/content-pages?kind=kb_article&limit=500')).json()).pages
    const rows = (await list()).filter((p) => p.slug === 'gift-card')
    expect(rows.every((p) => p.created_by_name && p.latest_version === 2 && p.body === undefined)).toBe(true)
    const published = rows.find((p) => p.status === 'published')!
    const detail = pageResponse.parse(await (await call('GET', `/v1/content-pages/${published.id}`)).json()).page
    expect(detail.body).toContain('礼品卡')
    expect(detail.created_by_name).toBeUndefined()
    const body = { slug: 'gift-card', kind: 'kb_article', category: '账单与支付', title: '如何使用礼品卡', summary: '', body: `${detail.body}\n\n补充一句。`, locale: 'zh-CN', target_platforms: [], min_client_version: '', max_client_version: '', target_plan_ids: [], visibility: 'authenticated', status: 'published', expected_latest_version: 1 }
    expect(await (await call('POST', '/v1/content-pages', body, 'k-1')).json()).toMatchObject({ error: { message: '内容已产生新版本，请刷新后重试' } })
    expect(await (await call('POST', '/v1/content-pages', { ...body, slug: 'Bad Slug', body: '短', expected_latest_version: 0 }, 'k-2')).json()).toMatchObject({ error: { fields: { slug: expect.any(String), body: expect.any(String) } } })
    expect((await call('POST', '/v1/content-pages', { ...body, review_due_at: '' }, 'k-3')).status).toBe(400)
    const saved = await call('POST', '/v1/content-pages', { ...body, expected_latest_version: 2 }, 'k-4')
    expect(saved.status).toBe(201)
    expect(pageSaved.parse(await saved.json()).page).toMatchObject({ version: 3, status: 'published' })
    const after = (await list()).filter((p) => p.slug === 'gift-card')
    expect(after.filter((p) => p.status === 'published').map((p) => p.version)).toEqual([3])
    const archived = pageArchived.parse(await (await call('POST', `/v1/content-pages/${after[0]!.id}/archive`, { expected_version: 3 }, 'k-5')).json())
    expect(archived.page.already_archived).toBe(false)
    const again = pageArchived.parse(await (await call('POST', `/v1/content-pages/${after[0]!.id}/archive`, { expected_version: 3 }, 'k-6')).json())
    expect(again.page.already_archived).toBe(true)
    expect((await call('POST', `/v1/content-pages/${after[0]!.id}/archive`, { expected_version: 2 }, 'k-7')).status).toBe(409)
  })

  it('serves the paper theme, sanitizes slots and validates the site timezone', async () => {
    const themes = themesResponse.parse(await (await call('GET', '/v1/themes')).json()).themes!
    expect(themes).toHaveLength(1)
    expect(themes[0]).toMatchObject({ code: 'paper', is_active: true, custom_css: '' })
    expect(Object.keys(themes[0]!.tokens.light)).toHaveLength(43)
    expect(slotsResponse.parse(await (await call('GET', '/v1/slots')).json()).slots).toHaveLength(7)
    const dirty = slotSaved.parse(await (await call('POST', '/v1/slots/portal.home.banner', { content: '<b onclick="x()">欢迎</b><script>alert(1)</script>', enabled: true }, 's-1')).json())
    expect(dirty.dropped).toHaveLength(2)
    expect(slotsResponse.parse(await (await call('GET', '/v1/slots')).json()).slots.find((s) => s.key === 'portal.home.banner')).toMatchObject({ content: '<b>欢迎</b>', enabled: true })
    expect(slotSaved.parse(await (await call('POST', '/v1/slots/portal.home.banner', { content: '', enabled: false }, 's-2')).json()).dropped).toBeNull()
    expect(await (await call('POST', '/v1/slots/portal.nope', { content: '', enabled: false }, 's-3')).json()).toMatchObject({ error: { fields: { key: '未知的插槽位' } } })
    expect(siteSettingsSchema.parse(await (await call('GET', '/v1/settings/site')).json()).timezone).toBe('Asia/Shanghai')
    for (const timezone of ['', 'Local', 'Mars/Base']) expect(await (await call('POST', '/v1/settings/site', { timezone })).json()).toMatchObject({ error: { fields: { timezone: '不是有效的时区' } } })
    expect(await (await call('POST', '/v1/settings/site', { timezone: 'Asia/Tokyo' })).json()).toEqual({ timezone: 'Asia/Tokyo' })
  })
})
