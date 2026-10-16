import { href } from '../../../core/router'
import { isApiError } from '../../../core/api'
import { gb, resetsLeft, money } from '../common/purchase'
import { useQuote } from '../common/quote'
import type { Subscription } from '../common/subscriptions'
import css from './Subs.module.css'

export function ResetOffer({ sub }: { sub: Subscription }) {
  const left = resetsLeft(sub)
  const scope = sub.plan_kind === 'one_time' ? '有效期内' : '本期'
  const title = sub.plan_kind === 'one_time' ? '重置流量' : '重置本期流量'
  const limitText = sub.traffic_limit !== null ? gb(sub.traffic_limit) : '限额'
  const n = left ?? 0
  const quote = useQuote(sub.reset_price === null || n === 0 ? null : { action: 'reset', subscription_id: sub.id })

  if (sub.reset_price === null) return null

  if (n === 0) {
    return (
      <div className={css.optRow}>
        <div className={css.optText}>
          <b>{title}</b>
          <small>{scope}的 {sub.reset_limit} 次已用完</small>
        </div>
      </div>
    )
  }

  const quotaHint =
    left === Number.POSITIVE_INFINITY
      ? null
      : left !== null
        ? sub.plan_kind === 'one_time'
          ? `有效期内还能买 ${left} 次`
          : `本期还能买 ${left} 次`
        : null

  if (quote.isPending) return null

  if (quote.isError) {
    if (isApiError(quote.error, 'order_pending')) {
      return (
        <div className={css.optRow}>
          <div className={css.optText}>
            <b>{title}</b>
            {quotaHint && <small>{quotaHint}</small>}
            <small>有一张待付款的重置订单</small>
          </div>
          <a className={css.optButton} href={href('/orders')}>
            去订单
          </a>
        </div>
      )
    }
    if (isApiError(quote.error, 'conflict')) {
      return (
        <div className={css.optRow}>
          <div className={css.optText}>
            <b>{title}</b>
            {quotaHint && <small>{quotaHint}</small>}
            <small>{quote.error.message}</small>
          </div>
        </div>
      )
    }
    return null
  }

  const row = quote.data?.quotes[0]
  if (!row) return null

  const btnText = `重置 · ${money(row.total)}`
  const desc =
    sub.plan_kind === 'one_time'
      ? `把已用的流量清零，马上能用满 ${limitText}；到期时间不变`
      : `把这一期已用的流量清零，马上能用满 ${limitText}；到期时间不变`

  return (
    <div className={css.optRow}>
      <div className={css.optText}>
        <b>{title}</b>
        {quotaHint && <small>{quotaHint}</small>}
        <small>{desc}</small>
        {sub.traffic_used === 0 && <small>{scope}还没用流量，现在重置不会多出流量</small>}
      </div>
      <a className={css.optButton} href={href('/checkout', { reset: sub.id })}>
        {btnText}
      </a>
    </div>
  )
}
