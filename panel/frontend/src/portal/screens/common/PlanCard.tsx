import type { ReactNode } from 'react'
import { Card } from '../../../ui'
import {
  headlinePrice,
  periodOf,
  periodSpan,
  periodUnit,
  planTraffic,
  resetPriceText,
  sortPrices,
  throttleNote,
  validityText,
  type Plan,
} from './catalog'
import css from './PlanCard.module.css'
import { devicesText, moneyShort } from './purchase'

export { headlinePrice } from './catalog'

/** 除大价签外其余价格档，写成「3 个月 ¥84 · 半年 ¥160 · 1 年 ¥300」 */
function otherPricesLine(plan: Plan): string | null {
  const head = headlinePrice(plan)
  if (!head || plan.kind !== 'subscription') return null
  const headKey = periodOf(head)
  const rest = sortPrices(plan.prices).filter((p) => periodOf(p) !== headKey)
  if (!rest.length) return null
  return rest.map((p) => `${periodSpan(periodOf(p))} ${moneyShort(p.unit_amount)}`).join(' · ')
}

function planFacts(plan: Plan): string[] {
  const price = headlinePrice(plan)
  const device = plan.max_devices === null ? '不限设备数' : `${devicesText(plan.max_devices)}设备同时用`
  if (plan.kind === 'one_time' && price) {
    return [planTraffic(plan), validityText(price.validity_days), device, throttleNote(plan.throttle_kbps), resetPriceText(plan)].filter(
      (f): f is string => Boolean(f),
    )
  }
  return [planTraffic(plan), device, throttleNote(plan.throttle_kbps), otherPricesLine(plan), resetPriceText(plan)].filter((f): f is string => Boolean(f))
}

/**
 * 套餐卡（原型 planCard）：选购页与「再买一份」共用。按钮与按钮下的小字由调用方按意图给，
 * 卡片只管展示套餐本身。
 */
export function PlanCard({ plan, tag, current = false, action, note }: { plan: Plan; tag?: ReactNode; current?: boolean; action: ReactNode; note?: ReactNode }) {
  const price = headlinePrice(plan)
  return (
    <Card className={current ? css.current : plan.recommended ? css.hot : css.card} id={`plan-${plan.id}`}>
      <div className={css.head}>
        <h3 className={css.name}>{plan.name}</h3>
        {tag}
      </div>
      {price ? (
        <div className={css.price}>
          {moneyShort(price.unit_amount)}
          <small>{plan.kind === 'one_time' ? ' 一次' : ` ${periodUnit(periodOf(price))}`}</small>
        </div>
      ) : (
        <div className={css.price}>—</div>
      )}
      {plan.description && <p className={css.desc}>{plan.description}</p>}
      <ul className={css.facts}>
        {planFacts(plan).map((f, i) => (
          <li key={i}>{f}</li>
        ))}
      </ul>
      {action}
      {note && <div className={css.note}>{note}</div>}
    </Card>
  )
}
