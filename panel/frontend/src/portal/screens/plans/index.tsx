/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../core/format 的 formatBytes / formatMoney，依赖 ../../../core/router 的 href / navigate / useHashLocation，依赖 ../../../ui 的 Card / Empty / Segmented / Skeleton / Tag，依赖 ../../queries 的 useSubscriptions / pickPrimary，依赖 ../common 的目录、订阅、流量文案（compactBytes）、插槽与 LoadError，依赖 ./labels 的纯函数
 * [OUTPUT]: 默认导出 Plans 页面组件（登记表 React.lazy 的目标）
 * [POS]: portal/screens/plans 的入口：选购套餐（门户-03 列表部分）。两个标签——订阅套餐（周期分段、套餐卡：价格、折合月价、流量与重置、设备数、当前 / 续费 / 变更入口）与流量包（容量卡、约每 GB 单价、「最划算」、使用规则）；#/plans?tab=packs 直达流量包。D-E-3 未决：特性列表只列后端已有事实、不显示「推荐」
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { formatBytes, formatMoney } from '../../../core/format'
import { href, navigate, useHashLocation } from '../../../core/router'
import { Card, Empty, Segmented, Skeleton, Tag } from '../../../ui'
import { pickPrimary, useSubscriptions, type Subscription } from '../../queries'
import { LoadError, Slot } from '../common/Blocks'
import {
  monthlyNote,
  periodName,
  periodUnit,
  perGbNote,
  priceFor,
  quotaPeriodNote,
  resetNote,
  trafficQuotaOf,
  usePackCatalog,
  usePlans,
  type Pack,
  type Plan,
  type PeriodKey,
} from '../common/catalog'
import { canRenew, useTrafficPacks } from '../common/subscriptions'
import { compactBytes, expiryInfo, pickTrafficQuota } from '../common/traffic'
import { availablePeriods, fromPrice, planAction } from './labels'
import css from './Plans.module.css'

type Tab = 'subs' | 'packs'

