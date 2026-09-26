/**
 * [INPUT]: 依赖 node:crypto 的 randomUUID，依赖 ../types 的 Json / MockContext / MockModule / MockResult，依赖 ../../../src/styles/design-tokens 的 COLOR_TOKENS（内置主题的 43 个令牌）
 * [OUTPUT]: 对外提供 content 模块的假接口 MockModule
 * [POS]: dev/mock/admin 的「内容与外观（后台-08）」假接口，归后台前端二；形状、错误码、reauth 与幂等照 api-contract.md（含 R19、R49）与 Go 的 announce.go、domain/content/service.go、appearance.go、site_settings.go：
 *        公告列表（到点的定时公告先转发布）、新建 / 编辑（全量覆盖、expected_version、撤回是终态、已发布不能退回草稿）、撤回；知识库每个版本一行、单版本含正文、保存为新版本（expected_latest_version，发布时归档同受众的旧发布版）、归档（重复归档回 already_archived）；
 *        主题只有生效的「默认 · 纸白」（主题写接口页面不调用，未模拟）；7 个插槽位（净化只模拟去掉 script / style / on* 并回 dropped，空内容 dropped 为 null）；站点时区（能被 Intl 加载的 IANA 名，拒绝空串与 Local）。按 DisallowUnknownFields 拒绝未知字段
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomUUID } from 'node:crypto'
import { COLOR_TOKENS } from '../../../src/styles/design-tokens.ts'
import type { Json, MockContext, MockModule, MockResult } from '../types.ts'

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------
const err = (status: number, code: string, message: string, fields?: Record<string, string>): MockResult => ({ status, body: { error: { code, message, ...(fields ? { fields } : {}) } } })
const invalid = (fields: Record<string, string>) => err(422, 'validation_failed', '请求参数校验未通过', fields)
const notFound = () => err(404, 'not_found', '资源不存在或无权访问')
const conflict = (message: string) => err(409, 'conflict', message)
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/
const runes = (v: string) => [...v].length
const at = (days: number, hours = 0) => new Date(Date.now() + days * 86_400_000 + hours * 3_600_000).toISOString()

/** 请求体读成对象；非法 JSON、未知字段、类型不符分别按 httpx.DecodeJSON 回 400 */
type Decoded = { ok: true; body: Json } | { ok: false; result: MockResult }
async function decode(ctx: MockContext, shape: Record<string, 'string' | 'boolean' | 'number' | 'strings'>): Promise<Decoded> {
  const bad = (message: string): Decoded => ({ ok: false, result: err(400, 'bad_request', message) })
  const body = await ctx.body()
  if (!body) return bad('请求体不是合法的 JSON')
  for (const [k, v] of Object.entries(body)) {
    const want = shape[k]
    if (!want) return bad(`请求体包含未知字段 "${k}"`)
    const ok = want === 'strings' ? v === null || (Array.isArray(v) && v.every((x) => typeof x === 'string')) : v === null || typeof v === want
    if (!ok) return bad(`字段 "${k}" 类型不正确`)
  }
  return { ok: true, body }
}
const str = (v: unknown) => (typeof v === 'string' ? v : '')
const strs = (v: unknown) => (Array.isArray(v) ? (v as string[]) : [])

/** 解析、去重、排序 uuid；有非法值返回 null */
function normalizeIds(raw: string[]): string[] | null {
  const out = new Set<string>()
  for (const v of raw) {
    if (!UUID.test(v.trim())) return null
    out.add(v.trim().toLowerCase())
  }
  return [...out].sort()
}

// ---------------------------------------------------------------------------
// 目录：套餐与用户组的 id 与 users.ts 的种子一致（公告定向到它们，门户按它们过滤）
// ---------------------------------------------------------------------------
const PLANS = [
  { id: '9c0e1a2b-3333-4b00-8000-000000000001', name: '标准版', status: 'active' },
  { id: '9c0e1a2b-3333-4b00-8000-000000000002', name: '专业版', status: 'active' },
  { id: '9c0e1a2b-3333-4b00-8000-000000000003', name: '家庭版', status: 'active' },
  { id: '9c0e1a2b-3333-4b00-8000-000000000004', name: '体验版', status: 'archived' },
]
const GROUPS = [
  { id: '9c0e1a2b-2222-4b00-8000-000000000001', name: 'VIP' },
  { id: '9c0e1a2b-2222-4b00-8000-000000000002', name: '企业客户' },
  { id: '9c0e1a2b-2222-4b00-8000-000000000003', name: '体验用户' },
]

