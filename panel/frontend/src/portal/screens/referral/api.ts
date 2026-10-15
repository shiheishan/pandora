import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../../../shell/runtime'
import { COMMISSION_KEY } from '../../queries'
import { inviteCodeSchema, inviteSchema } from './schemas'

export { inviteCodeSchema, inviteSchema, type Invite, type InviteCode } from './schemas'

export function useInvite() {
  const api = useApi()
  return useQuery({
    queryKey: ['portal', 'invite'],
    queryFn: ({ signal }) => api.get('v1/me/invite', inviteSchema, { signal }),
    staleTime: 60_000,
  })
}

export function useCreateInviteCode() {
  const api = useApi()
  const client = useQueryClient()
  return useMutation({
    mutationFn: () => api.post('v1/me/invite/codes', inviteCodeSchema, { body: {} }),
    onSettled: () => void client.invalidateQueries({ queryKey: ['portal', 'invite'] }),
  })
}

// ---------------------------------------------------------------------------
// 两个写操作都动钱、都没有推送：成功后主动失效佣金（转余额还要失效余额与流水）
// ---------------------------------------------------------------------------
export function useTransferCommission() {
  const api = useApi()
  const client = useQueryClient()
  return useMutation({
    mutationFn: ({ amount, key }: { amount: number; key: string }) =>
      api.post('v1/me/commission/transfer', z.object({ ledger_txn_id: z.string(), amount: z.number().int() }), { body: { amount }, idempotencyKey: key }),
    // 409 说明可用额已变（别处转过或提过），同样要重拉
    onSettled: () => {
      void client.invalidateQueries({ queryKey: COMMISSION_KEY })
      void client.invalidateQueries({ queryKey: ['portal', 'balance'] })
    },
  })
}

export function useRequestWithdrawal() {
  const api = useApi()
  const client = useQueryClient()
  return useMutation({
    mutationFn: ({ body, key }: { body: { amount: number; method: string; payout_detail: string }; key: string }) =>
      api.post('v1/me/withdrawals', z.object({ id: z.string() }), { body, idempotencyKey: key }),
    onSettled: () => void client.invalidateQueries({ queryKey: COMMISSION_KEY }),
  })
}
