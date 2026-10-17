import { href } from '../../../core/router'
import { Empty, Skeleton } from '../../../ui'
import { usePageHead } from '../../head'
import { Btn, Chip, cx } from '../../kit'
import { LoadError } from '../common/Blocks'
import { devicesText, KIND_NAME, planTraffic, priceFor, usePlans, validityText, type Plan } from '../common/catalog'
import { useHoldings } from '../common/holdings'
import { day, money, moneyShort, perPeriod } from '../common/purchase'
import { tierOf, useQuote, type QuoteRow } from '../common/quote'
import type { Subscription } from '../common/subscriptions'
import css from './Pages.module.css'

/** 换过去要付多少：「换过去，今天付 ¥21.00」/「换过去今天不用付，还退 ¥9.00 到钱包余额」 */
function changeResult(row: Pick<QuoteRow, 'total' | 'refund'>): string {
  if (row.total > 0) return `换过去，今天付 ${money(row.total)}`
  if (row.refund > 0) return `换过去今天不用付，还退 ${money(row.refund)} 到钱包余额`
  return '换过去今天不用付'
}

/** 同一档月价比较：比现在贵的标「更大」，便宜的标「更便宜」；只在同类型的按时间套餐之间比 */
function sizeTag(target: Plan, current: Plan | undefined): 'bigger' | 'cheaper' | null {
  if (!current || target.kind !== current.kind || target.kind === 'one_time') return null
  const a = priceFor(target, '1m')?.unit_amount
  const b = priceFor(current, '1m')?.unit_amount
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
  if (!sub || !sub.changeable)
    return (
      <Empty
        title="这一份现在不能换套餐"
        description="已经停用的套餐不能换，可以另买一份。"
        action={
          <Btn href={href('/plans')} tone="soft">
            去选购
          </Btn>
        }
      />
    )
  const current = plans.data!.find((p) => p.id === sub.plan_id)
  const price = sub.renewal_price
  return (
    <div className={cx(css.page, css.narrow)}>
      <p className={css.now}>
        {h.naming.multi ? `「${h.naming.dn(sub)}」` : '你'}现在是 <b>{sub.plan_name}</b>
        {current && `：${planTraffic(current)} · ${devicesText(current.max_devices)}`}
        {price && price.billing_interval !== 'one_time' && ` · ${moneyShort(price.unit_amount)}${perPeriod(price.billing_interval, price.interval_count)}`}
      </p>
      {quote.isPending ? (
        <div className={css.card}>
          <Skeleton height={120} />
        </div>
      ) : quote.isError ? (
        <div className={css.card}>
          <LoadError error={quote.error} onRetry={() => void quote.refetch()} what="价格" />
        </div>
      ) : quote.data.quotes.length === 0 ? (
        <div className={css.card}>
          <Empty bare title="暂时没有能换的套餐" description="可以另买一份，或者到期后再来看看。" />
        </div>
      ) : (
        <div className={css.opts}>
          {[...new Set(quote.data.quotes.map((q) => q.plan_id))]
            .filter((planId) => planId !== sub.plan_id)
            .map((planId) => {
              const plan = plans.data!.find((p) => p.id === planId)
              const row = tierOf(quote.data!.quotes, sub, (q) => q.plan_id === planId)
              return plan && row ? <Option key={planId} sub={sub} row={row} plan={plan} current={current} /> : null
            })}
        </div>
      )}
      <p className={css.faint}>换了马上生效，链接不变。下一步会把账算清楚再让你确认。</p>
    </div>
  )
}

/** 一行选项：左边套餐名与事实，右边「换成X」按钮（浏览器测试按这个链接的文字找行） */
function Option({ sub, row, plan, current }: { sub: Subscription; row: QuoteRow; plan: Plan; current: Plan | undefined }) {
  const tag = sizeTag(plan, current)
  const month = plan.kind === 'subscription' ? priceFor(plan, '1m') : undefined
  const priceNote =
    plan.kind === 'one_time'
      ? validityText(row.validity_days ?? priceFor(plan, 'once')?.validity_days ?? null)
      : month
        ? `${moneyShort(month.unit_amount)}${perPeriod(month.billing_interval, month.interval_count)}`
        : `${perPeriod(row.interval, row.interval_count)}`
  return (
    <div className={css.opt}>
      <div className={css.optText}>
        <span className={css.optName}>
          {plan.name}
          <Chip>{KIND_NAME[plan.kind]}</Chip>
          {tag === 'bigger' && <Chip tone="solid">更大</Chip>}
          {tag === 'cheaper' && <Chip>更便宜</Chip>}
        </span>
        <span className={css.optFacts}>
          {planTraffic(plan)} · {devicesText(plan.max_devices)} · {priceNote}
        </span>
        <span className={css.optResult}>
          {changeResult(row)}
          {row.period_end && ` · 从今天起算，到 ${day(row.period_end)}`}
        </span>
      </div>
      <Btn size="sm" tone={tag === 'bigger' ? 'primary' : 'soft'} href={href('/checkout', { change: sub.id, plan: plan.id })} id={`btn-change-${plan.id}`}>
        换成{plan.name}
      </Btn>
    </div>
  )
}
