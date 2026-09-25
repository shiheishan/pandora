/**
 * [INPUT]: 依赖 ../../../core/format 的 formatDateTime，依赖 ./schemas 的类型与枚举
 * [OUTPUT]: 对外提供公告（状态文字、列表时间与可见范围文字、表单模型与校验、请求体、按钮取舍）、知识库（按 slug 聚成文章、按分类分组、表单模型与校验、请求体、受众是否改动）、时间输入互转、站点时区选项、插槽的脏判断与过滤提示、主题预览色
 * [POS]: admin/screens/content 的纯函数层，组件只做渲染与请求；content.test.ts 守住。校验文案与上下限照 Go 的 announce.go、domain/content/service.go
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { formatDateTime } from '../../../core/format'
import type { Announcement, AnnStatus, Kind, Page, Platform, Severity, Slot, Theme, Visibility } from './schemas'

const runes = (s: string) => [...s.trim()].length

// ---------------------------------------------------------------------------
// 时间输入：<input type="datetime-local"> / type="date" 用浏览器本地时区的无时区串，
// 后端要带时区的 RFC3339。未改动的值原样送回原始时间串，避免秒被截掉
// ---------------------------------------------------------------------------
const pad = (n: number) => String(n).padStart(2, '0')

export function toLocalInput(iso: string | null | undefined): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** 本地输入 → RFC3339（UTC，带 Z）；空串得 ''。original 是它的来源，输入没动就送回 original */
export function fromLocalInput(value: string, original?: string | null): string {
  if (!value) return ''
  if (original && toLocalInput(original) === value) return original
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? '' : d.toISOString()
}

export function toDateInput(iso: string | null | undefined): string {
  return toLocalInput(iso).slice(0, 10)
}

export function fromDateInput(value: string, original?: string | null): string {
  if (!value) return ''
  if (original && toDateInput(original) === value) return original
  const d = new Date(`${value}T00:00`)
  return Number.isNaN(d.getTime()) ? '' : d.toISOString()
}

// ---------------------------------------------------------------------------
// 公告 · 展示
// ---------------------------------------------------------------------------
export const ANN_STATUS: Record<AnnStatus, { label: string; tone: 'ok' | 'warn' | 'info' | 'muted' }> = {
  published: { label: '已发布', tone: 'ok' },
  draft: { label: '草稿', tone: 'warn' },
  scheduled: { label: '定时', tone: 'info' },
  withdrawn: { label: '已撤回', tone: 'muted' },
}

export const SEVERITY: Record<Severity, { label: string; tone: 'neutral' | 'info' | 'warn' | 'danger' }> = {
  info: { label: '普通', tone: 'neutral' },
  notice: { label: '提示', tone: 'info' },
  warning: { label: '警告', tone: 'warn' },
  critical: { label: '紧急', tone: 'danger' },
}

/** 列表里「可见范围」：套餐名与用户组名以「、」连接，都为空是全部用户 */
export function annTargetLabel(a: Pick<Announcement, 'plan_targets' | 'user_group_targets'>): string {
  const names = [...a.plan_targets.map((p) => p.name), ...a.user_group_targets.map((g) => g.name)]
  return names.length === 0 ? '全部用户' : names.join('、')
}

/** 列表里的时间：草稿写「草稿」，定时写 MM-DD HH:mm，其余写发布日 MM-DD */
export function annWhen(a: Pick<Announcement, 'status' | 'publish_at' | 'created_at'>): string {
  if (a.status === 'draft') return '草稿'
  const at = formatDateTime(a.publish_at ?? a.created_at).slice(5)
  return a.status === 'scheduled' ? at : at.slice(0, 5)
}

// ---------------------------------------------------------------------------
// 公告 · 表单
// ---------------------------------------------------------------------------
export interface AnnForm {
  title: string
  body: string
  severity: Severity
  pinned: boolean
  planIds: string[]
  groupIds: string[]
  /** datetime-local 值，空 = 不定时 */
  publishAt: string
  expiresAt: string
}

