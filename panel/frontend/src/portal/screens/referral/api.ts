/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useMutation / useQuery / useQueryClient，依赖 zod，依赖 ../../../shell/runtime 的 useApi，依赖 ../../queries 的 COMMISSION_KEY
 * [OUTPUT]: 对外提供 inviteSchema / Invite、useInvite、useTransferCommission、useRequestWithdrawal
 * [POS]: portal/screens/referral 的数据层（契约门户-06，含修订 R5、R7）：我的邀请码与被邀请人、佣金转余额（幂等 commission_transfer_to_balance）、申请提现（幂等 commission_withdrawal_request）；佣金概况在外框 queries.ts（与头像菜单同键）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../../../shell/runtime'
import { COMMISSION_KEY } from '../../queries'

// ---------------------------------------------------------------------------
// GET v1/me/invite：没有活动邀请码时后端懒生成一个（会写库的 GET，重复请求拿到同一个码）
// invitees 最多 100 条、按 bound_at 倒序；risk_flag 是风控内部判定，schema 收下但页面不展示
// ---------------------------------------------------------------------------
export const inviteSchema = z.object({
  invite: z.object({ code: z.string(), invited: z.number().int(), max_uses: z.number().int().nullable(), created_at: z.string() }),
  invitees: z.array(z.object({ email: z.string(), bound_at: z.string(), risk_flag: z.enum(['none', 'suspicious', 'confirmed_fraud']) })),
})
export type Invite = z.output<typeof inviteSchema>

export function useInvite() {
  const api = useApi()
  return useQuery({
    queryKey: ['portal', 'invite'],
    queryFn: ({ signal }) => api.get('v1/me/invite', inviteSchema, { signal }),
    staleTime: 60_000,
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
    mutationFn: ({ body, key }: { body: { amount: number; payout_detail: string }; key: string }) =>
      api.post('v1/me/withdrawals', z.object({ id: z.string() }), { body, idempotencyKey: key }),
    onSettled: () => void client.invalidateQueries({ queryKey: COMMISSION_KEY }),
  })
}
