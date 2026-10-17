import { useMemo, useState } from 'react'
import { Btn, Chip, Num, cx } from '../../kit'
import { headlinePrice, periodOf, periodUnit, planTraffic, type Plan } from '../common/catalog'
import type { Holdings } from '../common/holdings'
import { PlanAltPrices, PlanFacts } from '../common/PlanCard'
import { moneyShort } from '../common/purchase'
import { planCardView } from './labels'
import css from './Plans.module.css'

export interface PickListProps {
  plans: readonly Plan[]
  h: Holdings
  className?: string
}

/**
 * 手机按时间套餐单选展开列表（样稿 .pick 的 details.pk）
 */
export function PickList({ plans, h, className }: PickListProps) {
  const defaultOpenId = useMemo(
    () => plans.find((p) => planCardView(p, h.held, h.naming).current)?.id ?? null,
    [plans, h.held, h.naming],
  )
  const [openId, setOpenId] = useState<string | null>(defaultOpenId)

  return (
    <div className={cx(css.pick, className)}>
      {plans.map((plan) => {
        const v = planCardView(plan, h.held, h.naming)
        const open = openId === plan.id
        const price = headlinePrice(plan)
        const badge = v.tag
          ? { text: v.tag.text, tone: v.tag.tone === 'brand' ? ('solid' as const) : ('soft' as const) }
          : null
        const unitSuffix = price ? (plan.kind === 'one_time' ? '一次' : periodUnit(periodOf(price)).trim()) : ''

        return (
          <div
            key={plan.id}
            className={css.pk}
            data-open={open ? 'true' : undefined}
            data-current={v.current ? 'true' : undefined}
          >
            <button
              type="button"
              className={css.pkHead}
              aria-expanded={open}
              aria-controls={`pk-${plan.id}`}
              onClick={() => setOpenId(open ? null : plan.id)}
            >
              <span className={css.pkN}>
                {plan.name}
                {badge && (
                  <Chip tone={badge.tone} className={css.chip}>
                    {badge.text}
                  </Chip>
                )}
              </span>
              <span className={css.pkS}>
                {planTraffic(plan)} · {plan.max_devices === null ? '不限台数' : `${plan.max_devices} 台`}
              </span>
              <span className={css.pkP}>
                <b>
                  <Num>{price ? moneyShort(price.unit_amount) : '—'}</Num>
                </b>
                {unitSuffix}
              </span>
            </button>
            <div id={`pk-${plan.id}`} className={css.pkB} hidden={!open}>
              <PlanAltPrices plan={plan} className={css.alt} />
              <PlanFacts plan={plan} skip={['traffic', 'devices']} />
              <div className={css.act}>
                <Btn
                  full
                  href={v.href}
                  tone={v.current ? 'hot' : v.primary ? 'primary' : 'soft'}
                  id={`btn-pick-${plan.id}`}
                >
                  {v.cta}
                </Btn>
              </div>
              {v.note && <p className={css.next}>{v.note}</p>}
            </div>
          </div>
        )
      })}
    </div>
  )
}