// ---------------------------------------------------------------------------
// 公告（announce.go）
// ---------------------------------------------------------------------------
interface Ann {
  id: string
  title: string
  body: string
  severity: string
  pinned: boolean
  status: 'draft' | 'scheduled' | 'published' | 'withdrawn'
  version: number
  planIds: string[]
  groupIds: string[]
  publish_at: string | null
  expires_at: string | null
  published_at: string | null
  created_at: string
}

const ann = (p: Partial<Ann> & Pick<Ann, 'title' | 'body' | 'status'>): Ann => ({
  id: randomUUID(),
  severity: 'info',
  pinned: false,
  version: 1,
  planIds: [],
  groupIds: [],
  publish_at: null,
  expires_at: null,
  published_at: null,
  created_at: at(-40),
  ...p,
})

const anns: Ann[] = [
  ann({ title: '国庆期间节点扩容与维护时间表', body: '10 月 1 日至 7 日将新增香港、东京各 2 台服务器。\n\n维护窗口：每日 04:00–04:30，期间可能短暂断线。', status: 'published', pinned: true, severity: 'notice', publish_at: at(-2), published_at: at(-2), created_at: at(-3), version: 3 }),
  ann({ title: '新客户端 Hiddify 使用教程已上线', body: '在帮助中心「客户端教程」里查看。', status: 'published', publish_at: at(-9), published_at: at(-9), created_at: at(-9) }),
  ann({ title: '十月会员日：续费专业版赠送 50 GB', body: '10 月 10 日当天续费专业版，自动赠送 50 GB 流量包。', status: 'scheduled', severity: 'warning', planIds: [PLANS[1]!.id], groupIds: [GROUPS[0]!.id], publish_at: at(2, 3), expires_at: at(12), created_at: at(-1), version: 2 }),
  ann({ title: '专业版 v4 额度提升预告', body: '每周期流量从 500 GB 提升到 600 GB。', status: 'draft', planIds: [PLANS[1]!.id], created_at: at(-1, -5) }),
  ann({ title: '8 月支付宝通道波动说明', body: '已恢复，受影响的订单已自动补单。', status: 'withdrawn', severity: 'critical', publish_at: at(-43), published_at: at(-43), created_at: at(-43), version: 4 }),
]

/** 到点的定时公告转为已发布（notify.PublishDue 在后端定时跑，这里在读列表时顺手做） */
function promoteDue() {
  const now = Date.now()
  for (const a of anns) {
    if (a.status === 'scheduled' && a.publish_at && new Date(a.publish_at).getTime() <= now) {
      a.status = 'published'
      a.published_at = a.publish_at
      a.version++
    }
  }
}

function annJson(a: Ann) {
  return {
    id: a.id,
    title: a.title,
    body: a.body,
    severity: a.severity,
    pinned: a.pinned,
    status: a.status,
    version: a.version,
    target_plan_ids: a.planIds,
    plan_targets: a.planIds.map((id) => {
      const p = PLANS.find((x) => x.id === id)
      return { id, name: p?.name ?? '不可用套餐', status: p?.status ?? 'missing' }
    }),
    target_user_group_ids: a.groupIds,
    user_group_targets: GROUPS.filter((g) => a.groupIds.includes(g.id)).sort((x, y) => x.name.localeCompare(y.name)),
    publish_at: a.publish_at,
    expires_at: a.expires_at,
    created_at: a.created_at,
  }
}

const ANN_SHAPE = { title: 'string', body: 'string', severity: 'string', pinned: 'boolean', target_plan_ids: 'strings', target_user_group_ids: 'strings', publish_at: 'string', expires_at: 'string', publish: 'boolean', expected_version: 'number' } as const

