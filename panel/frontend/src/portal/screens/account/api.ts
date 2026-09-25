/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useMutation / useQuery / useQueryClient，依赖 zod，依赖 ../../../shell/runtime 的 useApi，依赖 ./model 的偏好类别与渠道
 * [OUTPUT]: 对外提供 sessionSchema / Session、useSessions、useRevokeSession、useChangePassword、quickLoginSchema、useIssueQuickLogin、telegramSchema / Telegram、useTelegram、useBindCode、useUnbindTelegram、preferenceSchema / Preference、usePreferences、useSetPreference，preferencesSchema（tests/smoke 形状冒烟用）
 * [POS]: portal/screens/account 的数据层（契约门户-10，修订 R15、R28、R62）：会话列表与吊销、改密码（口令错回 401，passwordCheck 不触发登出；成功保留当前会话、其余下线）、快捷登录签发（保留规则 1：只在已登录会话签发、60 秒、一次性）、Telegram 状态 / 绑定码 / 解绑、通知偏好（一次改一项，乐观更新、失败回滚）。这几条都没挂幂等中间件，不带键；个人信息复用外框的 usePortalMe
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../../../shell/runtime'
import { PREF_CATEGORIES, PREF_CHANNELS, type PrefCategory, type PrefChannel } from './model'

const PREFIX = ['portal', 'account'] as const
const SESSIONS_KEY = [...PREFIX, 'sessions'] as const
const TELEGRAM_KEY = [...PREFIX, 'telegram'] as const
const PREFS_KEY = [...PREFIX, 'preferences'] as const

// ---------------------------------------------------------------------------
// 会话：Go SessionInfo，country / last_seen_at / expires_at 都是 omitempty；
// last_seen_at 从不更新（修订 R62）、country 无人写入（D-F-3），页面都不显示
// ---------------------------------------------------------------------------
export const sessionSchema = z.object({
  id: z.string(),
  current: z.boolean(),
  user_agent: z.string(),
  country: z.string().optional(),
  created_at: z.string(),
  last_seen_at: z.string().optional(),
  expires_at: z.string().optional(),
})
export type Session = z.output<typeof sessionSchema>

export function useSessions() {
  const api = useApi()
  return useQuery({
    queryKey: SESSIONS_KEY,
    queryFn: ({ signal }) => api.get('v1/me/sessions', z.object({ sessions: z.array(sessionSchema) }), { signal }),
    select: (d) => d.sessions,
  })
}

export function useRevokeSession() {
  const api = useApi()
  const client = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.delete(`v1/me/sessions/${encodeURIComponent(id)}`, z.object({ revoked: z.literal(true) })),
    onSettled: () => void client.invalidateQueries({ queryKey: SESSIONS_KEY }),
  })
}

export function useChangePassword() {
  const api = useApi()
  const client = useQueryClient()
  return useMutation({
    mutationFn: (body: { old_password: string; new_password: string }) => api.post('v1/me/password', z.object({ ok: z.literal(true) }), { body, passwordCheck: true }),
    // 其余会话已被吊销（修订 R62），列表重拉
    onSuccess: () => void client.invalidateQueries({ queryKey: SESSIONS_KEY }),
  })
}

// ---------------------------------------------------------------------------
// 快捷登录：无请求体；同一会话再生成时旧令牌立即作废
// ---------------------------------------------------------------------------
export const quickLoginSchema = z.object({ token: z.string().min(1), expires_at: z.string(), expires_in: z.number().int().positive() })

export function useIssueQuickLogin() {
  const api = useApi()
  return useMutation({ mutationFn: () => api.post('v1/me/quick-login', quickLoginSchema) })
}

// ---------------------------------------------------------------------------
// Telegram：绑定成功没有实时事件，「检查绑定状态」就是重拉；展示绑定码期间页面 3 秒重拉一次
// ---------------------------------------------------------------------------
export const telegramSchema = z.object({
  bound: z.boolean(),
  username: z.string().optional(),
  bot_username: z.string().optional(),
  enabled: z.boolean(),
})
export type Telegram = z.output<typeof telegramSchema>

export function useTelegram() {
  const api = useApi()
  return useQuery({ queryKey: TELEGRAM_KEY, queryFn: ({ signal }) => api.get('v1/me/telegram', telegramSchema, { signal }) })
}

export const bindCodeSchema = z.object({ code: z.string().min(1), bot_username: z.string(), expires_at: z.string() })

export function useBindCode() {
  const api = useApi()
  return useMutation({ mutationFn: () => api.post('v1/me/telegram/bind-code', bindCodeSchema) })
}

export function useUnbindTelegram() {
  const api = useApi()
  const client = useQueryClient()
  return useMutation({
    mutationFn: () => api.delete('v1/me/telegram', z.object({ unbound: z.literal(true) })),
    onSettled: () => void client.invalidateQueries({ queryKey: TELEGRAM_KEY }),
  })
}

// ---------------------------------------------------------------------------
// 通知偏好：固定 6 项，transactional 两项 locked 且恒开
// ---------------------------------------------------------------------------
export const preferenceSchema = z.object({
  category: z.enum(PREF_CATEGORIES),
  channel: z.enum(PREF_CHANNELS),
  enabled: z.boolean(),
  locked: z.boolean(),
})
export type Preference = z.output<typeof preferenceSchema>
export const preferencesSchema = z.object({ preferences: z.array(preferenceSchema) })
type Preferences = z.output<typeof preferencesSchema>

export function usePreferences() {
  const api = useApi()
  return useQuery({
    queryKey: PREFS_KEY,
    queryFn: ({ signal }) => api.get('v1/me/notification-preferences', preferencesSchema, { signal }),
    select: (d) => d.preferences,
  })
}

/** 每次点击调用一次（天然幂等）：先改缓存，失败回滚到点击前 */
export function useSetPreference() {
  const api = useApi()
  const client = useQueryClient()
  return useMutation({
    mutationFn: (body: { category: PrefCategory; channel: PrefChannel; enabled: boolean }) => api.put('v1/me/notification-preferences', z.object({ ok: z.literal(true) }), { body }),
    onMutate: async (body) => {
      await client.cancelQueries({ queryKey: PREFS_KEY })
      const before = client.getQueryData<Preferences>(PREFS_KEY)
      client.setQueryData<Preferences>(PREFS_KEY, (d) =>
        d ? { preferences: d.preferences.map((p) => (p.category === body.category && p.channel === body.channel ? { ...p, enabled: body.enabled } : p)) } : d,
      )
      return { before }
    },
    onError: (_e, _body, context) => {
      if (context?.before) client.setQueryData(PREFS_KEY, context.before)
    },
    onSettled: () => void client.invalidateQueries({ queryKey: PREFS_KEY }),
  })
}
