import { href } from '../../../core/router'
import type { Plan } from '../common/catalog'
import { headlinePrice } from '../common/PlanCard'
import { day, daysLeft, endAfter, type Naming } from '../common/purchase'
import { isLive, type Subscription } from '../common/subscriptions'

// ---------------------------------------------------------------------------
// 选购页套餐卡的按钮与小字（10-16）：落点由确认页 landings 让用户选；这里只拼 ?new= 或 ?plan=
// ---------------------------------------------------------------------------

export interface PlanCardView {
  tag: { text: string; tone: 'brand' | 'ok' | 'neutral' } | null
  label: string
  /** 按时间卡片上的按钮字（样稿：买标准版 / 续费专业版）；一次性列表照样稿用 label */
  cta: string
  href: string
  primary: boolean
  note: string | null
  /** 正在用的这一款：卡片换底色 */
  current: boolean
}

const endOf = (s: Subscription) => (s.current_period_end ? new Date(s.current_period_end).getTime() : Number.MAX_SAFE_INTEGER)

/** 同款可续的次序（与服务端 purchase.Options 的 renew 次序一致）：生效中按到期从早到晚，再接过期的按到期从晚到早 */
export function renewOrder(holders: readonly Subscription[]): Subscription[] {
  const live = holders.filter(isLive).sort((a, b) => endOf(a) - endOf(b))
  const expired = holders.filter((s) => !isLive(s)).sort((a, b) => endOf(b) - endOf(a))
  return [...live, ...expired]
}

export function planCardView(plan: Plan, held: readonly Subscription[], naming: Naming, now: Date = new Date()): PlanCardView {
  const holders = renewOrder(held.filter((s) => s.plan_id === plan.id))
  const owner = holders[0]

  const tag = owner
    ? {
        text: `${naming.multi ? `「${naming.sn(owner)}」在用` : '你在用'}${(() => {
          const left = daysLeft(owner.current_period_end, now)
          return left !== null && left <= 0 ? ' · 已过期' : ''
        })()}`,
        tone: 'ok' as const,
      }
    : plan.recommended
      ? { text: '推荐', tone: 'brand' as const }
      : null

  const label = owner
    ? owner.plan_kind === 'subscription' && owner.renewable
      ? '续费'
      : owner.plan_kind === 'one_time' && owner.stackable
        ? '再买一份'
        : '买这个'
    : '买这个'

  const cta = label === '续费' ? `续费${plan.name}` : label === '买这个' ? `买${plan.name}` : label

  const hrefOut = held.length === 0 ? href('/checkout', { new: plan.id }) : href('/checkout', { plan: plan.id })

  const primary = Boolean(owner) || plan.recommended
  const current = Boolean(owner)

  let note: string | null = null
  if (held.length === 0) {
    const price = headlinePrice(plan)
    if (price) {
      const end = endAfter(now, price)
      if (plan.kind === 'one_time') {
        note = end === null ? '永久有效' : `今天起到 ${day(end, now)}`
      } else {
        note = `今天起到 ${day(end!, now)}`
      }
    }
  } else if (owner) {
    if (label === '续费') note = '下一步选：续在原来那份上，或另开一份'
    else if (label === '再买一份') note = '下一步选：叠加到原来那份，或另开一份'
    else note = '原来那份不能续，会另开一份'
  } else {
    note = '下一步选：另开一份，或换掉现在的一份'
  }

  return { tag, label, cta, href: hrefOut, primary, note, current }
}
