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