export default function Plans() {
  const { query } = useHashLocation()
  const tab: Tab = query.get('tab') === 'packs' ? 'packs' : 'subs'
  const plans = usePlans()
  const packs = usePackCatalog()
  const primary = useSubscriptions(pickPrimary).data ?? null

  const tabs: Array<{ key: Tab; title: string; tag: string }> = [
    { key: 'subs', title: '订阅套餐', tag: `按时间${plans.data ? fromPrice(plans.data) : ''}` },
    { key: 'packs', title: '流量包', tag: `按流量${packs.data?.length ? ` · ${formatMoney(Math.min(...packs.data.map((p) => p.unit_amount)), 'CNY')} 起` : ''}` },
  ]

  return (
    <div className={css.page}>
      <Slot name="portal.plans.notice" />
      <div className={css.kinds}>
        <div className={css.kindTabs} role="tablist" aria-label="商品类型">
          {tabs.map((t) => (
            <button
              key={t.key}
              type="button"
              role="tab"
              aria-selected={t.key === tab}
              className={css.kind}
              onClick={() => navigate('/plans', { query: t.key === 'packs' ? { tab: 'packs' } : undefined, replace: true })}
            >
              <span className={css.kindTitle}>{t.title}</span>
              <span className={css.kindTag}>{t.tag}</span>
            </button>
          ))}
        </div>
        <div className={css.kindDesc}>{tab === 'packs' ? '买完立即生效，不限时间，用完为止。适合偶尔用，或订阅流量不够时补充。' : '每期送固定流量，按周期自动重置。适合每天都在用。'}</div>
      </div>
      {tab === 'subs' ? <SubscriptionPlans plans={plans} primary={primary} /> : <PackList packs={packs} primary={primary} />}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 订阅套餐
// ---------------------------------------------------------------------------
function SubscriptionPlans({ plans, primary }: { plans: ReturnType<typeof usePlans>; primary: Subscription | null }) {
  const [chosen, setChosen] = useState<PeriodKey | null>(null)
  if (plans.isPending) return <CardsSkeleton count={3} />
  if (plans.isError) return <LoadError error={plans.error} onRetry={() => void plans.refetch()} what="套餐" />
  if (plans.data.length === 0) return <Empty title="暂时没有可购买的套餐" description="新套餐上架后会显示在这里，也可以先看看流量包。" action={<a href={href('/plans', { tab: 'packs' })}>看看流量包</a>} />

  const periods = availablePeriods(plans.data)
  const period = chosen && periods.some((p) => p.key === chosen) ? chosen : (periods[0]?.key ?? '1m')
  const expiry = primary ? expiryInfo(primary.current_period_end) : null

  return (
    <>
      <div className={css.periodRow}>
        {periods.length > 1 && <Segmented label="计费周期" value={period} onChange={setChosen} options={periods.map((p) => ({ value: p.key, label: p.label }))} />}
        {primary && (
          <div className={css.hint}>
            当前 {primary.plan_name}
            {expiry ? ` · ${expiry.days} 天后到期` : ''}。换套餐时剩余天数自动折算。
          </div>
        )}
      </div>
      <div className={css.planGrid}>
        {plans.data.map((plan) => (
          <PlanCard key={plan.id} plan={plan} period={period} primary={primary} />
        ))}
      </div>
    </>
  )
}

function PlanCard({ plan, period, primary }: { plan: Plan; period: PeriodKey; primary: Subscription | null }) {
  const price = priceFor(plan, period)
  const quota = trafficQuotaOf(plan)
  const current = primary?.plan_id === plan.id
  const action = planAction(plan, price, primary, period, primary ? canRenew(primary) : false)

  return (
    <Card className={css.planCard}>
      <div className={css.planHead}>
        <span className={css.planName}>{plan.name}</span>
        {current && <Tag tone="ok">当前</Tag>}
      </div>
      {plan.description && <div className={css.planDesc}>{plan.description}</div>}
      <div className={css.priceBlock}>
        {price ? (
          <>
            <div className={css.priceRow}>
              <span className={css.price}>{formatMoney(price.unit_amount, price.currency)}</span>
              <span className={css.per}>{periodUnit(period)}</span>
            </div>
            <div className={css.monthly}>{monthlyNote(price)}</div>
          </>
        ) : (
          <div className={css.monthly}>暂不提供{periodName(period)}</div>
        )}
      </div>
      <div className={css.facts}>
        <div className={css.traffic}>
          <span className={css.trafficValue}>{!quota || quota.limit === null ? '不限流量' : `${compactBytes(quota.limit)} ${quotaPeriodNote(plan, quota.period, period)}`}</span>
          {quota && quota.limit !== null && quota.period !== 'total' && <span className={css.reset}>{resetNote(plan)}</span>}
        </div>
        <div className={css.fact}>
          <span aria-hidden="true">—</span>
          <span>{plan.max_devices === null ? '不限设备数' : `${plan.max_devices} 台设备同时在线`}</span>
        </div>
      </div>
      {action.href ? (
        <a className={css.cta} href={action.href}>
          {action.label}
        </a>
      ) : (
        <span className={css.ctaDisabled} aria-disabled="true">
          {action.label}
        </span>
      )}
    </Card>
  )
}

// ---------------------------------------------------------------------------
// 流量包：挂在用户身上、永不过期、可叠加（5.A D-E-1）
// ---------------------------------------------------------------------------
const PACK_RULES = [
  ['立即生效，用完为止', '支付成功马上到账，没有有效期，也不按月清零。'],
  ['先用订阅流量', '有订阅时，订阅流量用完才开始扣流量包。'],
  ['可以叠加，到期不清零', '多次购买的容量累加；订阅到期或续费后余量保留，有生效订阅时才会使用。'],
] as const

function PackList({ packs, primary }: { packs: ReturnType<typeof usePackCatalog>; primary: Subscription | null }) {
  const mine = useTrafficPacks()
  if (packs.isPending) return <CardsSkeleton count={4} />
  if (packs.isError) return <LoadError error={packs.error} onRetry={() => void packs.refetch()} what="流量包" />

  const quota = primary ? pickTrafficQuota(primary.quotas) : null
  const hints: string[] = []
  if (primary && quota && quota.remaining !== null) hints.push(`您的${primary.plan_name}本期还剩 ${formatBytes(Math.max(0, quota.remaining))}。买了流量包后，会先用完订阅流量，再自动接着用流量包。`)
  if (mine.data && mine.data.remaining_bytes_total > 0) hints.push(`现有流量包余量 ${compactBytes(mine.data.remaining_bytes_total)}。`)

  return (
    <>
      {hints.length > 0 && <div className={css.hint}>{hints.join('')}</div>}
      {packs.data.length === 0 ? (
        <Empty title="暂时没有可购买的流量包" description="流量包上架后会显示在这里。" />
      ) : (
        <div className={css.packGrid}>
          {packs.data.map((p) => (
            <PackCard key={p.id} pack={p} />
          ))}
        </div>
      )}
      <div className={css.rules}>
        {PACK_RULES.map(([title, body]) => (
          <div key={title} className={css.rule}>
            <span className={css.ruleTitle}>{title}</span>
            <span className={css.ruleBody}>{body}</span>
          </div>
        ))}
      </div>
    </>
  )
}

function PackCard({ pack }: { pack: Pack }) {
  return (
    <Card tint={pack.recommended} className={pack.recommended ? css.packHot : css.packCard}>
      <div className={css.packHead}>
        <span className={css.packKind}>流量包</span>
        {pack.recommended && <Tag tone="brandSolid">最划算</Tag>}
      </div>
      <div>
        <div className={css.packSize}>{compactBytes(pack.traffic_bytes)}</div>
        <div className={css.packPriceRow}>
          <span className={css.packPrice}>{formatMoney(pack.unit_amount, pack.currency)}</span>
          <span className={css.per}>{perGbNote(pack)}</span>
        </div>
      </div>
      <a className={pack.recommended ? css.ctaPrimary : css.cta} href={href('/checkout', { pack: pack.id })}>
        购买
      </a>
    </Card>
  )
}

function CardsSkeleton({ count }: { count: number }) {
  return (
    <div className={css.planGrid} aria-busy="true">
      {Array.from({ length: count }, (_, i) => (
        <Card key={i}>
          <Skeleton width={90} height={20} />
          <Skeleton width={140} height={36} />
          <Skeleton height={60} />
          <Skeleton height={40} radius="var(--radius-md)" />
        </Card>
      ))}
    </div>
  )
}
