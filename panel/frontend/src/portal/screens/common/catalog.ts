import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { z } from 'zod'
import { formatMoney } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { planSchema, type Plan, type PlanKind, type Price } from './catalog-schema'

// GET v1/plans 的形状在 catalog-schema.ts（纯 zod，测试也用），这里转出
export { PLAN_KINDS, type Plan, type PlanKind, type Price } from './catalog-schema'

export const plansSchema = z.object({ plans: z.array(planSchema) })

/** 套餐卡的限速事实：「限速 N Mbps」，不限速为 null（全程生效的按用户限速） */
export const throttleNote = (kbps: number | null) => (kbps === null ? null : `限速 ${Number((kbps / 1000).toFixed(3))} Mbps`)

/** 匿名接口但会解析 Bearer：登录后带令牌才看得到 authenticated / group 套餐，所以走默认的带令牌请求。 */
export function usePlans() {
  const api = useApi()
  return useQuery({
    queryKey: ['portal', 'plans'],
    queryFn: ({ signal }) => api.get('v1/plans', plansSchema, { signal }),
    select: (d) => d.plans,
    meta: { topics: ['plans.changed'] },
  })
}

// ---------------------------------------------------------------------------
// GET v1/payment-methods：只有人民币，没有币种字段；fee / total 只在带了 amount 时回（Go omitempty）
// ---------------------------------------------------------------------------
export const paymentMethodSchema = z.object({
  provider: z.string(),
  method: z.string(),
  label: z.string(),
  fee_fixed: z.number().int().nonnegative(),
  fee_percent: z.string(),
  fee: z.number().int().nonnegative().optional(),
  total: z.number().int().nonnegative().optional(),
})
export type PaymentMethod = z.output<typeof paymentMethodSchema>

export const methodKey = (m: Pick<PaymentMethod, 'provider' | 'method'>) => `${m.provider}:${m.method}`

export function usePaymentMethods(amount?: number) {
  const api = useApi()
  const validAmount = amount && amount > 0 ? amount : undefined
  return useQuery({
    queryKey: ['portal', 'payment-methods', validAmount ?? null],
    queryFn: ({ signal }) =>
      api.get('v1/payment-methods', z.object({ methods: z.array(paymentMethodSchema) }), {
        query: validAmount ? { amount: validAmount } : undefined,
        signal,
      }),
    select: (d) => d.methods,
    placeholderData: keepPreviousData,
    staleTime: 5 * 60_000,
  })
}

// ---------------------------------------------------------------------------
// 价格档：订阅套餐只有 (month,1) (month,3) (month,6) (year,1) 四档；一次性套餐只有 (one_time,1)
// ---------------------------------------------------------------------------
export type PeriodKey = '1m' | '3m' | '6m' | '12m' | 'once' | string

export function periodOf(p: { billing_interval: string; interval_count: number }): PeriodKey {
  const { billing_interval: i, interval_count: n } = p
  if (i === 'one_time') return 'once'
  if (i === 'month' && n === 1) return '1m'
  if (i === 'month' && n === 3) return '3m'
  if (i === 'month' && n === 6) return '6m'
  if ((i === 'year' && n === 1) || (i === 'month' && n === 12)) return '12m'
  return `${i}:${n}`
}

// [档名, 价签后缀里的单位, 折合月数, 「买多久」]
const NAMED: Readonly<Record<string, [string, string, number, string]>> = {
  '1m': ['月付', '月', 1, '1 个月'],
  '3m': ['季付', '季', 3, '3 个月'],
  '6m': ['半年付', '半年', 6, '半年'],
  '12m': ['年付', '年', 12, '1 年'],
}

/** 「月付 / 季付 / 半年付 / 年付 / 一次性」 */
export function periodName(key: PeriodKey): string {
  if (key === 'once') return '一次性'
  return NAMED[key]?.[0] ?? key
}

/** 价签后缀「/ 月」「/ 季」「/ 半年」「/ 年」；一次性没有后缀 */
export function periodUnit(key: PeriodKey): string {
  const named = NAMED[key]
  return named ? `/ ${named[1]}` : ''
}