export function emptyAnnForm(): AnnForm {
  return { title: '', body: '', severity: 'info', pinned: false, planIds: [], groupIds: [], publishAt: '', expiresAt: '' }
}

export function annFormFrom(a: Announcement): AnnForm {
  return {
    title: a.title,
    body: a.body,
    severity: a.severity,
    pinned: a.pinned,
    planIds: [...a.target_plan_ids],
    groupIds: [...a.target_user_group_ids],
    publishAt: toLocalInput(a.publish_at),
    expiresAt: toLocalInput(a.expires_at),
  }
}

/** 本地校验，键为表单字段名；照 announce.go：标题 2–160、正文 2–20000（去首尾空白按字计），下线晚于发布 */
export function validateAnn(form: AnnForm, now: Date = new Date()): Record<string, string> {
  const errors: Record<string, string> = {}
  const t = runes(form.title)
  if (t < 2 || t > 160) errors.title = '标题长度必须在 2 到 160 字之间'
  const b = runes(form.body)
  if (b < 2 || b > 20000) errors.body = '正文长度必须在 2 到 20000 字之间'
  if (form.expiresAt) {
    const expires = new Date(form.expiresAt).getTime()
    const from = form.publishAt ? new Date(form.publishAt).getTime() : now.getTime()
    if (!(expires > from)) errors.expiresAt = form.publishAt ? '下线时间必须晚于发布时间' : '下线时间必须晚于现在'
  }
  return errors
}

/** 422 fields 的键 → 表单字段名 */
export function annFieldErrors(fields: Record<string, string>): Record<string, string> {
  const map: Record<string, string> = { target_plan_ids: 'targets', target_user_group_ids: 'targets' }
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(fields)) out[map[k] ?? k] = v
  return out
}

export interface AnnBody {
  title: string
  body: string
  severity: Severity
  pinned: boolean
  target_plan_ids: string[]
  target_user_group_ids: string[]
  publish_at: string
  expires_at: string
  publish: boolean
  expected_version: number
}

/** 新建与编辑同一形状（编辑是全量覆盖）；original 用来原样送回没改过的时间 */
export function annBody(form: AnnForm, publish: boolean, original: Announcement | null): AnnBody {
  return {
    title: form.title.trim(),
    body: form.body.trim(),
    severity: form.severity,
    pinned: form.pinned,
    target_plan_ids: [...form.planIds].sort(),
    target_user_group_ids: [...form.groupIds].sort(),
    publish_at: fromLocalInput(form.publishAt, original?.publish_at),
    expires_at: fromLocalInput(form.expiresAt, original?.expires_at),
    publish,
    expected_version: original?.version ?? 0,
  }
}

export interface AnnActions {
  /** 主按钮文字；null = 不显示（已撤回） */
  primary: string | null
  /** 保存草稿（publish=false）：已发布的公告不能退回草稿，后端回 409 */
  draft: boolean
  /** 撤回：已发布与定时（= 取消定时，不可恢复） */
  withdraw: boolean
  /** 已发布公告的发布时间不可改（改到将来会变成定时，后端 409） */
  scheduleLocked: boolean
  /** 已撤回是终态（D-D-3 已决，5.A.2）：只读，给「复制为新公告」 */
  readOnly: boolean
}

export function annActions(status: AnnStatus | 'new', publishAt: string, now: Date = new Date()): AnnActions {
  if (status === 'withdrawn') return { primary: null, draft: false, withdraw: false, scheduleLocked: true, readOnly: true }
  if (status === 'published') return { primary: '保存修改', draft: false, withdraw: true, scheduleLocked: true, readOnly: false }
  const future = publishAt !== '' && new Date(publishAt).getTime() > now.getTime()
  if (status === 'scheduled') return { primary: future ? '保存修改' : '立即发布', draft: true, withdraw: true, scheduleLocked: false, readOnly: false }
  return { primary: future ? '定时发布' : '发布', draft: true, withdraw: false, scheduleLocked: false, readOnly: false }
}

