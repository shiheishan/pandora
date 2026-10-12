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

/**
 * 侧栏账户块的三段文字：标题「管理员 · 姓名」，姓名 ← display_name ?? email 本地部分；
 * 头像字取姓名首字，me 还没回来时取「管」。
 */
export function identityLabels(me: AdminMe | undefined) {
  const role = '管理员'
  const name = me?.display_name || me?.email.split('@')[0] || ''
  return {
    title: name ? `${role} · ${name}` : role,
    subtitle: me?.email ?? '',
    initial: (name || role).slice(0, 1),
  }
}
