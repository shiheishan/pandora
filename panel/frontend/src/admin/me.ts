import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../shell/runtime'

// 后台不分角色：登录了后台就是管理员。me 只用来画侧栏账户块、认出当前管理员（如工单「指派给我」）
export const adminMeSchema = z.object({
  user_id: z.string(),
  email: z.string(),
  // display_name 为空白时 null
  display_name: z.string().nullable(),
  reauthed: z.boolean(),
})
export type AdminMe = z.output<typeof adminMeSchema>

export const ME_QUERY_KEY = ['admin', 'me'] as const

export function useAdminMe() {
  const api = useApi()
  return useQuery({
    queryKey: ME_QUERY_KEY,
    queryFn: ({ signal }) => api.get('v1/me', adminMeSchema, { signal }),
    staleTime: 60_000,
  })
}
