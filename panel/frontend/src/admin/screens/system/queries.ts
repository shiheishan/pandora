/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useQuery / useQueryClient，依赖 react 的 useCallback，依赖 ../../../shell/runtime 的 useApi，依赖 ../../actions 的 useCan / useFailure / useIntentKey（转出），依赖 ./schemas
 * [OUTPUT]: 对外提供 SK 查询键前缀、通知与插件页各读 hook（邮件设置、Telegram 设置、模板列表、钩子列表、投递记录）、useInvalidateSystem，并转出 useCan / useFailure / useIntentKey
 * [POS]: admin/screens/system 的数据层：读只经 react-query + core/api；这几张表都没有实时变更通知，写后按 SK 前缀失效
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'
import { useApi } from '../../../shell/runtime'
import { deliveriesResponse, hooksResponse, mailSettingsSchema, telegramSettingsSchema, templatesResponse } from './schemas'

export { useCan, useFailure, useIntentKey } from '../../actions'

export const SK = ['admin', 'system'] as const

export function useMailSettings() {
  const api = useApi()
  return useQuery({ queryKey: [...SK, 'mail'], queryFn: ({ signal }) => api.get('v1/settings/mail', mailSettingsSchema, { signal }) })
}

export function useTelegramSettings() {
  const api = useApi()
  return useQuery({ queryKey: [...SK, 'telegram'], queryFn: ({ signal }) => api.get('v1/settings/telegram', telegramSettingsSchema, { signal }) })
}

export function useTemplates() {
  const api = useApi()
  return useQuery({ queryKey: [...SK, 'templates'], queryFn: ({ signal }) => api.get('v1/mail/templates', templatesResponse, { signal }).then((r) => r.templates) })
}

export function useHooks() {
  const api = useApi()
  return useQuery({ queryKey: [...SK, 'hooks'], queryFn: ({ signal }) => api.get('v1/plugin-hooks', hooksResponse, { signal }) })
}

/** 投递记录只在卡片展开后才挂载请求；null 归一为空数组 */
export function useDeliveries(code: string) {
  const api = useApi()
  return useQuery({
    queryKey: [...SK, 'deliveries', code],
    queryFn: ({ signal }) => api.get(`v1/plugin-hooks/${encodeURIComponent(code)}/deliveries`, deliveriesResponse, { signal }).then((r) => r.deliveries ?? []),
  })
}

export function useInvalidateSystem() {
  const client = useQueryClient()
  return useCallback((...scope: string[]) => client.invalidateQueries({ queryKey: [...SK, ...scope] }), [client])
}
