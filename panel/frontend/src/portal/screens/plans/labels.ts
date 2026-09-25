/**
 * [INPUT]: 依赖 ../../../core/format 的 formatMoney，依赖 ../../../core/router 的 href，依赖 ../common/catalog 的周期与价格函数，依赖 ../../queries 的 Subscription 类型
 * [OUTPUT]: 对外提供 availablePeriods、fromPrice、planAction、PlanAction
 * [POS]: portal/screens/plans 的纯逻辑：周期分段有哪几档（按设计稿三档排序、年付标出最小省幅）、标签页上的「¥X 起 / 月」、每张套餐卡的入口（续费 / 变更 / 新购 / 不可用）；有单元测试
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { formatMoney } from '../../../core/format'
import { href } from '../../../core/router'
import type { Subscription } from '../../queries'
import { cnyPrices, periodName, periodOf, PERIODS, periodUnit, savingPercent, type Plan, type PeriodKey, type Price } from '../common/catalog'

/** 所有套餐 CNY 价格里出现过的周期：设计稿三档在前，其余按首次出现排后；多月档带「省 N%」 */
export function availablePeriods(plans: readonly Plan[]): Array<{ key: PeriodKey; label: string }> {
  const seen = new Set<PeriodKey>()
  for (const plan of plans) for (const p of cnyPrices(plan)) seen.add(periodOf(p))
  const keys = [...PERIODS.filter((k) => seen.has(k)), ...[...seen].filter((k) => !PERIODS.includes(k))]
  return keys.map((key) => {
    const pct = savingPercent(plans, key)
    return { key, label: pct > 0 ? `${periodName(key)} · 省 ${pct}%` : periodName(key) }
  })
}

/** 标签页副标题「 · ¥29 起 / 月」：有月付取最低月付，否则取最低价及其周期；没有 CNY 价格返回空串 */
export function fromPrice(plans: readonly Plan[]): string {
  const all = plans.flatMap((p) => cnyPrices(p))
  const monthly = all.filter((p) => periodOf(p) === '1m')
  const pool = monthly.length ? monthly : all
  if (!pool.length) return ''
  const min = pool.reduce((a, b) => (b.unit_amount < a.unit_amount ? b : a))
  return ` · ${formatMoney(min.unit_amount, min.currency)} 起 ${periodUnit(periodOf(min))}`.trimEnd()
}

export interface PlanAction {
  label: string
  /** null = 按钮置灰 */
  href: string | null
}

/**
 * 套餐卡入口（契约门户-03 POST v1/orders 设计映射）：当前套餐 → 续费；已有生效订阅且选别的 → 变更
 * （目标 allow_upgrade=false 不可变更）；没有订阅 → 新购。结账页按同一规则再判一次模式。
 */
export function planAction(plan: Plan, price: Price | undefined, primary: Subscription | null, period: PeriodKey, renewable: boolean): PlanAction {
  if (!price) return { label: `暂无${periodName(period)}`, href: null }
  if (primary && primary.plan_id === plan.id) {
    if (plan.allow_renewal === false || !renewable) return { label: '不可续费', href: null }
    return { label: '续费', href: href('/checkout', { renew: primary.id, price: price.id }) }
  }
  if (primary && plan.allow_upgrade === false) return { label: '不支持变更', href: null }
  return { label: `${primary ? '换成' : '选择'}${plan.name}`, href: href('/checkout', { plan: plan.id, price: price.id }) }
}