async function saveAnn(ctx: MockContext, rawId: string | null): Promise<MockResult> {
  if (rawId !== null && !UUID.test(rawId)) return notFound()
  const decoded = await decode(ctx, ANN_SHAPE)
  if (!decoded.ok) return decoded.result
  const body = decoded.body
  const title = str(body.title).trim()
  const text = str(body.body).trim()
  let severity = str(body.severity)
  const expected = typeof body.expected_version === 'number' ? body.expected_version : 0
  const fields: Record<string, string> = {}
  if (runes(title) < 2 || runes(title) > 160) fields.title = '标题长度必须在 2 到 160 字之间'
  if (runes(text) < 2 || runes(text) > 20000) fields.body = '正文长度必须在 2 到 20000 字之间'
  if (severity === '') severity = 'info'
  else if (!['info', 'notice', 'warning', 'critical'].includes(severity)) fields.severity = '级别只能是 info / notice / warning / critical'
  if (rawId === null && expected !== 0) fields.expected_version = '新建公告的期望版本必须为 0'
  if (rawId !== null && expected <= 0) fields.expected_version = '编辑公告必须提交当前版本'
  if (Object.keys(fields).length > 0) return invalid(fields)

  const planIds = normalizeIds(strs(body.target_plan_ids))
  if (!planIds) return invalid({ target_plan_ids: '包含格式不正确的套餐 ID' })
  const groupIds = normalizeIds(strs(body.target_user_group_ids))
  if (!groupIds) return invalid({ target_user_group_ids: '包含格式不正确的用户组 ID' })
  const parseTime = (v: unknown) => {
    const s = str(v).trim()
    if (!s) return { ok: true as const, value: null }
    return RFC3339.test(s) ? { ok: true as const, value: new Date(s).toISOString() } : { ok: false as const }
  }
  const pub = parseTime(body.publish_at)
  const exp = parseTime(body.expires_at)
  if (!pub.ok || !exp.ok) return err(422, 'validation_failed', '时间必须是带时区的 RFC3339 格式')
  let publishAt = pub.value
  if (publishAt && exp.value && new Date(exp.value) <= new Date(publishAt)) return err(422, 'validation_failed', '下线时间必须晚于发布时间')

  let status: Ann['status'] = 'draft'
  let publishedAt: string | null = null
  if (body.publish === true) {
    const now = new Date().toISOString()
    if (publishAt && new Date(publishAt).getTime() > Date.now()) status = 'scheduled'
    else {
      status = 'published'
      publishedAt = now
      publishAt ??= now
    }
  }
  if (planIds.some((id) => !PLANS.some((p) => p.id === id))) return invalid({ target_plan_ids: '包含不存在或不属于当前租户的套餐' })
  if (groupIds.some((id) => !GROUPS.some((g) => g.id === id))) return invalid({ target_user_group_ids: '包含不存在或不属于当前租户的用户组' })

  const patch = { title, body: text, severity, pinned: body.pinned === true, planIds, groupIds, status, publish_at: publishAt, expires_at: exp.value }
  if (rawId === null) {
    const created = ann({ ...patch, published_at: publishedAt, created_at: new Date().toISOString() })
    anns.unshift(created)
    return { status: 200, body: { id: created.id, status, version: created.version } }
  }
  const a = anns.find((x) => x.id === rawId.toLowerCase())
  if (!a) return notFound()
  if (a.version !== expected) return conflict('公告已被其他管理员修改，请刷新后重试')
  if (a.status === 'withdrawn') return conflict('已撤回公告不可重新编辑，请新建公告')
  if (a.status === 'published' && status !== 'published') return conflict('已发布公告只能保持发布状态；如需下线请使用撤回')
  if (!(status === 'published' && a.status === 'published' && a.published_at)) a.published_at = publishedAt
  Object.assign(a, patch, { version: a.version + 1 })
  return { status: 200, body: { id: a.id, status, version: a.version } }
}

