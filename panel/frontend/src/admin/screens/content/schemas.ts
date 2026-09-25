/**
 * [INPUT]: 依赖 zod
 * [OUTPUT]: 对外提供内容与外观页的 zod schema 与类型：公告（列表、保存、撤回）、知识库内容页（列表行、单版本、保存、归档）、主题、插槽、站点时区、套餐目录子集
 * [POS]: admin/screens/content 的数据边界，与后端对账的唯一防线：形状照 api-contract.md 后台-08（含 R19、R49）并按 Go 处理器核对——Go 指针字段写 nullable、omitempty 字段写 optional、只在特定分支为 null 的切片写 nullable 后归一
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { z } from 'zod'

const uuid = z.string().uuid()
const time = z.string()

// ---------------------------------------------------------------------------
// 公告（announce.go：publish_at / expires_at 是无 omitempty 的指针，缺值为 null）
// ---------------------------------------------------------------------------
export const SEVERITIES = ['info', 'notice', 'warning', 'critical'] as const
export type Severity = (typeof SEVERITIES)[number]
export const ANN_STATUSES = ['draft', 'scheduled', 'published', 'withdrawn'] as const
export type AnnStatus = (typeof ANN_STATUSES)[number]

const planRef = z.object({ id: uuid, name: z.string(), status: z.string() })
const groupRef = z.object({ id: uuid, name: z.string() })

export const announcementSchema = z.object({
  id: uuid,
  title: z.string(),
  body: z.string(),
  severity: z.enum(SEVERITIES),
  pinned: z.boolean(),
  status: z.enum(ANN_STATUSES),
  version: z.number().int(),
  target_plan_ids: z.array(uuid),
  /** status 为 missing 表示套餐已删（名字回「不可用套餐」） */
  plan_targets: z.array(planRef),
  target_user_group_ids: z.array(uuid),
  user_group_targets: z.array(groupRef),
  publish_at: time.nullable(),
  expires_at: time.nullable(),
  created_at: time,
})
export type Announcement = z.output<typeof announcementSchema>
export type PlanRef = z.output<typeof planRef>
export type GroupRef = z.output<typeof groupRef>

export const announcementsResponse = z.object({
  announcements: z.array(announcementSchema),
  plans: z.array(planRef),
  user_groups: z.array(groupRef),
})
export type AnnouncementsData = z.output<typeof announcementsResponse>

export const annSaved = z.object({ id: uuid, status: z.enum(ANN_STATUSES), version: z.number().int() })
export const annWithdrawn = z.object({ ok: z.literal(true), version: z.number().int() })

// ---------------------------------------------------------------------------
// 知识库（domain/content Page：category / summary / 客户端版本 / 日期 / 作者都是 omitempty）
// ---------------------------------------------------------------------------
export const KINDS = ['kb_article', 'tutorial', 'page', 'legal'] as const
export type Kind = (typeof KINDS)[number]
export const PLATFORMS = ['web', 'windows', 'macos', 'linux', 'android', 'ios'] as const
export type Platform = (typeof PLATFORMS)[number]
export const PAGE_STATUSES = ['draft', 'published', 'archived'] as const
export type PageStatus = (typeof PAGE_STATUSES)[number]
export const VISIBILITIES = ['authenticated', 'internal'] as const
export type Visibility = (typeof VISIBILITIES)[number]

export const pageSchema = z.object({
  id: uuid,
  slug: z.string(),
  kind: z.enum(KINDS),
  category: z.string().optional(),
  version: z.number().int(),
  title: z.string(),
  summary: z.string().optional(),
  /** 只有单版本接口带正文 */
  body: z.string().optional(),
  locale: z.string(),
  sanitizer_version: z.string(),
  target_platforms: z.array(z.string()),
  min_client_version: z.string().optional(),
  max_client_version: z.string().optional(),
  target_plan_ids: z.array(uuid),
  visibility: z.enum(VISIBILITIES),
  status: z.enum(PAGE_STATUSES),
  review_due_at: time.optional(),
  published_at: time.optional(),
  created_at: time,
  updated_at: time,
  latest_version: z.number().int().optional(),
  is_latest_in_audience: z.boolean().optional(),
  /** 版本作者只在列表里有 */
  created_by: uuid.optional(),
  created_by_name: z.string().optional(),
})
export type Page = z.output<typeof pageSchema>

export const pagesResponse = z.object({ pages: z.array(pageSchema) })
export const pageResponse = z.object({ page: pageSchema })
export const pageSaved = z.object({ page: z.object({ id: uuid, slug: z.string(), version: z.number().int(), status: z.enum(['draft', 'published']) }) })
export const pageArchived = z.object({ page: z.object({ id: uuid, version: z.number().int(), status: z.literal('archived'), already_archived: z.boolean() }) })

// ---------------------------------------------------------------------------
// 主题与插槽（R19：tokens 分 light / dark 两组；无主题时 themes 为 null）
// ---------------------------------------------------------------------------
const tokenGroup = z.record(z.string(), z.string())
export const themeSchema = z.object({
  id: uuid,
  code: z.string(),
  name: z.string(),
  is_builtin: z.boolean(),
  is_active: z.boolean(),
  tokens: z.object({ light: tokenGroup, dark: tokenGroup }),
  branding: z.record(z.string(), z.unknown()),
  custom_css: z.string(),
})
export type Theme = z.output<typeof themeSchema>
export const themesResponse = z.object({ themes: z.array(themeSchema).nullable() })

export const slotSchema = z.object({
  key: z.string(),
  label: z.string(),
  where: z.string(),
  content: z.string(),
  enabled: z.boolean(),
  /** 数据库会话时区的 YYYY-MM-DD HH24:MI；从没保存过的插槽位没有 */
  updated_at: z.string().optional(),
})
export type Slot = z.output<typeof slotSchema>
export const slotsResponse = z.object({ slots: z.array(slotSchema) })
/** 内容为空时净化器回 nil，dropped 就是 null（契约写 string[]，按代码事实收） */
export const slotSaved = z.object({ saved: z.literal(true), dropped: z.array(z.string()).nullable() })

// ---------------------------------------------------------------------------
// 站点时区（R49）
// ---------------------------------------------------------------------------
export const siteSettingsSchema = z.object({ timezone: z.string() })

// ---------------------------------------------------------------------------
// 套餐目录子集（GET v1/plans，catalog.read）：知识库「限定套餐」只要名字与状态
// ---------------------------------------------------------------------------
export const planCatalogResponse = z.object({ plans: z.array(z.object({ id: uuid, name: z.string(), status: z.enum(['draft', 'active', 'archived']) })) })
export type CatalogPlan = z.output<typeof planCatalogResponse>['plans'][number]
