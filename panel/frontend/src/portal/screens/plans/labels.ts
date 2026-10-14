import { href } from '../../../core/router'
import type { Plan } from '../common/catalog'
import { headlinePrice } from '../common/PlanCard'
import { addPeriod, day, daysLeft, money, type Naming } from '../common/purchase'
import type { QuoteRow } from '../common/quote'
import { isLive, type Subscription } from '../common/subscriptions'

// ---------------------------------------------------------------------------
// 选购页套餐卡的按钮与小字（原型 planCard，购买模型 10-07）：
//   另买一份 / 还没有套餐   「买这个」→ 确认页 ?new=，小字「新链接 · 今天起到 …」
//   同款（有人在用）        只有「续费」→ ?renew=（同款可续里的第一份），小字「到期日 A → B · 链接不变」
//   不同款，能换的只有一份  「换成 X」→ ?change=<那份>&plan=，小字「换过去，今天付 ¥x · 链接不变」
//   不同款，能换的有多份    「换成 X」→ ?change-plan=，小字「下一步选换掉哪一份」（换掉谁永不预选）
// 金额只取报价接口给的，前端不算钱。
// ---------------------------------------------------------------------------
export type PlansMode = 'mine' | 'new'

export interface PlanCardView {
  tag: { text: string; tone: 'brand' | 'ok' | 'neutral' } | null
  label: string
  /** null = 按钮置灰 */
  href: string | null
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

/** 「换过去，今天付 ¥21.00」/「换过去今天不用付，还退 ¥9.00 到钱包余额」 */
export function changeNote(row: Pick<QuoteRow, 'total' | 'refund'> | undefined): string {
  if (!row) return '链接不变'
  if (row.total > 0) return `换过去，今天付 ${money(row.total)} · 链接不变`
  if (row.refund > 0) return `换过去今天不用付，还退 ${money(row.refund)} 到钱包余额 · 链接不变`
  return '换过去今天不用付 · 链接不变'
}

export function planCardView(plan: Plan, mode: PlansMode, held: readonly Subscription[], naming: Naming, quoteFor: (subId: string, planId: string) => QuoteRow | undefined, now: Date = new Date()): PlanCardView {
  const holders = renewOrder(held.filter((s) => s.plan_id === plan.id))
  if (mode === 'new' || held.length === 0) {
    const price = headlinePrice(plan)
    return {
      tag: holders.length ? { text: `你已有${holders.length > 1 ? ` ${holders.length} 份` : '一份'}`, tone: 'neutral' } : plan.recommended ? { text: '多数人选这个', tone: 'brand' } : null,
      label: '买这个',
      href: href('/checkout', { new: plan.id }),
      primary: plan.recommended,
      note: price ? `${held.length ? '新链接 · ' : ''}今天起到 ${day(addPeriod(now, price.billing_interval, price.interval_count), now)}` : null,
      current: false,
    }
  }
  const owner = holders[0]
  if (owner) {
    const left = daysLeft(owner.current_period_end, now)
    const tag = naming.multi
      ? { text: `「${naming.sn(owner)}」在用`, tone: 'ok' as const }
      : { text: `你在用 · ${left !== null && left <= 0 ? '已过期' : left === null ? '长期有效' : `还剩 ${left} 天`}`, tone: 'ok' as const }
    if (!owner.renew_until || !plan.allow_renewal) return { tag, label: '这个套餐暂时不能续费', href: null, primary: false, note: '可以换个套餐，或另买一份', current: true }
    const expired = left !== null && left <= 0
    return {
      tag,
      label: naming.multi ? `续费「${naming.sn(owner)}」` : '续费',
      href: href('/checkout', { renew: owner.id, from: 'plans' }),
      primary: true,
      note: expired || !owner.current_period_end ? `今天起，到 ${day(owner.renew_until, now)} · 链接不变` : `到期日 ${day(owner.current_period_end, now)} → ${day(owner.renew_until, now)} · 链接不变`,
      current: true,
    }
  }
  if (!plan.allow_upgrade) return { tag: null, label: `暂不能换成${plan.name}`, href: null, primary: false, note: '可以切到「另买一份」单独买', current: false }
  const cands = held.filter((s) => s.changeable)
  if (cands.length === 0) return { tag: null, label: `换成${plan.name}`, href: null, primary: false, note: '现在没有能换的那一份', current: false }
  if (cands.length === 1) {
    const sub = cands[0]!
    const note = changeNote(quoteFor(sub.id, plan.id))
    return { tag: null, label: `换成${plan.name}`, href: href('/checkout', { change: sub.id, plan: plan.id }), primary: false, note: naming.multi ? `换的是「${naming.sn(sub)}」 · ${note}` : note, current: false }
  }
  return { tag: null, label: `换成${plan.name}`, href: href('/checkout', { 'change-plan': plan.id }), primary: false, note: '下一步选换掉哪一份 · 链接不变', current: false }
}
