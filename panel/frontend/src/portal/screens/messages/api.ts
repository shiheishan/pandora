/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useMutation / useQuery / useQueryClient，依赖 zod，依赖 ../../../shell/runtime 的 useApi
 * [OUTPUT]: 对外提供 notificationSchema / Notification、NOTIFICATION_LIMIT、useNotifications、useMarkRead、useMarkAllRead
 * [POS]: portal/screens/messages 的数据层（契约门户-08）：站内信收件箱（一次取 100 条，接口无游标）、单条已读与全部已读（天然幂等、无请求体）；新站内信没有 SSE，外框铃铛自己 60 秒轮询 limit=1，这里的写操作失效 ['portal','notifications'] 整个前缀，铃铛角标一起刷新；公告复用 common/announcements
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../../../shell/runtime'

export const notificationSchema = z.object({
  id: z.string(),
  code: z.string(),
  subject: z.string(),
  body: z.string(),
  // notification_deliveries.sent_at 列可空，没有约束把它和 status='sent' 绑在一起
  sent_at: z.string().nullable(),
  read_at: z.string().nullable(),
})
export type Notification = z.output<typeof notificationSchema>

/** 接口上限 100（默认 30）；没有游标，一次取满 */
export const NOTIFICATION_LIMIT = 100

const PREFIX = ['portal', 'notifications'] as const

export function useNotifications() {
  const api = useApi()
  return useQuery({
    queryKey: [...PREFIX, 'list'],
    queryFn: ({ signal }) => api.get('v1/me/notifications', z.object({ notifications: z.array(notificationSchema), unread: z.number().int() }), { query: { limit: NOTIFICATION_LIMIT }, signal }),
  })
}

export function useMarkRead() {
  const api = useApi()
  const client = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.post(`v1/me/notifications/${encodeURIComponent(id)}/read`, z.object({ ok: z.literal(true) })),
    onSettled: () => void client.invalidateQueries({ queryKey: PREFIX }),
  })
}

export function useMarkAllRead() {
  const api = useApi()
  const client = useQueryClient()
  return useMutation({
    mutationFn: () => api.post('v1/me/notifications/read-all', z.object({ ok: z.literal(true) })),
    onSettled: () => void client.invalidateQueries({ queryKey: PREFIX }),
  })
}
