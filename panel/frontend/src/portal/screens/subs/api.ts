import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useApi } from '../../../shell/runtime'
import { SUBSCRIPTIONS_KEY } from '../../queries'

// ---------------------------------------------------------------------------
// PATCH v1/me/subscriptions/{id} {label}（设计稿 2.8）：空串或 null 清除；同一用户下不分大小写唯一，
// 撞名 409；不合规 422 fields.label。不幂等、不推进节点纪元。响应 {label, client_name}
// ---------------------------------------------------------------------------
import { renameSchema } from './schemas'

export function useRename() {
  const api = useApi()
  const client = useQueryClient()
  return useMutation({
    mutationFn: ({ id, label }: { id: string; label: string }) => api.request(`v1/me/subscriptions/${encodeURIComponent(id)}`, renameSchema, { method: 'PATCH', body: { label: label.trim() || null } }),
    onSuccess: () => void client.invalidateQueries({ queryKey: SUBSCRIPTIONS_KEY }),
  })
}