// ---------------------------------------------------------------------------
// 知识库 · 按 slug 聚成文章（列表每个版本一行，按 slug、version 倒序）
// ---------------------------------------------------------------------------
export const KIND_LABEL: Record<Kind, string> = { kb_article: '知识库', tutorial: '教程', page: '页面', legal: '法律条款' }
export const PLATFORM_LABEL: Record<Platform, string> = { web: '网页', windows: 'Windows', macos: 'macOS', linux: 'Linux', android: 'Android', ios: 'iOS' }
export const VISIBILITY_LABEL: Record<Visibility, string> = { authenticated: '门户用户', internal: '仅后台' }
export const PAGE_STATUS_LABEL = { draft: '草稿', published: '已发布', archived: '已归档' } as const

export interface Article {
  slug: string
  /** 最新版本那一行（version == latest_version） */
  latest: Page
  /** 同 slug 全部版本，版本号倒序 */
  rows: Page[]
  /** 版本号最大的已发布版本；归档对它调用 */
  published: Page | null
  /** 没有已发布版本且最新版已归档：列表淡显，按钮是「恢复」 */
  archived: boolean
}

export function groupArticles(pages: readonly Page[]): Article[] {
  const bySlug = new Map<string, Page[]>()
  for (const p of pages) {
    const rows = bySlug.get(p.slug)
    if (rows) rows.push(p)
    else bySlug.set(p.slug, [p])
  }
  return [...bySlug.entries()].map(([slug, rows]) => {
    rows.sort((a, b) => b.version - a.version)
    const top = rows[0]!
    const latest = rows.find((r) => r.version === (top.latest_version ?? top.version)) ?? top
    const published = rows.find((r) => r.status === 'published') ?? null
    return { slug, latest, rows, published, archived: published === null && latest.status === 'archived' }
  })
}

export const UNCATEGORIZED = '未分类'

/** 左栏：先按分类分组（分类按中文排序、未分类垫底），组内按标题；q 对最新版的标题与 slug 做不区分大小写的包含 */
export function groupByCategory(articles: readonly Article[], q = ''): Array<{ cat: string; items: Article[] }> {
  const needle = q.trim().toLowerCase()
  const hit = (a: Article) => !needle || a.latest.title.toLowerCase().includes(needle) || a.slug.includes(needle)
  const groups = new Map<string, Article[]>()
  for (const a of articles.filter(hit)) {
    const cat = a.latest.category || UNCATEGORIZED
    groups.set(cat, [...(groups.get(cat) ?? []), a])
  }
  return [...groups.entries()]
    .sort(([a], [b]) => (a === UNCATEGORIZED ? 1 : b === UNCATEGORIZED ? -1 : a.localeCompare(b, 'zh')))
    .map(([cat, items]) => ({ cat, items: items.sort((x, y) => x.latest.title.localeCompare(y.latest.title, 'zh')) }))
}

export function knownCategories(articles: readonly Article[]): string[] {
  return [...new Set(articles.map((a) => a.latest.category).filter((c): c is string => !!c))].sort((a, b) => a.localeCompare(b, 'zh'))
}

// ---------------------------------------------------------------------------
// 知识库 · 表单
// ---------------------------------------------------------------------------
export interface KbForm {
  slug: string
  kind: Kind
  category: string
  title: string
  summary: string
  body: string
  locale: string
  platforms: string[]
  minClient: string
  maxClient: string
  planIds: string[]
  visibility: Visibility
  /** date 输入值，空 = 不设复审 */
  reviewDue: string
}

export function emptyKbForm(kind: Kind): KbForm {
  return { slug: '', kind, category: '', title: '', summary: '', body: '', locale: 'zh-CN', platforms: [], minClient: '', maxClient: '', planIds: [], visibility: 'authenticated', reviewDue: '' }
}

export function kbFormFrom(p: Page): KbForm {
  return {
    slug: p.slug,
    kind: p.kind,
    category: p.category ?? '',
    title: p.title,
    summary: p.summary ?? '',
    body: p.body ?? '',
    locale: p.locale,
    platforms: [...p.target_platforms],
    minClient: p.min_client_version ?? '',
    maxClient: p.max_client_version ?? '',
    planIds: [...p.target_plan_ids],
    visibility: p.visibility,
    reviewDue: toDateInput(p.review_due_at),
  }
}