async function withdrawAnn(ctx: MockContext, rawId: string): Promise<MockResult> {
  if (!UUID.test(rawId)) return notFound()
  const decoded = await decode(ctx, { expected_version: 'number' })
  if (!decoded.ok) return decoded.result
  const body = decoded.body
  const expected = typeof body.expected_version === 'number' ? body.expected_version : 0
  if (expected <= 0) return invalid({ expected_version: '撤回公告必须提交当前版本' })
  const a = anns.find((x) => x.id === rawId.toLowerCase())
  if (!a) return notFound()
  if (a.version !== expected) return conflict('公告已被其他管理员修改，请刷新后重试')
  if (a.status === 'withdrawn') return conflict('这条公告已经撤回过了')
  a.status = 'withdrawn'
  a.version++
  return { status: 200, body: { ok: true, version: a.version } }
}

// ---------------------------------------------------------------------------
// 知识库（domain/content/service.go）：每个版本一行
// ---------------------------------------------------------------------------
interface ContentRow {
  id: string
  slug: string
  kind: string
  category: string
  version: number
  title: string
  summary: string
  body: string
  locale: string
  target_platforms: string[]
  min_client_version: string
  max_client_version: string
  target_plan_ids: string[]
  visibility: string
  status: 'draft' | 'published' | 'archived'
  review_due_at: string | null
  published_at: string | null
  created_at: string
  created_by: string | null
  created_by_name: string | null
}

const AUTHORS = [
  ['5f1c0a3e-1111-4c00-8000-000000000001', '周敏'],
  ['5f1c0a3e-1111-4c00-8000-000000000002', '林舟'],
  ['5f1c0a3e-1111-4c00-8000-000000000003', '唐一鸣'],
] as const

const pages: ContentRow[] = []
/** 一篇文章的全部版本：versions 从旧到新写状态，最后一版的内容用 body */
function seedArticle(slug: string, kind: string, category: string, title: string, body: string, versions: ContentRow['status'][], extra: Partial<ContentRow> = {}) {
  versions.forEach((status, i) => {
    const [by, name] = AUTHORS[(i + slug.length) % AUTHORS.length]!
    const created = at(-(versions.length - i) * 17)
    pages.push({
      id: randomUUID(),
      slug,
      kind,
      category,
      version: i + 1,
      title: i === versions.length - 1 ? title : `${title}（旧稿）`,
      summary: '',
      body: i === versions.length - 1 ? body : `${body}\n\n（v${i + 1} 的旧内容）`,
      locale: 'zh-CN',
      target_platforms: [],
      min_client_version: '',
      max_client_version: '',
      target_plan_ids: [],
      visibility: 'authenticated',
      status,
      review_due_at: null,
      published_at: status === 'draft' ? null : created,
      created_at: created,
      created_by: by,
      created_by_name: name,
      ...extra,
    })
  })
}
seedArticle('first-connect', 'kb_article', '快速上手', '三分钟完成首次连接', '# 三分钟完成首次连接\n\n1. 在「我的订阅」复制订阅地址\n2. 打开客户端，选择「从剪贴板导入」\n3. 选择延迟最低的节点', ['archived', 'archived', 'archived', 'archived', 'archived', 'published'])
seedArticle('clash-verge-rev', 'kb_article', '客户端教程', 'Clash Verge Rev（Windows / macOS）', '# Clash Verge Rev\n\n## 导入订阅\n\n配置 → 新建 → 类型选择 Remote，粘贴订阅地址。\n\n## 常见问题\n\n解析失败：请确认复制的是 Clash 格式链接。', ['archived', 'archived', 'published', 'draft'], { target_platforms: ['macos', 'windows'] })
seedArticle('shadowrocket', 'kb_article', '客户端教程', 'Shadowrocket（iOS）', '# Shadowrocket\n\n在 App Store 外区账号下载后，点右上角加号粘贴订阅地址。', ['archived', 'archived', 'published'], { target_platforms: ['ios'] })
seedArticle('hiddify', 'kb_article', '客户端教程', 'Hiddify（Android）', '# Hiddify\n\n首页点「新建配置」→「从剪贴板添加」。', ['published'], { target_platforms: ['android'] })
seedArticle('gift-card', 'kb_article', '账单与支付', '如何使用礼品卡', '# 礼品卡\n\n在「钱包」页输入卡码，先预览再兑换。', ['archived', 'published'])
seedArticle('refund-policy-old', 'kb_article', '账单与支付', '退款政策（旧）', '# 退款政策\n\n本政策已由新版服务条款取代。', ['published', 'archived', 'archived', 'archived', 'archived'])
seedArticle('router-setup', 'tutorial', '进阶', '在路由器上使用订阅', '# 路由器\n\nOpenWrt 安装 OpenClash 后导入 Clash 格式订阅。', ['published'], { target_platforms: ['linux'] })
seedArticle('terms-of-service', 'legal', '', '服务条款', '# 服务条款\n\n使用本服务即表示同意以下条款。', ['archived', 'published'])

