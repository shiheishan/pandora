import { href } from '../../../core/router'
import { Empty, Skeleton } from '../../../ui'
import { usePageHead } from '../../head'
import { LoadError } from '../common/Blocks'
import { gb, planTraffic, usePlans } from '../common/catalog'
import { flowCss } from '../common/Flow'
import { useHoldings } from '../common/holdings'
import { day, leftOf, money } from '../common/purchase'
import { tierOf, useQuote } from '../common/quote'
import { isLive, type Subscription } from '../common/subscriptions'
import { periodWord } from '../common/card-text'
import { ResetOffer } from './ResetOffer'
import css from './Subs.module.css'

/**
 * 流量不够：剩余用量、付费重置、换更大套餐（金额来自报价接口）。
 */
export function Traffic({ id }: { id: string }) {
  usePageHead('流量不够', '/subs')
  const h = useHoldings()
  if (h.subs.isPending) return <Skeleton height={240} />
  if (h.subs.isError) return <LoadError error={h.subs.error} onRetry={() => void h.subs.refetch()} what="套餐" />
  const sub = h.held.find((s) => s.id === id)
  if (!sub || !isLive(sub)) {
    return <Empty title="这一份现在不在用" description="重置流量、换更大的套餐都要在一份在用的套餐上办。已过期的先续费，就能接着用。" action={<a href={href('/subs')}>回到我的套餐</a>} />
  }
  return <TrafficBody sub={sub} naming={h.naming} />
}

function TrafficBody({ sub, naming }: { sub: Subscription; naming: ReturnType<typeof useHoldings>['naming'] }) {
  const { left, total, bonus } = leftOf(sub)
  const period = periodWord(sub)
  const scope = period === '总' ? '总流量' : '本期'
  const bonusNote =
    bonus > 0
      ? sub.plan_kind === 'one_time'
        ? `含送的 ${gb(bonus)}`
        : `含送的 ${gb(bonus)}，本期有效`
      : null

  return (
    <div className={`${flowCss.stack} ${flowCss.narrow}`}>
      {naming.multi && (
        <p className={flowCss.lead}>
          <b>{naming.dn(sub)}</b>
        </p>
      )}
      <p className={flowCss.lead}>
        {scope}还剩 <b>{left === null ? '不限' : gb(left)}</b>
        {total !== null && <> / 共 {gb(total)}</>}
        {bonusNote && <>（{bonusNote}）</>}
        。
        {sub.plan_kind === 'subscription' && sub.current_period_end && sub.traffic_limit !== null && (
          <> {day(sub.current_period_end)} 续上后，新的一期又有 {gb(sub.traffic_limit)}。</>
        )}
      </p>
      <ResetOffer sub={sub} />
      <BiggerPlan sub={sub} />
    </div>
  )
}

/** 同类型套餐里流量更多的最小一档 */
function BiggerPlan({ sub }: { sub: Subscription }) {
  const plans = usePlans()
  const quote = useQuote(sub.changeable ? { action: 'change', subscription_id: sub.id } : null)
  const mine = sub.traffic_limit
  if (mine === null || !plans.data || !quote.data) return null
  const bigger = [...new Set(quote.data.quotes.map((q) => q.plan_id))]
    .map((planId) => ({ row: tierOf(quote.data.quotes, sub, (q) => q.plan_id === planId)!, plan: plans.data.find((p) => p.id === planId) }))
    .filter((x) => x.plan && x.plan.kind === sub.plan_kind)
    .map((x) => ({ ...x, limit: x.plan!.traffic_bytes }))
    .filter((x) => x.limit === null || (x.limit ?? 0) > mine)
    .sort((a, b) => (a.limit ?? Number.MAX_SAFE_INTEGER) - (b.limit ?? Number.MAX_SAFE_INTEGER))[0]
  if (!bigger?.plan) return null
  const pay = bigger.row.total > 0 ? `今天付 ${money(bigger.row.total)}` : '今天不用付'
  return (
    <div className={css.aside}>
      <span className={flowCss.faint}>总是不够用？</span>
      <a className={flowCss.textButton} href={href('/checkout', { change: sub.id, plan: bigger.plan.id })} id={`btn-change-${bigger.plan.id}`}>
        看看换成{bigger.plan.name}（{planTraffic(bigger.plan)}，{pay}）→
      </a>
    </div>
  )
}
