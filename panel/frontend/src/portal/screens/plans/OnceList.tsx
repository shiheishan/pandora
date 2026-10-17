import { Btn, Chip, Num } from '../../kit'
import {
  gbParts,
  headlinePrice,
  resetPriceText,
  throttleNote,
  type Plan,
} from '../common/catalog'
import type { Holdings } from '../common/holdings'
import { moneyShort } from '../common/purchase'
import { planCardView } from './labels'
import css from './Plans.module.css'

export interface OnceListProps {
  plans: readonly Plan[]
  h: Holdings
}

/**
 * 一次性套餐的行列表（样稿 .once 的 .on-row）
 */
export function OnceList({ plans, h }: OnceListProps) {
  return (
    <div className={css.once}>
      {plans.map((plan) => {
        const v = planCardView(plan, h.held, h.naming)
        const price = headlinePrice(plan)
        const badge = v.tag
          ? { text: v.tag.text, tone: v.tag.tone === 'brand' ? ('solid' as const) : ('soft' as const) }
          : null

        const validity = price
          ? price.validity_days === null
            ? '永久有效'
            : `${price.validity_days} 天内用完`
          : '永久有效'
        const devices = plan.max_devices === null ? '不限台数' : `${plan.max_devices} 台设备`
        const throttle = throttleNote(plan.throttle_kbps)
        const reset = resetPriceText(plan)

        const metaParts = [validity, devices, throttle, reset].filter(Boolean).join(' · ')

        return (
          <section
            key={plan.id}
            id={`plan-${plan.id}`}
            className={css.onRow}
            data-current={v.current ? 'true' : undefined}
          >
            <div className={css.onGb}>
              {plan.traffic_bytes === null ? (
                <Num className={css.gbNum}>不限</Num>
              ) : (
                <>
                  <Num className={css.gbNum}>{gbParts(plan.traffic_bytes).value}</Num>
                  <small className={css.gbUnit}>{gbParts(plan.traffic_bytes).unit}</small>
                </>
              )}
            </div>
            <div className={css.onT}>
              <h3 className={css.onName}>{plan.name}</h3>
              {badge && (
                <Chip tone={badge.tone} className={css.chip}>
                  {badge.text}
                </Chip>
              )}
              <small className={css.onSub}>{metaParts}</small>
            </div>
            <div className={css.onP}>
              <Num className={css.pNum}>{price ? moneyShort(price.unit_amount) : '—'}</Num>
            </div>
            <div className={css.onA}>
              <Btn
                size="sm"
                full
                href={v.href}
                tone={v.current ? 'hot' : v.primary ? 'primary' : 'soft'}
                id={`btn-plan-${plan.id}`}
              >
                {v.label}
              </Btn>
              {v.note && <p className={css.next}>{v.note}</p>}
            </div>
          </section>
        )
      })}
    </div>
  )
}
