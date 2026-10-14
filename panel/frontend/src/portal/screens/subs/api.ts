import { useMutation, useQueryClient } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../../../shell/runtime'
import { SUBSCRIPTIONS_KEY } from '../../queries'

// ---------------------------------------------------------------------------
// PATCH v1/me/subscriptions/{id} {label}（设计稿 2.8）：空串或 null 清除；同一用户下不分大小写唯一，
// 撞名 409；不合规 422 fields.label。不幂等、不推进节点纪元。响应 {label, client_name}
// ---------------------------------------------------------------------------
export { renameSchema } from './schemas'
import { renameSchema } from './schemas'

export function useRename() {
  const api = useApi()
  const client = useQueryClient()
  return useMutation({
    mutationFn: ({ id, label }: { id: string; label: string }) => api.request(`v1/me/subscriptions/${encodeURIComponent(id)}`, renameSchema, { method: 'PATCH', body: { label: label.trim() || null } }),
    onSuccess: () => void client.invalidateQueries({ queryKey: SUBSCRIPTIONS_KEY }),
  })
}

// ---------------------------------------------------------------------------
// POST v1/me/traffic-packs/transfer（设计稿 2.7）：from 为 null 是「未分配」，否则须是彻底停用的那份；
// 目标须在用或能救回。不幂等：重复调用第二次没东西可转，moved_bytes=0
// ---------------------------------------------------------------------------
export const transferSchema = z.object({ moved_bytes: z.number().int() })

export function useTransferPacks() {
  const api = useApi()
  const client = useQueryClient()
  return useMutation({
    mutationFn: ({ from, to }: { from: string | null; to: string }) => api.post('v1/me/traffic-packs/transfer', transferSchema, { body: { from_subscription_id: from, to_subscription_id: to } }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: SUBSCRIPTIONS_KEY })
      void client.invalidateQueries({ queryKey: ['portal', 'traffic-packs'] })
    },
  })
}
