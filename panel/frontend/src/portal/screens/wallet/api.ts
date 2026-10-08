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

export { giftCardSchema, placementOptionSchema, placementSchema, PLACEMENT_KINDS, type GiftCard, type Placement, type PlacementKind, type PlacementOption } from './schemas'
import { giftCardSchema, type PlacementKind } from './schemas'

export function useGiftPreview() {
  const api = useApi()
  return useMutation({
    mutationFn: (code: string) => api.post('v1/gift-cards/preview', z.object({ card: giftCardSchema }), { body: { code } }),
  })
}

// ---------------------------------------------------------------------------
// POST v1/gift-cards/redeem（修订 R31；设计稿 2.5 带 choice：preview 选项里选定的那一项，只有一项时可不传）
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
  // Redeem 在 summary 为空时直接报错，成功响应一定是非空数组。服务端的话术里有「订阅」等词，
  // 门户不直接显示，完成页按选定的用法自己写
  summary: z.array(z.string()),
})

export interface RedeemBody {
  code: string
  choice?: { kind: PlacementKind; subscription_id?: string }
}

export type RedeemResult = z.output<typeof redeemResultSchema>

export function useRedeemGift() {
  const api = useApi()
  const client = useQueryClient()
  return useMutation({
    mutationFn: ({ body, key }: { body: RedeemBody; key: string }) => api.post('v1/gift-cards/redeem', redeemResultSchema, { body, idempotencyKey: key }),
    // 礼品卡改余额、订阅、流量包，都没有推送：主动失效整个门户前缀
    onSuccess: () => void client.invalidateQueries({ queryKey: ['portal'] }),
  })
}

// ---------------------------------------------------------------------------
// GET v1/me/gift-cards（修订 R68：行带 code_hint，Go 无 omitempty、恒在；奖励字段 Go 带 omitempty，为零时缺席）
// ---------------------------------------------------------------------------
export const redemptionSchema = z.object({
  template_name: z.string(),
  type: z.string(),
  code_hint: z.string(),
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
    // MyRedemptions 以空切片初始化，没有记录时回 []，不是 null
    queryFn: ({ signal }) => api.get('v1/me/gift-cards', z.object({ redemptions: z.array(redemptionSchema) }), { signal }),
    select: (d) => d.redemptions,
  })
}