const SLUG = /^[a-z0-9]+(?:-[a-z0-9]+)*$/
const LOCALE = /^[a-z]{2,3}(?:-[A-Z]{2})?$/
const CLIENT_VERSION = /^v?[0-9]+(?:\.[0-9]+){0,2}$/

export function compareVersion(a: string, b: string): number {
  const parse = (raw: string) => {
    const parts = raw.replace(/^v/, '').split('.')
    return [0, 1, 2].map((i) => Number.parseInt(parts[i] ?? '0', 10) || 0)
  }
  const [l, r] = [parse(a), parse(b)]
  for (let i = 0; i < 3; i++) if (l[i]! !== r[i]!) return l[i]! < r[i]! ? -1 : 1
  return 0
}

/** 照 normalizePublishInput 的规则与文案；takenSlugs 是已有文章的 slug（新文章撞上时后端只会回版本冲突，先在前端说清楚） */
export function validateKb(form: KbForm, takenSlugs: readonly string[] = []): Record<string, string> {
  const errors: Record<string, string> = {}
  const slug = form.slug.trim().toLowerCase()
  if (slug.length > 80 || !SLUG.test(slug)) errors.slug = '标识只能由小写字母、数字和单个连字符组成，最长 80 字符'
  else if (takenSlugs.includes(slug)) errors.slug = '这个标识已被其他文章使用'
  if (runes(form.category) > 80) errors.category = '分类最长 80 字'
  const t = runes(form.title)
  if (t < 2 || t > 160) errors.title = '标题需在 2–160 字之间'
  if (runes(form.summary) > 500) errors.summary = '摘要最长 500 字'
  const b = runes(form.body)
  if (b < 10 || b > 100000) errors.body = '正文需在 10–100000 字之间'
  if (!LOCALE.test(form.locale.trim())) errors.locale = '语言格式不正确'
  const min = form.minClient.trim()
  const max = form.maxClient.trim()
  if (min && !CLIENT_VERSION.test(min)) errors.min_client_version = '最低客户端版本格式不正确'
  if (max && !CLIENT_VERSION.test(max)) errors.max_client_version = '最高客户端版本格式不正确'
  else if (min && max && CLIENT_VERSION.test(min) && compareVersion(min, max) > 0) errors.max_client_version = '最高客户端版本不能低于最低版本'
  return errors
}

export interface KbBody {
  slug: string
  kind: Kind
  category: string
  title: string
  summary: string
  body: string
  locale: string
  target_platforms: string[]
  min_client_version: string
  max_client_version: string
  target_plan_ids: string[]
  visibility: Visibility
  status: 'draft' | 'published'
  review_due_at?: string
  expected_latest_version: number
}

/** review_due_at 是 *time.Time，空串解析会 400，所以不设时整个键省略 */
export function kbBody(form: KbForm, status: 'draft' | 'published', expectedLatest: number, original?: Page | null): KbBody {
  const review = fromDateInput(form.reviewDue, original?.review_due_at)
  return {
    slug: form.slug.trim().toLowerCase(),
    kind: form.kind,
    category: form.category.trim(),
    title: form.title.trim(),
    summary: form.summary.trim(),
    body: form.body.trim(),
    locale: form.locale.trim(),
    target_platforms: [...form.platforms].sort(),
    min_client_version: form.minClient.trim(),
    max_client_version: form.maxClient.trim(),
    target_plan_ids: [...form.planIds].sort(),
    visibility: form.visibility,
    status,
    ...(review ? { review_due_at: review } : {}),
    expected_latest_version: expectedLatest,
  }
}

/**
 * 受众（语言、平台、客户端版本范围、限定套餐、可见性）与来源版本不同：
 * 后端只归档「同一受众组合」的旧发布版，改了受众，旧版会和新版同时对用户可见
 */
