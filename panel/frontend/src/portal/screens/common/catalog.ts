/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useQuery，依赖 zod，依赖 ./catalog-schema 的套餐 schema，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../core/format 的 formatMoney
 * [OUTPUT]: 对外提供套餐目录 / 流量包目录 / 支付方式的 schema、类型与查询（usePlans、usePackCatalog、usePaymentMethods），周期映射 periodOf / PERIODS / periodName / periodUnit，价格文案 monthlyNote / savingPercent / perGbNote，额度文案 trafficQuotaOf / resetNote / quotaPeriodNote / throttleNote，cnyPrices / priceFor / savingAmount / periodMonths / methodKey；plansSchema 为 tests/smoke 形状冒烟导出
 * [POS]: portal/screens/common 的商品目录层（契约门户-03 与外壳的 payment-methods）：选购页与结账页共用；只展示 CNY 价格（余额与 epay 只有 CNY），周期把 (month,3)|(quarter,1)、(year,1)|(month,12) 归成同一档；套餐行含 R99 限速与 R100 卖点 / 推荐
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'
import { formatMoney } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { planSchema, type Plan, type Price } from './catalog-schema'

// GET v1/plans 的形状在 catalog-schema.ts（纯 zod，测试也用），这里转出
export { planSchema, priceSchema, RESET_STRATEGIES, type Plan, type Price } from './catalog-schema'

export const plansSchema = z.object({ plans: z.array(planSchema) })

/** 套餐卡的限速事实：「限速 N Mbps」，不限速为 null（R99：全程生效的按用户限速） */
export const throttleNote = (kbps: number | null) => (kbps === null ? null : `限速 ${Number((kbps / 1000).toFixed(3))} Mbps`)

/** 匿名接口但会解析 Bearer：登录后带令牌才看得到 authenticated / group 套餐与组专属价格，所以走默认的带令牌请求。 */
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
// GET v1/traffic-packs（修订 R30）
// ---------------------------------------------------------------------------
export const packSchema = z.object({
  id: z.string(),
  name: z.string(),
  traffic_bytes: z.number().int(),
  currency: z.string(),
  unit_amount: z.number().int(),
  recommended: z.boolean(),
})
export type Pack = z.output<typeof packSchema>

export function usePackCatalog() {
  const api = useApi()
  return useQuery({
    queryKey: ['portal', 'traffic-pack-catalog'],
    queryFn: ({ signal }) => api.get('v1/traffic-packs', z.object({ packs: z.array(packSchema) }), { signal }),
    select: (d) => d.packs,
    meta: { topics: ['plans.changed'] },
  })
}

// ---------------------------------------------------------------------------
// GET v1/payment-methods（修订 R61）：只列能收 CNY 的方式
// ---------------------------------------------------------------------------
export const paymentMethodSchema = z.object({ provider: z.string(), method: z.string(), label: z.string(), currencies: z.array(z.string()) })
export type PaymentMethod = z.output<typeof paymentMethodSchema>

export const methodKey = (m: Pick<PaymentMethod, 'provider' | 'method'>) => `${m.provider}:${m.method}`

export function usePaymentMethods() {
  const api = useApi()
  return useQuery({
    queryKey: ['portal', 'payment-methods'],
    queryFn: ({ signal }) => api.get('v1/payment-methods', z.object({ methods: z.array(paymentMethodSchema) }), { signal }),
    select: (d) => d.methods.filter((m) => m.currencies.includes('CNY')),
    staleTime: 5 * 60_000,
  })
}

// ---------------------------------------------------------------------------
// 周期：设计稿三档 1m / 3m / 12m，其余组合走通用文案
// ---------------------------------------------------------------------------
export type PeriodKey = '1m' | '3m' | '12m' | string

export function periodOf(p: Pick<Price, 'billing_interval' | 'interval_count'>): PeriodKey {
  const { billing_interval: i, interval_count: n } = p
  if (i === 'month' && n === 1) return '1m'
  if ((i === 'month' && n === 3) || (i === 'quarter' && n === 1)) return '3m'
  if ((i === 'year' && n === 1) || (i === 'month' && n === 12)) return '12m'
  return `${i}:${n}`
}

/** 设计稿的三档，按这个顺序排；其余组合排在后面 */
export const PERIODS: readonly PeriodKey[] = ['1m', '3m', '12m']
const NAMED: Readonly<Record<string, [string, string, number]>> = {
  '1m': ['月付', '月', 1],
  '3m': ['季付', '季', 3],
  '12m': ['年付', '年', 12],
}
const UNIT: Readonly<Record<string, string>> = { day: '天', week: '周', month: '个月', quarter: '个季度', year: '年' }

/** 「月付 / 季付 / 年付」，通用组合为「每 2 个月」「一次性」 */
export function periodName(key: PeriodKey): string {
  const named = NAMED[key]
  if (named) return named[0]
  const [i = '', n = '1'] = key.split(':')
  if (i === 'one_time') return '一次性'
  return `每 ${n} ${UNIT[i] ?? i}`
}

