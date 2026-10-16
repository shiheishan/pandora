import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../../../shell/runtime'

// ---------------------------------------------------------------------------
// GET v1/me/checkout/landings?plan_id=（Go order/handler 的 landingsResponse）：买这个套餐能放到哪里。
// 由用户选，不替用户猜；只有一个选项时不用选。new 永远在第一个；subscription_id 只有 new 时缺席（omitempty）。
//   new                     另开一份（新链接）
//   renew   <一份>          同款的订阅套餐：续在那一份上（链接不变）
//   stack   <一份>          同款的一次性套餐：叠加到那一份（总量加上、天数顺延，链接不变）
//   change  <一份>          别的套餐：把那一份换成这个（可跨类型，链接不变）
// 挂着未完结续费、叠加或换套餐单的那份不列；套餐关闭新购时回 409（原话显示）。
// ---------------------------------------------------------------------------
export const LANDING_MODES = ['new', 'renew', 'stack', 'change'] as const
export type LandingMode = (typeof LANDING_MODES)[number]

const landingSchema = z.object({ mode: z.enum(LANDING_MODES), subscription_id: z.string().optional() })
export type Landing = z.output<typeof landingSchema>

export const landingsSchema = z.object({ options: z.array(landingSchema) })

export function useLandings(planId: string | null) {
  const api = useApi()
  return useQuery({
    queryKey: ['portal', 'landings', planId],
    queryFn: ({ signal }) => api.get('v1/me/checkout/landings', landingsSchema, { query: { plan_id: planId! }, signal }),
    enabled: planId !== null,
    select: (d) => d.options,
    retry: false,
    meta: { topics: ['orders.changed', 'subscriptions.changed'] },
  })
}
