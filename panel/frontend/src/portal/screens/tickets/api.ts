/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useMutation / useQuery / useQueryClient，依赖 zod，依赖 ../../../shell/runtime 的 useApi，依赖 ../common/orders 的 ordersPageSchema
 * [OUTPUT]: 对外提供 TICKET_STATUSES、ticketRowSchema / TicketRow、ticketDetailSchema / TicketDetail、ticketMessageSchema / TicketMessage、useCategories、useTickets、useTicket、useRecentOrders、useCreateTicket、useReplyTicket、useCloseTicket、useWithdrawTicket
 * [POS]: portal/screens/tickets 的数据层（契约门户-07，修订 R25、R60）：分类、列表、详情（含消息）、新建（幂等 support_ticket_create，201）、回复（幂等 support_ticket_user_reply）、关闭（幂等 support_ticket_user_close，无请求体）、撤回（不幂等、必须带 JSON 体）；实时 ticket.updated / tickets.changed 失效列表与详情；新建表单的「关联订单」取最近 20 张订单
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../../../shell/runtime'
import { ordersPageSchema } from '../common/orders'

// ---------------------------------------------------------------------------
// 读形状（domain/support.Ticket）。三个列表 Go 端都以空切片初始化，不收 null。
// 修订 R60 的 closed_reason / related_order 写成可选：旧后端（假后端 legacy）缺席时降级
// ---------------------------------------------------------------------------
export const TICKET_STATUSES = ['open', 'pending_user', 'pending_agent', 'escalated', 'resolved', 'closed'] as const

const ticketBase = {
  id: z.string(),
  ticket_no: z.string(),
  subject: z.string(),
  category: z.string(),
  priority: z.string(),
  status: z.enum(TICKET_STATUSES),
  created_at: z.string(),
  updated_at: z.string(),
  resolved_at: z.string().nullable(),
  message_count: z.number().int(),
  last_reply_at: z.string(),
  closed_reason: z.enum(['user_closed', 'withdrawn', 'agent_closed']).nullable().optional(),
}

/** 列表行：related_order 只在详情里填，列表恒为 null */
export const ticketRowSchema = z.object({ ...ticketBase, related_order: z.null().optional() })
export type TicketRow = z.output<typeof ticketRowSchema>

export const ticketMessageSchema = z.object({
  id: z.string(),
  author_kind: z.enum(['user', 'agent', 'system']),
  // 非 user 恒为 null（客服真实姓名不给用户，D-F-2 已决）；user 是自己的 display_name，也可能为 null
  author_name: z.string().nullable(),
  body: z.string(),
  created_at: z.string(),
})
export type TicketMessage = z.output<typeof ticketMessageSchema>

/** 详情：message_count 为 0、last_reply_at 为零值（详情不填），以 messages 为准 */
export const ticketDetailSchema = z.object({
  ...ticketBase,
  related_order: z.object({ id: z.string(), order_no: z.string() }).nullable().optional(),
  messages: z.array(ticketMessageSchema),
})
export type TicketDetail = z.output<typeof ticketDetailSchema>

const TOPICS = ['ticket.updated', 'tickets.changed'] as const
const LIST_KEY = ['portal', 'tickets', 'list'] as const
const detailKey = (id: string) => ['portal', 'tickets', 'detail', id] as const

// ---------------------------------------------------------------------------
// 读
// ---------------------------------------------------------------------------
export function useCategories() {
  const api = useApi()
  return useQuery({
    queryKey: ['portal', 'tickets', 'categories'],
    queryFn: ({ signal }) => api.get('v1/support/categories', z.object({ categories: z.array(z.object({ code: z.string(), name: z.string() })) }), { signal }),
    select: (d) => d.categories,
    staleTime: 60 * 60_000,
  })
}

/** 最多 100 条，按 updated_at 倒序，不分页 */
export function useTickets() {
  const api = useApi()
  return useQuery({
    queryKey: LIST_KEY,
    queryFn: ({ signal }) => api.get('v1/support/tickets', z.object({ tickets: z.array(ticketRowSchema) }), { signal }),
    select: (d) => d.tickets,
    meta: { topics: TOPICS },
  })
}

export function useTicket(id: string | undefined) {
  const api = useApi()
  return useQuery({
    queryKey: detailKey(id ?? ''),
    queryFn: ({ signal }) => api.get(`v1/support/tickets/${encodeURIComponent(id!)}`, ticketDetailSchema, { signal }),
    enabled: Boolean(id),
    meta: { topics: TOPICS },
  })
}

/** 新建表单「关联订单」的候选：最近 20 张，不分状态（契约门户-07 待补·前端） */
export function useRecentOrders() {
  const api = useApi()
  return useQuery({
    queryKey: ['portal', 'orders', { recent: 20 }],
    queryFn: ({ signal }) => api.get('v1/orders', ordersPageSchema, { query: { limit: 20 }, signal }),
    select: (d) => d.orders,
    meta: { topics: ['orders.changed'] },
  })
}

// ---------------------------------------------------------------------------
// 写：成功后失效列表与这张工单（失败也失效：409 往往说明状态已被别处改了）
// ---------------------------------------------------------------------------
function useRefresh() {
  const client = useQueryClient()
  return (id?: string) => {
    void client.invalidateQueries({ queryKey: LIST_KEY })
    if (id) void client.invalidateQueries({ queryKey: detailKey(id) })
  }
}

export interface NewTicket {
  subject: string
  category: string
  body: string
  order_id?: string
}

export function useCreateTicket() {
  const api = useApi()
  const refresh = useRefresh()
  return useMutation({
    mutationFn: ({ body, key }: { body: NewTicket; key: string }) => api.post('v1/support/tickets', ticketRowSchema, { body, idempotencyKey: key }),
    onSuccess: () => refresh(),
  })
}

export function useReplyTicket(id: string) {
  const api = useApi()
  const refresh = useRefresh()
  return useMutation({
    mutationFn: ({ body, key }: { body: string; key: string }) => api.post(`v1/support/tickets/${encodeURIComponent(id)}/reply`, z.object({ ok: z.literal(true) }), { body: { body }, idempotencyKey: key }),
    onSettled: () => refresh(id),
  })
}

export function useCloseTicket(id: string) {
  const api = useApi()
  const refresh = useRefresh()
  return useMutation({
    mutationFn: (key: string) => api.post(`v1/support/tickets/${encodeURIComponent(id)}/close`, z.object({ ok: z.literal(true) }), { idempotencyKey: key }),
    onSettled: () => refresh(id),
  })
}

/** 撤回不幂等；后端用 DecodeJSON，空体 400，所以至少带 {} */
export function useWithdrawTicket(id: string) {
  const api = useApi()
  const refresh = useRefresh()
  return useMutation({
    mutationFn: () => api.post(`v1/support/tickets/${encodeURIComponent(id)}/withdraw`, z.object({ withdrawn: z.literal(true) }), { body: {} }),
    onSettled: () => refresh(id),
  })
}