/** 价格后缀「/ 月」「/ 季」「/ 年」，通用组合为「/ 2 个月」 */
export function periodUnit(key: PeriodKey): string {
  const named = NAMED[key]
  if (named) return `/ ${named[1]}`
  const [i = '', n = '1'] = key.split(':')
  if (i === 'one_time') return ''
  return `/ ${n === '1' ? '' : `${n} `}${UNIT[i] ?? i}`
}

/** 该档折合几个月；通用组合无法按月比较时为 null */
export const periodMonths = (key: PeriodKey): number | null => NAMED[key]?.[2] ?? null

export const cnyPrices = (plan: Pick<Plan, 'prices'>): Price[] => plan.prices.filter((p) => p.currency === 'CNY')

export function priceFor(plan: Pick<Plan, 'prices'>, key: PeriodKey): Price | undefined {
  return cnyPrices(plan).find((p) => periodOf(p) === key)
}

/** 卡片副文案：月付「按月付费」（后端无自动续费，契约门户-03 改文案），多月档「折合 ¥Y / 月」 */
export function monthlyNote(price: Price): string {
  const months = periodMonths(periodOf(price))
  if (months === 1) return '按月付费'
  if (!months) return ''
  return `折合 ${formatMoney(Math.round(price.unit_amount / months), price.currency)} / 月`
}

/** 相对「月付 × 月数」省了多少（分）；没有月付价或不省时为 0 */
export function savingAmount(plan: Pick<Plan, 'prices'>, price: Price): number {
  const months = periodMonths(periodOf(price))
  const monthly = priceFor(plan, '1m')
  if (!months || months === 1 || !monthly) return 0
  return Math.max(0, monthly.unit_amount * months - price.unit_amount)
}

/**
 * 分段按钮上的「年付 · 省 N%」：取所有同时有月付与该档价格的套餐里最小的省幅，
 * 保证每个套餐都至少省这么多，不夸大；算不出或为 0 时返回 0。
 */
export function savingPercent(plans: ReadonlyArray<Pick<Plan, 'prices'>>, key: PeriodKey): number {
  const months = periodMonths(key)
  if (!months || months === 1) return 0
  const pct = plans.flatMap((plan) => {
    const monthly = priceFor(plan, '1m')
    const price = priceFor(plan, key)
    return monthly && price ? [Math.floor((1 - price.unit_amount / (monthly.unit_amount * months)) * 100)] : []
  })
  return pct.length ? Math.max(0, Math.min(...pct)) : 0
}

/** 流量包「约 ¥0.25 / GB」（1<<30 字节为 1 GB，与 formatBytes 同口径） */
export function perGbNote(pack: Pick<Pack, 'unit_amount' | 'traffic_bytes' | 'currency'>): string {
  const gb = pack.traffic_bytes / 1024 ** 3
  if (gb <= 0) return ''
  return `约 ${formatMoney(Math.round(pack.unit_amount / gb), pack.currency)} / GB`
}

// ---------------------------------------------------------------------------
// 额度文案：GB / 月 取 traffic.bytes 的 limit；重置按 quota_reset_strategy
// ---------------------------------------------------------------------------
export function trafficQuotaOf(plan: Pick<Plan, 'quotas'>) {
  return plan.quotas.find((q) => q.metric === 'traffic.bytes') ?? null
}

/** 契约门户-03：never「不重置」、natural_month「每月 1 日重置」、fixed_day「每月 N 日重置」；billing_cycle 与缺席为「到期日自动重置」 */
export function resetNote(plan: Pick<Plan, 'quota_reset_strategy' | 'quota_reset_day'>): string {
  switch (plan.quota_reset_strategy) {
    case 'never':
      return '不重置'
    case 'natural_month':
      return '每月 1 日重置'
    case 'fixed_day':
      return plan.quota_reset_day ? `每月 ${plan.quota_reset_day} 日重置` : '每月固定日重置'
    default:
      return '到期日自动重置'
  }
}

/**
 * 额度周期后缀：month 与按月重置的 cycle 为「/ 月」，day「/ 天」，total「总量」；
 * 按计费周期重置（billing_cycle 或旧后端缺席）的 cycle 跟着所选价格走，年付即「/ 年」。
 */
export function quotaPeriodNote(plan: Pick<Plan, 'quota_reset_strategy'>, period: 'total' | 'cycle' | 'day' | 'month', key: PeriodKey): string {
  if (period === 'day') return '/ 天'
  if (period === 'total') return '总量'
  if (period === 'month' || plan.quota_reset_strategy === 'natural_month' || plan.quota_reset_strategy === 'fixed_day') return '/ 月'
  return periodUnit(key) || '总量'
}
