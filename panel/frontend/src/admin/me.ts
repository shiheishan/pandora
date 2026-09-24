/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useQuery，依赖 zod，依赖 ../shell/runtime 的 useApi
 * [OUTPUT]: 对外提供 adminMeSchema、AdminMe、ME_QUERY_KEY、useAdminMe、identityLabels
 * [POS]: admin 的当前管理员身份：GET v1/me（契约后台外壳），侧栏账户块、权限判断与 reauth 状态都读它；email / display_name / roles 是待补·后端字段，按可选处理
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
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
  // 待补·后端：先按可选，后端补上即生效
  email: z.string().optional(),
  display_name: z.string().nullable().optional(),
  roles: z.array(z.object({ code: z.string(), name: z.string() })).optional(),
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
 * 头像字 ← 角色名首字。后端未补字段前退回「管理员」与 user_id 前 8 位。
 */
export function identityLabels(me: AdminMe | undefined) {
  const role = me?.roles?.[0]?.name ?? '管理员'
  const name = me?.display_name || me?.email?.split('@')[0] || ''
  return {
    title: name ? `${role} · ${name}` : role,
    subtitle: me?.email ?? (me ? me.user_id.slice(0, 8) : ''),
    initial: role.slice(0, 1),
  }
}
