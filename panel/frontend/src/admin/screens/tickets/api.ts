/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useQuery / useInfiniteQuery / useQueryClient，依赖 react 的 useCallback，依赖 zod，依赖 ../../../shell/runtime 的 useApi
 * [OUTPUT]: 对外提供工单模块的 zod schema 与类型（Ticket、TicketDetail、Message、Assignee、Macro 等）、读 hook（useTicketQueue、useTicketDetail、useAssignees、useMacros）、TK 查询键前缀与 useInvalidateTickets、写接口的响应 schema；ticketDetailSchema / assigneesSchema / macrosSchema 为 tests/smoke 形状冒烟导出
 * [POS]: admin/screens/tickets 的数据层：形状照 api-contract.md 后台-02 与修订 R25 / R42 / R60，并按 domain/support/service.go 的 json tag 核对（omitempty 的字段一律可选）；model.ts 消费类型，界面组件消费 hook
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'
import { z } from 'zod'
import { useApi } from '../../../shell/runtime'

// ---------------------------------------------------------------------------
// 枚举：后端封闭列表（00008 的 CHECK、support.Categories），未知值判为不符约定
// ---------------------------------------------------------------------------
export const TICKET_STATUSES = ['open', 'pending_user', 'pending_agent', 'escalated', 'resolved', 'closed'] as const
export const TICKET_PRIORITIES = ['low', 'normal', 'high', 'urgent'] as const
export const TICKET_CATEGORIES = ['general', 'billing', 'subscription', 'technical', 'account', 'abuse'] as const
export type TicketStatus = (typeof TICKET_STATUSES)[number]
export type TicketPriority = (typeof TICKET_PRIORITIES)[number]
export type TicketCategory = (typeof TICKET_CATEGORIES)[number]

const count = z.number().int().nonnegative()
const time = z.string()

// ---------------------------------------------------------------------------
// Ticket：列表与详情共用一个 Go 结构。带 omitempty 的字段（后端 nil / false / '' 时省略）
// 写 optional；closed_reason、related_order 没有 omitempty，恒在、可为 null。
// related_order 目前只在门户详情填，后台恒为 null（已报告协调会话），有值时照样显示。
// ---------------------------------------------------------------------------
const ticketSchema = z.object({
  id: z.string(),
  ticket_no: z.string(),
  subject: z.string(),
  category: z.enum(TICKET_CATEGORIES),
  priority: z.enum(TICKET_PRIORITIES),
  status: z.enum(TICKET_STATUSES),
  created_at: time,
  updated_at: time,
  resolved_at: time.nullable(),
  user_id: z.string().optional(),
  user_email: z.string().optional(),
  assigned_to: z.string().optional(),
  assignee_email: z.string().optional(),
  sla_first_response_due: time.optional(),
  sla_resolution_due: time.optional(),
  first_responded_at: time.optional(),
  escalated_at: time.optional(),
  sla_breached: z.boolean().optional(),
  // R60：队列行有，最后一条非内部备注消息的作者；为 user 时加粗（代替已读表）
  last_message_author_kind: z.enum(['user', 'agent', 'system']).optional(),
  // R60：详情有，active / trialing 最新订阅的套餐名
  user_active_plan: z.string().optional(),
  message_count: count,
  last_reply_at: time,
  closed_reason: z.enum(['user_closed', 'withdrawn', 'agent_closed']).nullable(),
  related_order: z.object({ id: z.string(), order_no: z.string() }).nullable(),
})

const messageSchema = z.object({
  id: z.string(),
  author_kind: z.enum(['user', 'agent', 'system']),
  author_name: z.string().nullable(),
  body: z.string(),
  internal_note: z.literal(true).optional(),
  created_at: time,
})

// 详情的 messages 在 Go 里是 omitempty 的切片：一条都没有时整个键省略，归一成 []
export const ticketDetailSchema = ticketSchema.extend({
  messages: z
    .array(messageSchema)
    .optional()
    .transform((m) => m ?? []),
})

export const queueSchema = z.object({ tickets: z.array(ticketSchema), total: count })
export type Ticket = z.output<typeof ticketSchema>
export type TicketDetail = z.output<typeof ticketDetailSchema>
export type Message = z.output<typeof messageSchema>

const assigneeSchema = z.object({ id: z.string(), email: z.string(), display_name: z.string().optional() })
export const assigneesSchema = z.object({ assignees: z.array(assigneeSchema) })
export type Assignee = z.output<typeof assigneeSchema>

const macroSchema = z.object({ id: z.string(), title: z.string(), body: z.string(), sort_order: z.number().int(), updated_at: time })
export const macrosSchema = z.object({ macros: z.array(macroSchema) })
export type Macro = z.output<typeof macroSchema>

// 写接口的响应
export const okSchema = z.object({ ok: z.literal(true) })
export const escalatedSchema = z.object({ escalated: count })
export const macroSavedSchema = z.object({ id: z.string() })

// ---------------------------------------------------------------------------
// 查询
// ---------------------------------------------------------------------------
export const TK = ['admin', 'tickets'] as const
export const QUEUE_PAGE = 25

/** 队列筛选（已映射成后端 query，见 model.queueParams） */
export interface QueueParams {
  status?: string
  assigned_to?: string
  breached?: '1'
  q?: string
}

/** 队列按「加载更多」翻页：limit / offset，页尾以 total 判断还有没有 */
export function useTicketQueue(params: QueueParams) {
  const api = useApi()
  return useInfiniteQuery({
    queryKey: [...TK, 'queue', params],
    queryFn: ({ signal, pageParam }) => api.get('v1/tickets', queueSchema, { signal, query: { ...params, limit: QUEUE_PAGE, offset: pageParam } }),
    initialPageParam: 0,
    getNextPageParam: (last, pages) => {
      const loaded = pages.reduce((n, p) => n + p.tickets.length, 0)
      return loaded < last.total && last.tickets.length > 0 ? loaded : undefined
    },
    meta: { topics: ['tickets.changed'] },
    placeholderData: (prev) => prev,
    refetchInterval: 60_000,
  })
}

export function useTicketDetail(id: string | null) {
  const api = useApi()
  return useQuery({
    queryKey: [...TK, 'detail', id],
    queryFn: ({ signal }) => api.get(`v1/tickets/${encodeURIComponent(id!)}`, ticketDetailSchema, { signal }),
    enabled: id !== null,
    meta: { topics: ['tickets.changed'] },
  })
}

export function useAssignees(enabled: boolean) {
  const api = useApi()
  return useQuery({
    queryKey: [...TK, 'assignees'],
    queryFn: ({ signal }) => api.get('v1/tickets/assignees', assigneesSchema, { signal }).then((r) => r.assignees),
    enabled,
    staleTime: 5 * 60_000,
  })
}

export function useMacros(enabled: boolean) {
  const api = useApi()
  return useQuery({
    queryKey: [...TK, 'macros'],
    queryFn: ({ signal }) => api.get('v1/ticket-macros', macrosSchema, { signal }).then((r) => r.macros),
    enabled,
    staleTime: 5 * 60_000,
  })
}

/** 写成功后：队列与该工单详情都重拉（实时事件也会到，但本人操作要立刻看到） */
export function useInvalidateTickets() {
  const client = useQueryClient()
  return useCallback(
    (what: 'tickets' | 'macros' = 'tickets') =>
      what === 'macros'
        ? client.invalidateQueries({ queryKey: [...TK, 'macros'] })
        : Promise.all([client.invalidateQueries({ queryKey: [...TK, 'queue'] }), client.invalidateQueries({ queryKey: [...TK, 'detail'] })]),
    [client],
  )
}