const audienceKey = (r: Pick<ContentRow, 'locale' | 'target_platforms' | 'min_client_version' | 'max_client_version' | 'target_plan_ids' | 'visibility'>) =>
  JSON.stringify([r.locale, [...r.target_platforms].sort(), r.min_client_version, r.max_client_version, [...r.target_plan_ids].sort(), r.visibility])

/** Go 的 Page JSON：omitempty 字段为空时省略，列表不带正文，作者只在列表里 */
function pageJson(r: ContentRow, opts: { body: boolean; author: boolean }) {
  const siblings = pages.filter((p) => p.slug === r.slug)
  const latest = Math.max(...siblings.map((p) => p.version))
  const newest = !siblings.some((p) => p.version > r.version && audienceKey(p) === audienceKey(r))
  return {
    id: r.id,
    slug: r.slug,
    kind: r.kind,
    ...(r.category ? { category: r.category } : {}),
    version: r.version,
    title: r.title,
    ...(r.summary ? { summary: r.summary } : {}),
    ...(opts.body && r.body ? { body: r.body } : {}),
    locale: r.locale,
    sanitizer_version: 'plain-v1',
    target_platforms: r.target_platforms,
    ...(r.min_client_version ? { min_client_version: r.min_client_version } : {}),
    ...(r.max_client_version ? { max_client_version: r.max_client_version } : {}),
    target_plan_ids: r.target_plan_ids,
    visibility: r.visibility,
    status: r.status,
    ...(r.review_due_at ? { review_due_at: r.review_due_at } : {}),
    ...(r.published_at ? { published_at: r.published_at } : {}),
    created_at: r.created_at,
    updated_at: r.created_at,
    latest_version: latest,
    ...(newest ? { is_latest_in_audience: true } : {}),
    ...(opts.author && r.created_by ? { created_by: r.created_by } : {}),
    ...(opts.author && r.created_by_name ? { created_by_name: r.created_by_name } : {}),
  }
}

const PAGE_SHAPE = {
  slug: 'string', kind: 'string', category: 'string', title: 'string', summary: 'string', body: 'string', locale: 'string',
  target_platforms: 'strings', min_client_version: 'string', max_client_version: 'string', target_plan_ids: 'strings',
  visibility: 'string', status: 'string', review_due_at: 'string', expected_latest_version: 'number',
} as const
const SLUG = /^[a-z0-9]+(?:-[a-z0-9]+)*$/
const LOCALE = /^[a-z]{2,3}(?:-[A-Z]{2})?$/
const CLIENT_VERSION = /^v?[0-9]+(?:\.[0-9]+){0,2}$/
const PLATFORMS = ['web', 'windows', 'macos', 'linux', 'android', 'ios']
const cmpVersion = (a: string, b: string) => {
  const p = (s: string) => s.replace(/^v/, '').split('.').map((x) => Number(x) || 0)
  const [l, r] = [p(a), p(b)]
  for (let i = 0; i < 3; i++) if ((l[i] ?? 0) !== (r[i] ?? 0)) return (l[i] ?? 0) < (r[i] ?? 0) ? -1 : 1
  return 0
}

