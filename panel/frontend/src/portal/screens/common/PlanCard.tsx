import type { ReactNode } from 'react'
import { Eyebrow, Icon, Num, Sticker, cx } from '../../kit'
import {
  gb,
  headlinePrice,
  periodOf,
  periodUnit,
  sortPrices,
  type Plan,
} from './catalog'
import css from './PlanCard.module.css'
import { moneyShort } from './purchase'

export { headlinePrice } from './catalog'

const PERIOD_ALT_NAMES: Readonly<Record<string, string>> = {
  '1m': '月付',
  '3m': '季付',
  '6m': '半年',
  '12m': '年付',
}

function altPeriodName(key: string): string {
  if (PERIOD_ALT_NAMES[key]) return PERIOD_ALT_NAMES[key]
  return periodUnit(key).replace(/^\/\s*/, '') || key
}

/** 其余价格档，如「季付 ¥79 · 半年 ¥149 · 年付 ¥299」 */
export function PlanAltPrices({ plan, className }: { plan: Plan; className?: string }) {
  if (plan.kind !== 'subscription') return null
  const head = headlinePrice(plan)
  if (!head) return null
  const headKey = periodOf(head)
  const rest = sortPrices(plan.prices).filter((p) => periodOf(p) !== headKey)
  if (!rest.length) return null

  return (
    <div className={cx(css.alt, className)}>
      {rest.map((p, idx) => {
        const key = periodOf(p)
        return (
          <span key={key}>
            {idx > 0 && ' · '}
            {altPeriodName(key)} <b>{moneyShort(p.unit_amount)}</b>
          </span>
        )
      })}
    </div>
  )
}

export type PlanFactKey = 'traffic' | 'validity' | 'devices' | 'throttle' | 'reset'

export interface PlanFactsProps {
  plan: Plan
  skip?: readonly PlanFactKey[]
  className?: string
}

/** 事实列表（流量、设备、限速、重置等） */
export function PlanFacts({ plan, skip = [], className }: PlanFactsProps) {
  const skipSet = new Set(skip)
  const items: { key: PlanFactKey; content: ReactNode }[] = []

  // 1. 流量
  if (!skipSet.has('traffic')) {
    if (plan.traffic_bytes !== null) {
      if (plan.kind === 'one_time') {
        items.push({
          key: 'traffic',
          content: (
            <span>
              共 <b>{gb(plan.traffic_bytes)}</b> 流量
            </span>
          ),
        })
      } else {
        items.push({
          key: 'traffic',
          content: (
            <span>
              每期 <b>{gb(plan.traffic_bytes)}</b> 流量
            </span>
          ),
        })
      }
    } else {
      items.push({
        key: 'traffic',
        content: (
          <span>
            <b>不限</b>流量
          </span>
        ),
      })
    }
  }

  // 2. 有效期（仅一次性套餐）
  if (!skipSet.has('validity') && plan.kind === 'one_time') {
    const head = headlinePrice(plan)
    if (!head || head.validity_days === null) {
      items.push({
        key: 'validity',
        content: (
          <span>
            <b>永久</b>有效
          </span>
        ),
      })
    } else {
      items.push({
        key: 'validity',
        content: (
          <span>
            有效 <b>{head.validity_days} 天</b>
          </span>
        ),
      })
    }
  }

  // 3. 设备
  if (!skipSet.has('devices')) {
    if (plan.max_devices !== null) {
      items.push({
        key: 'devices',
        content: (
          <span>
            <b>{plan.max_devices} 台</b>设备同时用
          </span>
        ),
      })
    } else {
      items.push({
        key: 'devices',
        content: (
          <span>
            设备数<b>不限</b>
          </span>
        ),
      })
    }
  }

  // 4. 限速
  if (!skipSet.has('throttle')) {
    if (plan.throttle_kbps === null) {
      items.push({
        key: 'throttle',
        content: <span>不限速</span>,
      })
    } else {
      items.push({
        key: 'throttle',
        content: (
          <span>
            限速 <b>{Number((plan.throttle_kbps / 1000).toFixed(3))} Mbps</b>
          </span>
        ),
      })
    }
  }

  // 5. 流量重置
  if (!skipSet.has('reset') && plan.reset !== null) {
    const periodWord = plan.kind === 'one_time' ? '有效期内' : '每期'
    if (plan.reset.limit !== null) {
      items.push({
        key: 'reset',
        content: (
          <span>
            流量重置 <b>{moneyShort(plan.reset.price)}</b> / 次，{periodWord} <b>{plan.reset.limit} 次</b>
          </span>
        ),
      })
    } else {
      items.push({
        key: 'reset',
        content: (
          <span>
            流量重置 <b>{moneyShort(plan.reset.price)}</b> / 次，<b>不限</b>次数
          </span>
        ),
      })
    }
  }

  return (
    <ul className={cx(css.facts, className)}>
      {items.map((item) => (
        <li key={item.key} className={css.factItem}>
          <Icon name="check" size={15} className={css.factIcon} />
          {item.content}
        </li>
      ))}
    </ul>
  )
}

export interface PlanCardProps {
  plan: Plan
  /** 眉标：站点名（样稿 .eyebrow「Pandora」） */
  site?: string
  /** 右上角徽标（样稿 .badge）：soft = 白底（你在用），solid = 深绿底白字（推荐）；null 不画 */
  badge?: { text: string; tone: 'soft' | 'solid' } | null
  /** 兼容旧代码直接传 tag */
  tag?: ReactNode
  /** 正在用的这一款：换底色（样稿 .plan.pop） */
  current?: boolean
  /** 按钮（调用方传 kit 的 Btn，href 链接） */
  action: ReactNode
  /** 按钮下的小字（样稿 .next） */
  note?: ReactNode
}

/**
 * 套餐卡（样稿 article.plan，但元素用 section）
 */
export function PlanCard({
  plan,
  site = 'Pandora',
  badge,
  tag,
  current = false,
  action,
  note,
}: PlanCardProps) {
  const price = headlinePrice(plan)

  return (
    <section className={cx(css.card, current && css.current)} id={`plan-${plan.id}`}>
      {badge ? (
        <span className={cx(css.badge, badge.tone === 'solid' && css.badgeSolid)}>{badge.text}</span>
      ) : tag ? (
        <span className={css.badge}>{tag}</span>
      ) : null}
      <Eyebrow>{site}</Eyebrow>
      <Sticker as="h3">{plan.name}</Sticker>
      {plan.description && <p className={css.desc}>{plan.description}</p>}
      {price ? (
        <div className={css.price}>
          <span className={css.cur}>¥</span>
          <Num className={css.priceNum}>{moneyShort(price.unit_amount).replace(/^¥/, '')}</Num>
          <small>{plan.kind === 'one_time' ? '一次' : periodUnit(periodOf(price))}</small>
        </div>
      ) : (
        <div className={css.price}>—</div>
      )}
      <PlanAltPrices plan={plan} />
      <div className={css.rule} />
      <PlanFacts plan={plan} />
      <div className={css.act}>{action}</div>
      {note && <p className={css.next}>{note}</p>}
    </section>
  )
}
