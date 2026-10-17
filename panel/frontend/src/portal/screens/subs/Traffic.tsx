import { href } from '../../../core/router'
import { Empty, Skeleton } from '../../../ui'
import { usePageHead } from '../../head'
import { Bar, BigNumber, Btn, Meta, Row, Rows, cx } from '../../kit'
import { LoadError } from '../common/Blocks'
import { gb, gbParts, planTraffic, usePlans } from '../common/catalog'
import { useHoldings } from '../common/holdings'
import { day, isLow, leftOf, money } from '../common/purchase'
import { tierOf, useQuote } from '../common/quote'
import { isLive, type Subscription } from '../common/subscriptions'
import { periodWord } from '../common/card-text'
import { ResetOffer } from './ResetOffer'
import css from './Pages.module.css'

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
    return (
      <Empty
        title="这一份现在不在用"
        description="重置流量、换更大的套餐都要在一份在用的套餐上办。已过期的先续费，就能接着用。"
        action={
          <Btn href={href('/subs')} tone="soft">
            回到我的套餐
          </Btn>
        }
      />
    )
  }
  return <TrafficBody sub={sub} naming={h.naming} />
}

function TrafficBody({ sub, naming }: { sub: Subscription; naming: ReturnType<typeof useHoldings>['naming'] }) {
  const { left, total, bonus } = leftOf(sub)
  const period = periodWord(sub)
  const scope = period === '总' ? '总流量' : '本期'
  // 剩余流量拆成数和单位；不限流量直接写「不限」
  const big = left === null ? null : gbParts(left)
  // 下一期从流量清零那天起（next_reset_at）：提前续过费的，那天自动开新一期；最后一期要续上才有
  const nextCycle = sub.next_reset_at
    ? sub.current_period_end && new Date(sub.next_reset_at) < new Date(sub.current_period_end)
      ? `${day(sub.next_reset_at)} 起`
      : `${day(sub.next_reset_at)} 续上后`
    : null
  return (
    <div className={cx(css.page, css.narrow)}>
      <div className={css.card}>
        {naming.multi && <p className={css.faint}>{naming.dn(sub)}</p>}
        <BigNumber size="lg" value={big ? big.value : '不限'} unit={big?.unit} />
        <Meta>
          {scope}剩余
          {total !== null && (
            <>
              {' · 共 '}
              <b>{gb(total)}</b>
            </>
          )}
          {bonus > 0 && (sub.plan_kind === 'one_time' ? `，含送的 ${gb(bonus)}` : `，含送的 ${gb(bonus)}，本期有效`)}
          。
          {sub.plan_kind === 'subscription' && nextCycle && sub.traffic_limit !== null && <> {nextCycle}，新的一期又有 {gb(sub.traffic_limit)}。</>}
        </Meta>
        {total !== null && total > 0 && left !== null && <Bar value={left / total} low={isLow(sub)} label="剩余流量" />}
      </div>
      {/* 两个都返回 null 时 Rows 空着（一个高度为 0 的白底条，看不出来） */}
      <Rows>
        <ResetOffer sub={sub} />
        <BiggerPlan sub={sub} />
      </Rows>
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
    .map((planId) => ({ row: tierOf(quote.data!.quotes, sub, (q) => q.plan_id === planId)!, plan: plans.data!.find((p) => p.id === planId) }))
    .filter((x) => x.plan && x.plan.kind === sub.plan_kind)
    .map((x) => ({ ...x, limit: x.plan!.traffic_bytes }))
    .filter((x) => x.limit === null || (x.limit ?? 0) > mine)
    .sort((a, b) => (a.limit ?? Number.MAX_SAFE_INTEGER) - (b.limit ?? Number.MAX_SAFE_INTEGER))[0]
  if (!bigger?.plan) return null
  const pay = bigger.row.total > 0 ? `今天付 ${money(bigger.row.total)}` : '今天不用付'
  return (
    <Row
      icon="swap"
      title={`换成${bigger.plan.name}`}
      sub={`总是不够用？${planTraffic(bigger.plan)}，${pay}`}
      href={href('/checkout', { change: sub.id, plan: bigger.plan.id })}
    />
  )
}