async function publishPage(ctx: MockContext): Promise<MockResult> {
  const decoded = await decode(ctx, PAGE_SHAPE)
  if (!decoded.ok) return decoded.result
  const body = decoded.body
  if (body.review_due_at !== undefined && body.review_due_at !== null && !RFC3339.test(str(body.review_due_at))) return err(400, 'bad_request', '字段 "review_due_at" 不是合法时间')
  const t = (k: string) => str(body[k]).trim()
  const slug = t('slug').toLowerCase()
  const kind = t('kind') || 'kb_article'
  const locale = t('locale') || 'zh-CN'
  const visibility = t('visibility') || 'authenticated'
  const status = t('status') || 'draft'
  const [minV, maxV] = [t('min_client_version'), t('max_client_version')]
  const fields: Record<string, string> = {}
  if (slug.length > 80 || !SLUG.test(slug)) fields.slug = '标识只能由小写字母、数字和单个连字符组成，最长 80 字符'
  if (!['page', 'kb_article', 'tutorial', 'legal'].includes(kind)) fields.kind = '类型必须是 page / kb_article / tutorial / legal'
  if (runes(t('category')) > 80) fields.category = '分类最长 80 字'
  if (runes(t('title')) < 2 || runes(t('title')) > 160) fields.title = '标题需在 2–160 字之间'
  if (runes(t('summary')) > 500) fields.summary = '摘要最长 500 字'
  if (runes(t('body')) < 10 || runes(t('body')) > 100000) fields.body = '正文需在 10–100000 字之间'
  if (!LOCALE.test(locale)) fields.locale = '语言格式不正确'
  if (visibility !== 'authenticated' && visibility !== 'internal') fields.visibility = '可见性必须是 authenticated / internal'
  if (status !== 'draft' && status !== 'published') fields.status = '状态必须是 draft / published'
  if (minV && !CLIENT_VERSION.test(minV)) fields.min_client_version = '最低客户端版本格式不正确'
  if (maxV && !CLIENT_VERSION.test(maxV)) fields.max_client_version = '最高客户端版本格式不正确'
  if (minV && maxV && cmpVersion(minV, maxV) > 0) fields.max_client_version = '最高客户端版本不能低于最低版本'
  const platforms = [...new Set(strs(body.target_platforms).map((p) => p.trim().toLowerCase()))].sort()
  if (platforms.some((p) => !PLATFORMS.includes(p))) fields.target_platforms = '包含不支持的平台'
  const planIds = normalizeIds(strs(body.target_plan_ids))
  if (!planIds) fields.target_plan_ids = '套餐 ID 格式不正确'
  if (Object.keys(fields).length > 0) return invalid(fields)

  const siblings = pages.filter((p) => p.slug === slug)
  const latest = siblings.length ? Math.max(...siblings.map((p) => p.version)) : 0
  const expected = typeof body.expected_latest_version === 'number' ? body.expected_latest_version : 0
  if (latest !== expected) return conflict('内容已产生新版本，请刷新后重试')
  const row: ContentRow = {
    id: randomUUID(),
    slug,
    kind,
    category: t('category'),
    version: latest + 1,
    title: t('title'),
    summary: t('summary'),
    body: t('body'),
    locale,
    target_platforms: platforms,
    min_client_version: minV,
    max_client_version: maxV,
    target_plan_ids: planIds!,
    visibility,
    status: status as ContentRow['status'],
    review_due_at: body.review_due_at ? new Date(str(body.review_due_at)).toISOString() : null,
    published_at: status === 'published' ? new Date().toISOString() : null,
    created_at: new Date().toISOString(),
    created_by: ctx.user.userId,
    created_by_name: ctx.user.displayName ?? ctx.user.email,
  }
  if (status === 'published') for (const p of siblings) if (p.status === 'published' && audienceKey(p) === audienceKey(row)) p.status = 'archived'
  pages.push(row)
  return { status: 201, body: { page: { id: row.id, slug: row.slug, version: row.version, status: row.status } } }
}

async function archivePage(ctx: MockContext, rawId: string): Promise<MockResult> {
  const decoded = await decode(ctx, { expected_version: 'number' })
  if (!decoded.ok) return decoded.result
  const body = decoded.body
  const expected = typeof body.expected_version === 'number' ? body.expected_version : 0
  if (!UUID.test(rawId) || expected <= 0) return notFound()
  const row = pages.find((p) => p.id === rawId.toLowerCase())
  if (!row) return notFound()
  if (row.version !== expected) return conflict('内容版本已变化，请刷新后重试')
  const already = row.status === 'archived'
  row.status = 'archived'
  return { status: 200, body: { page: { id: row.id, version: row.version, status: 'archived', already_archived: already } } }
}

