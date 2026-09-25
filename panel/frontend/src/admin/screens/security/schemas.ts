/**
 * [INPUT]: 依赖 zod
 * [OUTPUT]: 对外提供安全与运维页的 zod schema、类型与枚举：审计日志（行、列表响应、操作者类型、结果）、访问日志（安全事件行、分类）、共享 IP 聚类（成员、复核结论、风险、网络类型、标记正常与批量停用的响应、跳过原因）、降级开关（行、列表、切换响应）
 * [POS]: admin/screens/security 的数据边界：形状照 api-contract.md 后台-09 · 安全与运维（含 R23 R40 R41 R44 R58），并按 Go 的 audit_log.go + adminops/audit.go、access_log.go、risk.go + adminops/risk.go、handlers.go setSwitch 核对——
 *        审计行的可空字段都是无 omitempty 的指针（缺值 null），actor_kind 按迁移 00012 的 CHECK 含 node（契约漏写）；访问日志行除 category / occurred_at 外全是 omitempty（缺值即缺席），订阅拉取的 outcome 是原始 result（ok / not_found / revoked / expired / rate_limited），所以不收成枚举
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { z } from 'zod'

// ---------------------------------------------------------------------------
// 审计日志（adminops.AuditRow）
// ---------------------------------------------------------------------------
/** agent 是客服，node 是 Node Agent 进程（迁移 00012），两者不是一回事 */
export const ACTOR_KINDS = ['user', 'admin', 'system', 'agent', 'node', 'plugin', 'anonymous'] as const
export type ActorKind = (typeof ACTOR_KINDS)[number]
export const OUTCOMES = ['success', 'failure', 'denied', 'partial'] as const
export type Outcome = (typeof OUTCOMES)[number]

export const auditEventSchema = z.object({
  id: z.string(),
  occurred_at: z.string(),
  actor_kind: z.enum(ACTOR_KINDS),
  actor_email: z.string().nullable(),
  action: z.string(),
  resource_type: z.string().nullable(),
  resource_id: z.string().nullable(),
  api_domain: z.string().nullable(),
  outcome: z.enum(OUTCOMES),
  /** 取自 after_digest.reason */
  reason: z.string().nullable(),
  /** 对象可读名；对象已删或类型不在映射里为 null */
  resource_label: z.string().nullable(),
  /** 00080 之前的记录与系统记录为 null */
  auth_context: z.enum(['session', 'reauth']).nullable(),
  /** 解不开（早于加密上线）为 null */
  source_ip: z.string().nullable(),
})
export type AuditEvent = z.output<typeof auditEventSchema>
export const auditResponse = z.object({ events: z.array(auditEventSchema), total: z.number().int() })
export type AuditPage = z.output<typeof auditResponse>

// ---------------------------------------------------------------------------
// 访问日志（安全事件：audit_events 与 subscription_fetch_log 两路归并）
// ---------------------------------------------------------------------------
export const ACCESS_CATEGORIES = ['login', 'register', 'reset_password', 'order', 'payment', 'ticket', 'admin', 'other', 'subscribe'] as const
export type AccessCategory = (typeof ACCESS_CATEGORIES)[number]

export const accessItemSchema = z.object({
  category: z.enum(ACCESS_CATEGORIES),
  action: z.string().optional(),
  user_id: z.string().optional(),
  user_email: z.string().optional(),
  ip: z.string().optional(),
  geo: z.string().optional(),
  network_kind: z.string().optional(),
  user_agent: z.string().optional(),
  /** 审计四种结果之一，或订阅拉取的原始 result（ok 即成功） */
  outcome: z.string().optional(),
  occurred_at: z.string(),
})
export type AccessItem = z.output<typeof accessItemSchema>
/** 没有 total：两路归并后切页，只能按「这页满没满」判断还有没有更早的 */
export const accessResponse = z.object({ items: z.array(accessItemSchema) })

// ---------------------------------------------------------------------------
// 共享 IP 聚类（risk.go 的 ipClusterView 内嵌 adminops.IPCluster）
// ---------------------------------------------------------------------------
export const RISKS = ['high', 'mid', 'low'] as const
export type Risk = (typeof RISKS)[number]

export const clusterUserSchema = z.object({
  id: z.string(),
  email: z.string(),
  status: z.string(),
  active_plan: z.string().nullable(),
})
export type ClusterUser = z.output<typeof clusterUserSchema>

export const clusterReviewSchema = z.object({
  decision: z.enum(['normal', 'disabled']),
  decided_at: z.string(),
  /** disabled 没有有效期 */
  expires_at: z.string().nullable(),
})
export type ClusterReview = z.output<typeof clusterReviewSchema>

export const clusterSchema = z.object({
  /** 解不开时为 "" */
  ip: z.string(),
  geo: z.string(),
  network_kind: z.string(),
  risk: z.enum(RISKS),
  /** source_ip_hash 的 hex，处置接口用它定位 */
  key: z.string(),
  accounts: z.number().int(),
  events: z.number().int(),
  first: z.string(),
  last: z.string(),
  emails: z.array(z.string()),
  users: z.array(clusterUserSchema),
  review: clusterReviewSchema.nullable(),
})
export type Cluster = z.output<typeof clusterSchema>
export const clustersResponse = z.object({ clusters: z.array(clusterSchema) })

export const clusterReviewed = z.object({ key: z.string(), decision: z.literal('normal'), expires_at: z.string() })

export const SKIP_REASONS = ['self', 'administrator', 'not_member', 'already_disabled'] as const
export type SkipReason = (typeof SKIP_REASONS)[number]
export const clusterDisabled = z.object({
  disabled: z.number().int(),
  skipped: z.array(z.object({ user_id: z.string(), reason: z.enum(SKIP_REASONS) })),
})
export type ClusterDisabled = z.output<typeof clusterDisabled>

// ---------------------------------------------------------------------------
// 降级开关（adminops.SwitchRow；enabled = 功能可用）
// ---------------------------------------------------------------------------
export const switchSchema = z.object({
  code: z.string(),
  enabled: z.boolean(),
  essential: z.boolean(),
  reason: z.string().nullable(),
})
export type SwitchRow = z.output<typeof switchSchema>
export const switchesResponse = z.object({ switches: z.array(switchSchema) })
export const switchSaved = z.object({ ok: z.literal(true), enabled: z.boolean() })
