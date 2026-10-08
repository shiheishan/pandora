import type { ReactNode } from 'react'
import { Card } from '../../../ui'
import { cnyPrices, periodOf, periodUnit, priceFor, throttleNote, type Plan } from './catalog'
import css from './PlanCard.module.css'
import { devicesText, moneyShort, planTraffic } from './purchase'

/** 套餐卡的事实：每月流量、设备数、限速、3 个月价，后面跟后台写的卖点（R100） */
export function planFacts(plan: Plan): string[] {
  const month = priceFor(plan, '1m')
  const three = priceFor(plan, '3m')
  return [
    planTraffic(plan),
    plan.max_devices === null ? '不限设备数' : `${devicesText(plan.max_devices)}设备同时用`,
    throttleNote(plan.throttle_kbps),
    month && three ? `3 个月 ${moneyShort(three.unit_amount)}` : null,
    ...plan.highlights,
  ].filter((f): f is string => Boolean(f))
}

/** 卡片上的大价签：有月付写月付，否则写最便宜的一档 */
export function headlinePrice(plan: Plan) {
  return priceFor(plan, '1m') ?? [...cnyPrices(plan)].sort((a, b) => a.unit_amount - b.unit_amount)[0] ?? null
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
          <small> {periodUnit(periodOf(price))}</small>
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