// ---------------------------------------------------------------------------
// 主题、插槽、站点时区（appearance.go、site_settings.go）
// ---------------------------------------------------------------------------
const tokenGroup = (mode: 'light' | 'dark') => Object.fromEntries(COLOR_TOKENS.map((t) => [t.name, t[mode]]))
const PAPER = { id: randomUUID(), code: 'paper', name: '默认 · 纸白', is_builtin: true, is_active: true, tokens: { light: tokenGroup('light'), dark: tokenGroup('dark') }, branding: { site_name: 'Pandora' }, custom_css: '' }

const SLOT_CATALOG: ReadonlyArray<readonly [string, string, string]> = [
  ['portal.login.notice', '登录页提示', '登录框上方，未登录访客可见'],
  ['portal.home.banner', '概览页横幅', '概览页最顶部，通栏'],
  ['portal.home.aside', '概览页附加卡片', '概览页内容下方'],
  ['portal.sidebar.extra', '侧栏附加内容', '侧栏导航下方、账号信息上方'],
  ['portal.subscribe.notice', '订阅页说明', '「我的订阅」页顶部'],
  ['portal.plans.notice', '选购页说明', '「选购套餐」页顶部，适合放退换与发票说明'],
  ['portal.footer', '页脚', '所有页面底部'],
]
const stamp = () => new Date().toISOString().slice(0, 16).replace('T', ' ')
const savedSlots = new Map<string, { content: string; enabled: boolean; updated_at: string }>([
  ['portal.login.notice', { content: '国庆活动：全场 8 折，优惠码 AUTUMN26', enabled: true, updated_at: '2026-09-20 10:12' }],
  ['portal.subscribe.notice', { content: '订阅地址请勿分享，泄露后可在本页一键更换。', enabled: true, updated_at: '2026-09-02 18:40' }],
  ['portal.footer', { content: '<a href="/tos">服务条款</a> · <a href="/privacy">隐私</a>', enabled: false, updated_at: '2026-08-14 09:03' }],
])

/** 净化的粗略模拟：去掉 script / style 元素与 on* 属性，逐条说明；空内容回 null（与 Go 的 nil 切片一致） */
function sanitize(input: string): { clean: string; dropped: string[] | null } {
  const text = input.trim()
  if (!text) return { clean: '', dropped: null }
  const dropped = new Set<string>()
  const clean = text
    .replace(/<(script|style)\b[^>]*>[\s\S]*?<\/\1>/gi, (_, tag: string) => (dropped.add(`<${tag.toLowerCase()}> 元素已删除`), ''))
    .replace(/\s+on[a-z]+\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)/gi, () => (dropped.add('事件属性 on* 已删除'), ''))
    .trim()
  return { clean, dropped: [...dropped].sort() }
}

let siteTimezone = 'Asia/Shanghai'
const validTimezone = (name: string) => {
  if (!name || name === 'Local' || name.length > 64) return false
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: name })
    return true
  } catch {
    return false
  }
}

// ---------------------------------------------------------------------------
// 路由
// ---------------------------------------------------------------------------
const reply = (ctx: MockContext, r: MockResult) => ctx.send(r.status, r.body)