export function audienceChanged(form: KbForm, from: Page): boolean {
  const same = (a: readonly string[], b: readonly string[]) => [...a].sort().join() === [...b].sort().join()
  return (
    form.locale.trim() !== from.locale ||
    !same(form.platforms, from.target_platforms) ||
    form.minClient.trim() !== (from.min_client_version ?? '') ||
    form.maxClient.trim() !== (from.max_client_version ?? '') ||
    !same(form.planIds, from.target_plan_ids) ||
    form.visibility !== from.visibility
  )
}

// ---------------------------------------------------------------------------
// 站点时区（R49）：常用 IANA 名，Asia/Shanghai 置顶；当前值不在表里也要能显示
// ---------------------------------------------------------------------------
export const COMMON_TIMEZONES: ReadonlyArray<readonly [string, string]> = [
  ['Asia/Shanghai', '中国 · 上海'],
  ['Asia/Hong_Kong', '中国 · 香港'],
  ['Asia/Taipei', '中国 · 台北'],
  ['Asia/Singapore', '新加坡'],
  ['Asia/Tokyo', '日本 · 东京'],
  ['Asia/Seoul', '韩国 · 首尔'],
  ['Asia/Bangkok', '泰国 · 曼谷'],
  ['Asia/Kolkata', '印度 · 加尔各答'],
  ['Asia/Dubai', '阿联酋 · 迪拜'],
  ['Europe/Moscow', '俄罗斯 · 莫斯科'],
  ['Europe/Berlin', '德国 · 柏林'],
  ['Europe/London', '英国 · 伦敦'],
  ['America/New_York', '美国 · 纽约'],
  ['America/Chicago', '美国 · 芝加哥'],
  ['America/Los_Angeles', '美国 · 洛杉矶'],
  ['Australia/Sydney', '澳大利亚 · 悉尼'],
  ['UTC', '协调世界时'],
]

/** 「UTC+8」「UTC−3:30」「UTC」；运行环境不认识的名字返回 null */
export function utcOffsetLabel(tz: string, at: Date = new Date()): string | null {
  try {
    const part = new Intl.DateTimeFormat('en-US', { timeZone: tz, timeZoneName: 'shortOffset' }).formatToParts(at).find((p) => p.type === 'timeZoneName')
    const raw = part?.value ?? ''
    return raw === 'GMT' || raw === 'GMT+0' ? 'UTC' : raw.replace(/^GMT/, 'UTC').replace('-', '−')
  } catch {
    return null
  }
}

export function timezoneOptions(current: string | undefined, at: Date = new Date()): Array<{ value: string; label: string }> {
  const rows = COMMON_TIMEZONES.some(([tz]) => tz === current) || !current ? COMMON_TIMEZONES : [[current, current] as const, ...COMMON_TIMEZONES]
  return rows.map(([tz, name]) => {
    const offset = utcOffsetLabel(tz, at)
    return { value: tz, label: `${name === tz ? tz : `${name}（${tz}）`}${offset ? ` ${offset}` : ''}` }
  })
}

// ---------------------------------------------------------------------------
// 插槽：失焦时只在内容真的变了才保存（每次保存都要 reauth 与新幂等键）
// ---------------------------------------------------------------------------
export function slotDirty(draft: string | undefined, slot: Pick<Slot, 'content'>): boolean {
  return draft !== undefined && draft !== slot.content
}

/** 净化丢掉的标签 / 属性说明；null 或空 = 没丢东西 */
export function droppedMessage(dropped: readonly string[] | null): string | null {
  return dropped && dropped.length > 0 ? `部分内容已被过滤：${dropped.join('；')}` : null
}

// ---------------------------------------------------------------------------
// 主题卡片预览：取亮色一组的背景、正文、品牌色；缺键回退到当前令牌
// ---------------------------------------------------------------------------
export function themePreview(t: Pick<Theme, 'tokens'>): { bg: string; fg: string; accent: string } {
  const light = t.tokens.light
  return { bg: light['--bg'] ?? 'var(--bg)', fg: light['--text'] ?? 'var(--text)', accent: light['--brand'] ?? 'var(--brand)' }
}

export function siteName(t: Pick<Theme, 'branding'>): string | null {
  const v = t.branding.site_name
  return typeof v === 'string' && v.trim() ? v.trim() : null
}
