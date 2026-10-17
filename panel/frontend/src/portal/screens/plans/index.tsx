import { useMemo } from 'react'
import { navigate, useHashLocation } from '../../../core/router'
import { Empty, Segmented, Skeleton, type SegmentedOption } from '../../../ui'
import { PageHeadAside } from '../../head'
import { Btn } from '../../kit'
import { LoadError, Slot } from '../common/Blocks'
import { listable, usePlans, type PlanKind } from '../common/catalog'
import { useHoldings, type Holdings } from '../common/holdings'
import { PlanCard } from '../common/PlanCard'
import { planCardView } from './labels'
import { OnceList } from './OnceList'
import { PickList } from './PickList'
import css from './Plans.module.css'

// ---------------------------------------------------------------------------
// 选购：按时间 / 一次性两类套餐；?kind= 指定，缺省按时间（只持有一次性时缺省一次性）。
// ---------------------------------------------------------------------------

const KIND_OPTIONS: readonly SegmentedOption<PlanKind>[] = [
  { value: 'subscription', label: '按时间' },
  { value: 'one_time', label: '一次性' },
]

const SEG_CAPS: Readonly<Record<PlanKind, string>> = {
  subscription: '月 / 季 / 半年 / 年付，每期给流量，到下一期重新给满',
  one_time: '付一次，流量用完为止，不会清零',
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

  const heldIds = useMemo(() => new Set(h.held.map((s) => s.plan_id)), [h.held])
  const shown = useMemo(() => (plans.data ?? []).filter((p) => listable(p, heldIds)), [plans.data, heldIds])
  const catalogKinds = useMemo(() => new Set(shown.map((p) => p.kind)), [shown])
  const asked = query.get('kind')
  const showTabs = catalogKinds.has('subscription') && catalogKinds.has('one_time')
  // 只有一类时忽略地址里的 kind，直接列那一类
  const kind: PlanKind = showTabs && (asked === 'one_time' || asked === 'subscription') ? asked : defaultKind(h.held, catalogKinds)

  if (h.subs.isPending || plans.isPending) return <Skeleton height={240} />
  if (h.subs.isError) return <LoadError error={h.subs.error} onRetry={() => void h.subs.refetch()} what="套餐" />
  if (plans.isError) return <LoadError error={plans.error} onRetry={() => void plans.refetch()} what="套餐" />

  const list = shown.filter((p) => p.kind === kind)
  return (
    <div>
      <Slot name="portal.plans.notice" />
      {showTabs && (
        <PageHeadAside>
          <Segmented label="套餐类型" size="md" value={kind} onChange={setKind} options={KIND_OPTIONS} />
        </PageHeadAside>
      )}
      <p className={css.segCap}>{SEG_CAPS[kind]}</p>
      {list.length === 0 ? (
        <Empty title="暂时没有可买的套餐" description="新套餐上架后会出现在这里。" />
      ) : kind === 'subscription' ? (
        <>
          <div className={css.plans}>
            {list.map((plan) => {
              const v = planCardView(plan, h.held, h.naming)
              const badge = v.tag
                ? { text: v.tag.text, tone: v.tag.tone === 'brand' ? ('solid' as const) : ('soft' as const) }
                : null
              return (
                <PlanCard
                  key={plan.id}
                  plan={plan}
                  site={h.site}
                  badge={badge}
                  current={v.current}
                  action={
                    <Btn
                      href={v.href}
                      tone={v.current ? 'hot' : v.primary ? 'primary' : 'soft'}
                      id={`btn-plan-${plan.id}`}
                    >
                      {v.label}
                    </Btn>
                  }
                  note={v.note}
                />
              )
            })}
          </div>
          <PickList plans={list} h={h} />
        </>
      ) : (
        <OnceList plans={list} h={h} />
      )}
    </div>
  )
}