export const content: MockModule = {
  routes: {
    'GET /v1/announcements': (ctx) => {
      if (!ctx.requirePermission('ops.announcement.write')) return
      promoteDue()
      const ts = (a: Ann) => new Date(a.published_at ?? a.created_at).getTime()
      const list = [...anns].sort((a, b) => Number(b.pinned) - Number(a.pinned) || ts(b) - ts(a))
      ctx.send(200, { announcements: list.map(annJson), plans: PLANS, user_groups: GROUPS })
    },
    'POST /v1/announcements': async (ctx) => {
      if (!ctx.requirePermission('ops.announcement.write') || !ctx.requireReauth()) return
      await ctx.idempotent('announcement_save', () => saveAnn(ctx, null))
    },
    'POST /v1/announcements/:id/withdraw': async (ctx) => {
      if (!ctx.requirePermission('ops.announcement.write') || !ctx.requireReauth()) return
      await ctx.idempotent('announcement_withdraw', () => withdrawAnn(ctx, ctx.params.id!))
    },
    'POST /v1/announcements/:id': async (ctx) => {
      if (!ctx.requirePermission('ops.announcement.write') || !ctx.requireReauth()) return
      await ctx.idempotent('announcement_save', () => saveAnn(ctx, ctx.params.id!))
    },

    'GET /v1/content-pages': (ctx) => {
      if (!ctx.requirePermission('ops.content.write')) return
      const kind = ctx.query.get('kind') ?? ''
      const status = ctx.query.get('status') ?? ''
      const q = (ctx.query.get('q') ?? '').trim().toLowerCase()
      let limit = Number.parseInt(ctx.query.get('limit') ?? '', 10)
      if (!(limit > 0 && limit <= 500)) limit = 200
      const rows = pages
        .filter((p) => (!kind || p.kind === kind) && (!status || p.status === status) && (!q || p.slug.includes(q) || p.title.toLowerCase().includes(q)))
        .sort((a, b) => a.slug.localeCompare(b.slug) || b.version - a.version)
        .slice(0, limit)
      ctx.send(200, { pages: rows.map((r) => pageJson(r, { body: false, author: true })) })
    },
    'GET /v1/content-pages/:id': (ctx) => {
      if (!ctx.requirePermission('ops.content.write')) return
      const row = UUID.test(ctx.params.id!) ? pages.find((p) => p.id === ctx.params.id!.toLowerCase()) : undefined
      if (!row) return reply(ctx, notFound())
      ctx.send(200, { page: pageJson(row, { body: true, author: false }) })
    },
    'POST /v1/content-pages': async (ctx) => {
      if (!ctx.requirePermission('ops.content.write') || !ctx.requireReauth()) return
      await ctx.idempotent('content_page_version_create', () => publishPage(ctx))
    },
    'POST /v1/content-pages/:id/archive': async (ctx) => {
      if (!ctx.requirePermission('ops.content.write') || !ctx.requireReauth()) return
      await ctx.idempotent('content_page_archive', () => archivePage(ctx, ctx.params.id!))
    },

    'GET /v1/themes': (ctx) => {
      if (!ctx.requirePermission('platform.appearance.read')) return
      ctx.send(200, { themes: [PAPER] })
    },
    'GET /v1/slots': (ctx) => {
      if (!ctx.requirePermission('platform.appearance.read')) return
      ctx.send(200, { slots: SLOT_CATALOG.map(([key, label, where]) => ({ key, label, where, content: '', enabled: false, ...savedSlots.get(key) })) })
    },
    'POST /v1/slots/:key': async (ctx) => {
      if (!ctx.requirePermission('platform.appearance.write') || !ctx.requireReauth()) return
      await ctx.idempotent('appearance_slot_save', async () => {
        const decoded = await decode(ctx, { content: 'string', enabled: 'boolean' })
        if (!decoded.ok) return decoded.result
        const body = decoded.body
        const key = ctx.params.key!
        if (!SLOT_CATALOG.some(([k]) => k === key)) return invalid({ key: '未知的插槽位' })
        const { clean, dropped } = sanitize(str(body.content))
        if (new TextEncoder().encode(clean).length > 32768) return invalid({ content: '内容超过 32KB，请精简' })
        savedSlots.set(key, { content: clean, enabled: body.enabled === true, updated_at: stamp() })
        return { status: 200, body: { saved: true, dropped } }
      })
    },

    'GET /v1/settings/site': (ctx) => {
      if (!ctx.requirePermission('security.audit.read')) return
      ctx.send(200, { timezone: siteTimezone })
    },
    'POST /v1/settings/site': async (ctx) => {
      if (!ctx.requirePermission('platform.settings.write') || !ctx.requireReauth()) return
      const decoded = await decode(ctx, { timezone: 'string' })
      if (!decoded.ok) return reply(ctx, decoded.result)
      const tz = str(decoded.body.timezone)
      if (!validTimezone(tz)) return reply(ctx, invalid({ timezone: '不是有效的时区' }))
      siteTimezone = tz
      ctx.send(200, { timezone: tz })
    },
  },
}
