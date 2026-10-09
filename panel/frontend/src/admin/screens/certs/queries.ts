import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'
import { useApi } from '../../../shell/runtime'
import { acmeSettingsSchema, certificateDetailSchema, certificateListSchema, dnsCredentialListSchema } from './schemas'

export { useCan, useFailure, useIntentKey } from '../../actions'

export const CK = ['admin', 'certs'] as const

/** 有进行中的订单时 5 秒重拉一次，签发完成后自动停 */
const busyInterval = (busy: boolean) => (busy ? 5000 : false)

export function useCertificates() {
  const api = useApi()
  return useQuery({
    queryKey: [...CK, 'list'],
    queryFn: ({ signal }) => api.get('v1/certificates', certificateListSchema, { signal }),
    refetchInterval: (q) => busyInterval(q.state.data?.items.some((c) => c.active_order !== null) ?? false),
  })
}

export function useCertificate(id: string) {
  const api = useApi()
  return useQuery({
    queryKey: [...CK, 'detail', id],
    queryFn: ({ signal }) => api.get(`v1/certificates/${encodeURIComponent(id)}`, certificateDetailSchema, { signal }),
    refetchInterval: (q) => busyInterval(q.state.data?.certificate.active_order != null),
  })
}

export function useDnsCredentials() {
  const api = useApi()
  return useQuery({
    queryKey: [...CK, 'dns'],
    queryFn: ({ signal }) => api.get('v1/dns-credentials', dnsCredentialListSchema, { signal }).then((r) => r.items),
  })
}

export function useAcmeSettings() {
  const api = useApi()
  return useQuery({ queryKey: [...CK, 'acme'], queryFn: ({ signal }) => api.get('v1/settings/acme', acmeSettingsSchema, { signal }) })
}

/** 写后按前缀整体失效：证书与凭据互相引用（引用数、凭据名、恢复的证书），一起刷 */
export function useInvalidateCerts() {
  const client = useQueryClient()
  return useCallback(() => client.invalidateQueries({ queryKey: CK }), [client])
}
