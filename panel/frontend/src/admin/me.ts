/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useQuery，依赖 zod，依赖 ../shell/runtime 的 useApi
 * [OUTPUT]: 对外提供 adminMeSchema、AdminMe、ME_QUERY_KEY、useAdminMe、identityLabels
 * [POS]: admin 的当前管理员身份：GET v1/me（契约后台外壳），侧栏账户块、权限判断与 reauth 状态都读它；email / display_name / roles 恒在（api/admin handlers.me 的 map 字面量，display_name 可为 null，roles 至少是空数组）
 */
import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../shell/runtime'

export const adminMeSchema = z.object({
  user_id: z.string(),
  permissions: z
    .array(z.string())
    .nullable()
    .transform((p) => p ?? []),
  reauthed: z.boolean(),
  // identity.AdminProfile：display_name 为空白时 null；roles 由 pgx.CollectRows 给出，没有绑定时是 []
  email: z.string(),
  display_name: z.string().nullable(),
  roles: z.array(z.object({ code: z.string(), name: z.string() })),
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
 * 侧栏账户块的三段文字（契约映射）：角色 ← roles[0].name；姓名 ← display_name ?? email 本地部分；
 * 头像字 ← 角色名首字。没有租户级角色时退回「管理员」。
 */
export function identityLabels(me: AdminMe | undefined) {
  const role = me?.roles[0]?.name ?? '管理员'
  const name = me?.display_name || me?.email.split('@')[0] || ''
  return {
    title: name ? `${role} · ${name}` : role,
    subtitle: me?.email ?? '',
    initial: role.slice(0, 1),
  }
}
