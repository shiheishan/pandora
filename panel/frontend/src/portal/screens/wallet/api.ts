/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useMutation / useQuery / useQueryClient，依赖 zod，依赖 ../../../shell/runtime 的 useApi
 * [OUTPUT]: 对外提供 giftCardSchema / GiftCard、redemptionSchema / Redemption、redeemResultSchema、useTopup、useGiftPreview、useRedeemGift、useMyGiftCards
 * [POS]: portal/screens/wallet 的数据层（契约门户-05，含修订 R31、R68）：充值建单（幂等 balance_topup_create，响应 200）、礼品卡预览与兑换（幂等 gift_card_redeem）、我的兑换记录；余额与流水在外框 queries.ts（与顶栏同键）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../../../shell/runtime'

// ---------------------------------------------------------------------------
// POST v1/me/topups：{ amount 分 } → 200 { order_id, order_no, amount, currency }，之后走支付弹窗
// ---------------------------------------------------------------------------
const topupSchema = z.object({ order_id: z.string(), order_no: z.string(), amount: z.number().int(), currency: z.string() })

export function useTopup() {
  const api = useApi()
  return useMutation({
    mutationFn: ({ amount, key }: { amount: number; key: string }) => api.post('v1/me/topups', topupSchema, { body: { amount }, idempotencyKey: key }),
  })
}

// ---------------------------------------------------------------------------
// POST v1/gift-cards/preview（修订 R68：不回发行量，套餐卡补套餐名与周期）
// ---------------------------------------------------------------------------
export const giftCardSchema = z.object({
  id: z.string(),
  name: z.string(),
  description: z.string(),
  type: z.enum(['general', 'plan', 'mystery']),
  status: z.string(),
  rewards: z.object({
    balance: z.number().int().optional(),
    traffic_bytes: z.number().int().optional(),
    expire_days: z.number().int().optional(),
    reset_quota: z.boolean().optional(),
    plan_id: z.string().optional(),
    price_id: z.string().optional(),
    pool: z.array(z.object({ label: z.string(), weight: z.number() })).optional(),
  }),
  conditions: z.record(z.string(), z.unknown()),
  limits: z.record(z.string(), z.unknown()),
  theme_color: z.string(),
  created_at: z.string(),
  plan_name: z.string().optional(),
  interval: z.string().optional(),
  interval_count: z.number().int().optional(),
})
export type GiftCard = z.output<typeof giftCardSchema>

export function useGiftPreview() {
  const api = useApi()
  return useMutation({
    mutationFn: (code: string) => api.post('v1/gift-cards/preview', z.object({ card: giftCardSchema }), { body: { code } }),
  })
}

// ---------------------------------------------------------------------------
// POST v1/gift-cards/redeem（修订 R31：流量奖励发流量包余额，不再要求有订阅）
// ---------------------------------------------------------------------------
export const redeemResultSchema = z.object({
  template_name: z.string(),
  type: z.string(),
  prize_label: z.string().optional(),
  balance: z.number().int().optional(),
  traffic_bytes: z.number().int().optional(),
  expire_days: z.number().int().optional(),
  quota_reset: z.boolean().optional(),
  plan_granted: z.string().optional(),
  // Go 的 nil 切片会编码成 null
  summary: z.array(z.string()).nullable(),
})

export function useRedeemGift() {
  const api = useApi()
  const client = useQueryClient()
  return useMutation({
    mutationFn: ({ code, key }: { code: string; key: string }) => api.post('v1/gift-cards/redeem', redeemResultSchema, { body: { code }, idempotencyKey: key }),
    // 礼品卡改余额、订阅、流量包，都没有推送：主动失效整个门户前缀
    onSuccess: () => void client.invalidateQueries({ queryKey: ['portal'] }),
  })
}

// ---------------------------------------------------------------------------
// GET v1/me/gift-cards（修订 R68：行带 code_hint）
// ---------------------------------------------------------------------------
export const redemptionSchema = z.object({
  template_name: z.string(),
  type: z.string(),
  code_hint: z.string().optional(),
  prize_label: z.string().optional(),
  balance: z.number().int().optional(),
  traffic_bytes: z.number().int().optional(),
  expire_days: z.number().int().optional(),
  redeemed_at: z.string(),
})
export type Redemption = z.output<typeof redemptionSchema>

export function useMyGiftCards() {
  const api = useApi()
  return useQuery({
    queryKey: ['portal', 'gift-cards'],
    queryFn: ({ signal }) => api.get('v1/me/gift-cards', z.object({ redemptions: z.array(redemptionSchema).nullable() }), { signal }),
    select: (d) => d.redemptions ?? [],
  })
}
