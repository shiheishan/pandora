import { href } from '../../../core/router'
import { Card, Empty, Skeleton, Tag } from '../../../ui'
import { usePageHead } from '../../head'
import { LoadError } from '../common/Blocks'
import { priceFor, usePlans, type Plan } from '../common/catalog'
import { flowCss } from '../common/Flow'
import { useHoldings } from '../common/holdings'
import { day, devicesText, money, moneyShort, perPeriod, planTraffic } from '../common/purchase'
import { useQuote, type QuoteRow } from '../common/quote'
import type { Subscription } from '../common/subscriptions'
import css from './Subs.module.css'

/** 换过去要付多少：「换过去，今天付 ¥21.00」/「换过去今天不用付，还退 ¥9.00 到钱包余额」 */
export function changeResult(row: Pick<QuoteRow, 'total' | 'refund'>): string {
  if (row.total > 0) return `换过去，今天付 ${money(row.total)}`
  if (row.refund > 0) return `换过去今天不用付，还退 ${money(row.refund)} 到钱包余额`
  return '换过去今天不用付'
}

/** 同一档月价比较：比现在贵的标「更大」，便宜的标「更便宜」 */
export function sizeTag(target: Plan, current: Plan | undefined): 'bigger' | 'cheaper' | null {
  const a = priceFor(target, '1m')?.unit_amount
  const b = current ? priceFor(current, '1m')?.unit_amount : undefined
  if (a === undefined || b === undefined || a === b) return null
  return a > b ? 'bigger' : 'cheaper'
}

/**
 * 换个套餐（原型 change-pick）：从卡片进来，已知换的是哪一份。一次报价按套餐展开，
 * 每行写「换过去，今天付 ¥x · 从今天起算，到 …」，点了去确认页把账算清楚。
 */
export function ChangePick({ id }: { id: string }) {
  usePageHead('换个套餐', '/subs')
  const h = useHoldings()
  const plans = usePlans()
  const sub = h.held.find((s) => s.id === id)
  const quote = useQuote(sub?.changeable ? { action: 'change', subscription_id: sub.id } : null)
  if (h.subs.isPending || plans.isPending) return <Skeleton height={240} />
  const failed = h.subs.isError ? h.subs : plans.isError ? plans : null
  if (failed) return <LoadError error={failed.error} onRetry={() => void failed.refetch()} what="套餐" />
  if (!sub || !sub.changeable) return <Empty title="这一份现在不能换套餐" description="已经停用的套餐不能换，可以另买一份。" action={<a href={href('/plans')}>去选购</a>} />
  const current = plans.data!.find((p) => p.id === sub.plan_id)
  const price = sub.renewal_price
  return (
    <div className={`${flowCss.stack} ${flowCss.narrow}`}>
      <p className={flowCss.lead}>
        {h.naming.multi ? `「${h.naming.dn(sub)}」` : '你'}现在是 <b>{sub.plan_name}</b>
        {current && `：${planTraffic(current)} · ${devicesText(current.max_devices)}`}
        {price && ` · ${moneyShort(price.unit_amount)}${perPeriod(price.billing_interval, price.interval_count)}`}
      </p>
      <Card>
        {quote.isPending ? (
          <Skeleton height={120} />
        ) : quote.isError ? (
          <LoadError error={quote.error} onRetry={() => void quote.refetch()} what="价格" />
        ) : quote.data.quotes.length === 0 ? (
          <Empty bare title="暂时没有能换的套餐" description="可以另买一份，或者到期后再来看看。" />
        ) : (
          <div>
            {quote.data.quotes.map((row) => {
              const plan = plans.data!.find((p) => p.id === row.plan_id)
              return plan ? <Row key={row.plan_id} sub={sub} row={row} plan={plan} current={current} /> : null
            })}
          </div>
        )}
      </Card>
      <p className={flowCss.faint}>换了马上生效，链接不变。下一步会把账算清楚再让你确认。</p>
    </div>
  )
}

function Row({ sub, row, plan, current }: { sub: Subscription; row: QuoteRow; plan: Plan; current: Plan | undefined }) {
  const tag = sizeTag(plan, current)
  const month = priceFor(plan, '1m')
  return (
    <div className={css.optRow}>
      <div className={css.optText}>
        <b>{plan.name}</b> {tag === 'bigger' ? <Tag tone="brand">更大</Tag> : tag === 'cheaper' ? <Tag>更便宜</Tag> : null}
        <small>
          {planTraffic(plan)} · {devicesText(plan.max_devices)}
          {month && ` · ${moneyShort(month.unit_amount)}/月`}
        </small>
        <small className={css.optResult}>
          {changeResult(row)}
          {row.period_end && ` · 从今天起算，到 ${day(row.period_end)}`}
        </small>
      </div>
      <a className={css.optButton} href={href('/checkout', { change: sub.id, plan: plan.id })} id={`btn-change-${plan.id}`}>
        换成{plan.name}
      </a>
    </div>
  )
}