/** 「买多久」：1 个月 / 3 个月 / 半年 / 1 年；一次性为空串 */
export function periodSpan(key: PeriodKey): string {
  return NAMED[key]?.[3] ?? ''
}

/** 该档折合几个月；一次性与非常规组合为 null */
export const periodMonths = (key: PeriodKey): number | null => NAMED[key]?.[2] ?? null

/** 价格档按时长排：1 个月、3 个月、半年、1 年，一次性与其余排后 */
export const sortPrices = <P extends { billing_interval: string; interval_count: number; unit_amount: number }>(prices: readonly P[]): P[] =>
  [...prices].sort((a, b) => (periodMonths(periodOf(a)) ?? 999) - (periodMonths(periodOf(b)) ?? 999) || a.unit_amount - b.unit_amount)

export function priceFor(plan: Pick<Plan, 'prices'>, key: PeriodKey): Price | undefined {
  return plan.prices.find((p) => periodOf(p) === key)
}

/** 卡片上的大价签：订阅套餐有月付写月付，否则写最短的一档；一次性套餐就是那一个价 */
export function headlinePrice(plan: Pick<Plan, 'prices'>): Price | null {
  return priceFor(plan, '1m') ?? sortPrices(plan.prices)[0] ?? null
}

/** 卡片副文案：月付「按月付费」，多月档「折合 ¥Y / 月」；一次性为空串 */
export function monthlyNote(price: Price): string {
  const months = periodMonths(periodOf(price))
  if (months === 1) return '按月付费'
  if (!months) return ''
  return `折合 ${formatMoney(Math.round(price.unit_amount / months))} / 月`
}

/** 相对「月付 × 月数」省了多少（分）；没有月付价或不省时为 0 */
export function savingAmount(plan: Pick<Plan, 'prices'>, price: Price): number {
  const months = periodMonths(periodOf(price))
  const monthly = priceFor(plan, '1m')
  if (!months || months === 1 || !monthly) return 0
  return Math.max(0, monthly.unit_amount * months - price.unit_amount)
}

// ---------------------------------------------------------------------------
// 套餐事实的说法（选购页卡片、换套餐、确认页共用）。「周期」「订阅」是用户端禁用词：
// 订阅套餐的流量说「每期」，两类套餐叫「按时间」「一次性」。
// ---------------------------------------------------------------------------
/** 两类套餐在用户端的叫法 */
export const KIND_NAME: Readonly<Record<PlanKind, string>> = { subscription: '按时间', one_time: '一次性' }

/** 原型的短写法：8G、100G、1.5T；不到 1G 写 MB */
export function gb(bytes: number): string {
  const g = Math.max(0, bytes) / 1024 ** 3
  if (g >= 1024) return `${trim(g / 1024)}T`
  if (g >= 1) return `${trim(g)}G`
  return `${Math.round(Math.max(0, bytes) / 1024 ** 2)}M`
}
const trim = (n: number) => (n >= 10 ? String(Math.round(n)) : String(Math.round(n * 10) / 10))

/** 流量：订阅套餐「每期 100G」，一次性「共 50G」，不限「不限流量」 */
export function planTraffic(plan: Pick<Plan, 'kind' | 'traffic_bytes'>): string {
  if (plan.traffic_bytes === null) return '不限流量'
  return plan.kind === 'one_time' ? `共 ${gb(plan.traffic_bytes)}` : `每期 ${gb(plan.traffic_bytes)}`
}

/** 一次性的有效期：「有效 30 天」「永久有效」 */
export const validityText = (days: number | null) => (days === null ? '永久有效' : `有效 ${days} 天`)

/** 设备数：「3 台」「不限台数」 */
export const devicesText = (n: number | null) => (n === null ? '不限台数' : `${n} 台`)

/** 流量重置的价目：「流量重置 ¥10 / 次，每期最多 2 次」；一次性说「有效期内」；没开为 null */
export function resetPriceText(plan: Pick<Plan, 'kind' | 'reset'>): string | null {
  if (!plan.reset) return null
  const price = `流量重置 ${formatMoney(plan.reset.price)} / 次`
  if (plan.reset.limit === null) return `${price}，不限次数`
  return `${price}，${plan.kind === 'one_time' ? '有效期内' : '每期'}最多 ${plan.reset.limit} 次`
}
