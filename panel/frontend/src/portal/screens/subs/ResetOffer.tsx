import { href } from '../../../core/router'
import { isApiError } from '../../../core/api'
import { money } from '../common/purchase'
import { useQuote } from '../common/quote'
import type { Subscription } from '../common/subscriptions'
import { pickTrafficQuota } from '../common/traffic'
import { gb } from '../common/purchase'
import css from './Subs.module.css'

export function ResetOffer({ sub }: { sub: Subscription }) {
  const quote = useQuote({ action: 'reset', subscription_id: sub.id })

  if (quote.isPending) return null

  if (quote.isError) {
    if (isApiError(quote.error, 'order_pending')) {
      return (
        <div className={css.optRow}>
          <div className={css.optText}>
            <b>重置本期流量</b>
            <small>有一张待付款的重置订单</small>
          </div>
          <a className={css.optButton} href={href('/orders')}>
            去订单
          </a>
        </div>
      )
    }
    return null
  }

  const row = quote.data?.quotes[0]
  if (!row) return null

  const limit = pickTrafficQuota(sub.quotas)?.limit
  const limitText = limit ? gb(limit) : '限额'
  const btnText = `重置 · ${money(row.total)}`

  return (
    <div className={css.optRow}>
      <div className={css.optText}>
        <b>重置本期流量</b>
        <small>把这一期已用的流量清零，马上能用满 {limitText}；到期时间不变</small>
      </div>
      <a className={css.optButton} href={href('/checkout', { reset: sub.id })}>
        {btnText}
      </a>
    </div>
  )
}
