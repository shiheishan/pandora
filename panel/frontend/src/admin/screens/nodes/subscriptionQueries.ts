import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'
import { useApi } from '../../../shell/runtime'
import { subscriptionExtrasSchema, templateDetailSchema, templateListSchema } from './subscription'

export { useFailure } from '../../actions'

export const SUB_KEY = ['admin', 'nodes', 'subscription'] as const

export function useSubscriptionExtras() {
  const api = useApi()
  return useQuery({
    queryKey: [...SUB_KEY, 'extras'],
    queryFn: ({ signal }) => api.get('v1/settings/subscription', subscriptionExtrasSchema, { signal }),
  })
}

export function useSubscriptionTemplates() {
  const api = useApi()
  return useQuery({
    queryKey: [...SUB_KEY, 'templates'],
    queryFn: ({ signal }) => api.get('v1/subscription-templates', templateListSchema, { signal }).then((r) => r.templates),
  })
}

export function useSubscriptionTemplate(format: string) {
  const api = useApi()
  return useQuery({
    queryKey: [...SUB_KEY, 'template', format],
    queryFn: ({ signal }) => api.get(`v1/subscription-templates/${encodeURIComponent(format)}`, templateDetailSchema, { signal }),
  })
}

export function useInvalidateSubscription() {
  const client = useQueryClient()
  return useCallback(() => client.invalidateQueries({ queryKey: SUB_KEY }), [client])
}
