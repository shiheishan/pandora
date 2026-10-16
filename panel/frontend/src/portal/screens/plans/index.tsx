import { useMemo } from 'react'
import { navigate, useHashLocation } from '../../../core/router'
import { Empty, Skeleton, Tag } from '../../../ui'
import { LoadError, Slot } from '../common/Blocks'
import { usePlans, type PlanKind } from '../common/catalog'
import { flowCss } from '../common/Flow'
import { useHoldings, type Holdings } from '../common/holdings'
import { PlanCard } from '../common/PlanCard'
import { planCardView } from './labels'
import css from './Plans.module.css'

// ---------------------------------------------------------------------------
// 选购：按时间 / 一次性两类套餐；?kind= 指定，缺省按时间（只持有一次性时缺省一次性）。
// 落点（另开 / 续 / 叠加 / 换掉）在确认页由 landings 让用户选。
// ---------------------------------------------------------------------------

const TAB_META: Readonly<Record<PlanKind, { title: string; hint: string }>> = {
  subscription: { title: '按时间', hint: '月 / 季 / 半年 / 年付，每期给流量' },
  one_time: { title: '一次性', hint: '付一次，流量用完为止' },
}

function defaultKind(held: Holdings['held'], kinds: ReadonlySet<PlanKind>): PlanKind {
  if (kinds.size === 1) return kinds.values().next().value!
  if (held.length > 0 && held.every((s) => s.plan_kind === 'one_time')) return 'one_time'
  return 'subscription'
}

export default function Plans() {
  const { query } = useHashLocation()
  const h = useHoldings()
  const plans = usePlans()
  // 总是写明 kind：缺省值随持有变（只持有一次性时缺省是一次性），省掉就切不回「按时间」
  const setKind = (kind: PlanKind) => navigate('/plans', { query: { ...Object.fromEntries(query), kind }, replace: true })

  const catalogKinds = useMemo(() => new Set((plans.data ?? []).map((p) => p.kind)), [plans.data])
  const asked = query.get('kind')
  const showTabs = catalogKinds.has('subscription') && catalogKinds.has('one_time')
  // 只有一类时忽略地址里的 kind，直接列那一类
  const kind: PlanKind = showTabs && (asked === 'one_time' || asked === 'subscription') ? asked : defaultKind(h.held, catalogKinds)

  if (h.subs.isPending || plans.isPending) return <Skeleton height={240} />
  if (h.subs.isError) return <LoadError error={h.subs.error} onRetry={() => void h.subs.refetch()} what="套餐" />
  if (plans.isError) return <LoadError error={plans.error} onRetry={() => void plans.refetch()} what="套餐" />

  const list = plans.data.filter((p) => p.kind === kind)
  return (
    <div className={flowCss.stack}>
      <Slot name="portal.plans.notice" />
      {showTabs && (
        <div className={css.seg} role="tablist" aria-label="套餐类型">
          {(['subscription', 'one_time'] as const).map((k) => (
            <button key={k} type="button" role="tab" aria-selected={kind === k} onClick={() => setKind(k)} id={`tab-${k}`}>
              {TAB_META[k].title}
              <small>{TAB_META[k].hint}</small>
            </button>
          ))}
        </div>
      )}
      {list.length === 0 ? (
        <Empty title="暂时没有可买的套餐" description="新套餐上架后会出现在这里。" />
      ) : (
        <div className={flowCss.cards}>
          {list.map((plan) => {
            const v = planCardView(plan, h.held, h.naming)
            return (
              <PlanCard
                key={plan.id}
                plan={plan}
                current={v.current}
                tag={v.tag ? <Tag tone={v.tag.tone === 'neutral' ? undefined : v.tag.tone}>{v.tag.text}</Tag> : undefined}
                action={
                  <a className={v.primary ? flowCss.cta : flowCss.ctaQuiet} href={v.href} id={`btn-plan-${plan.id}`}>
                    {v.label}
                  </a>
                }
                note={v.note}
              />
            )
          })}
        </div>
      )}
    </div>
  )
}
