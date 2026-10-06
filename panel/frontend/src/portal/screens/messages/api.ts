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
