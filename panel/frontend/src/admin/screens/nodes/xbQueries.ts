import { useQuery } from '@tanstack/react-query'
import { useApi } from '../../../shell/runtime'
import { echSchema, relaysResponse } from './schemas'

export function useNodeRelays(id: string) {
  const api = useApi()
  return useQuery({
    queryKey: ['admin', 'nodes', 'relays', id],
    queryFn: ({ signal }) => api.get(`v1/nodes/${id}/relays`, relaysResponse, { signal }).then((r) => r.relays),
  })
}

export function useNodeECH(id: string) {
  const api = useApi()
  return useQuery({
    queryKey: ['admin', 'nodes', 'ech', id],
    queryFn: ({ signal }) => api.get(`v1/nodes/${id}/ech`, echSchema, { signal }),
  })
}
