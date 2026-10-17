import { href } from '../../../core/router'
import { isApiError } from '../../../core/api'
import { Row } from '../../kit'
import { gb, leftOf, money, resetsLeft } from '../common/purchase'
import { useQuote } from '../common/quote'
import type { Subscription } from '../common/subscriptions'

/**
 * 流量不够页的「重置流量」行（kit Row）：报价来自报价接口，重置单永远按原价、不收券。
 * 重置只把已用清零、送的流量保留，所以「马上能用满」写上限 + 送的（leftOf 的 total）。
 */
export function ResetOffer({ sub }: { sub: Subscription }) {
  const left = resetsLeft(sub)
  const scope = sub.plan_kind === 'one_time' ? '有效期内' : '本期'
  const title = sub.plan_kind === 'one_time' ? '重置流量' : '重置本期流量'
  const { total } = leftOf(sub)
  const limitText = total !== null ? gb(total) : '限额'
  const n = left ?? 0
  const quote = useQuote(sub.reset_price === null || n === 0 ? null : { action: 'reset', subscription_id: sub.id })

  if (sub.reset_price === null) return null

  if (n === 0) {
    return <Row icon="reset" title={title} sub={`${scope}的 ${sub.reset_limit} 次已用完`} />
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
      return <Row icon="reset" title={title} sub="有一张待付款的重置订单" value="去订单" href={href('/orders')} />
    }
    if (isApiError(quote.error, 'conflict')) {
      return <Row icon="reset" title={title} sub={quote.error.message} />
    }
    return null
  }

  const row = quote.data?.quotes[0]
  if (!row) return null

  const desc =
    sub.plan_kind === 'one_time'
      ? `把已用的流量清零，马上能用满 ${limitText}；到期时间不变`
      : `把这一期已用的流量清零，马上能用满 ${limitText}；到期时间不变`
  // 还没用流量时那句提示拼进小字末尾
  const unusedNote = sub.traffic_used === 0 ? ` · ${scope}还没用流量，现在重置不会多出流量` : ''

  return (
    <Row
      icon="reset"
      title={title}
      sub={`${desc}${quotaHint ? ` · ${quotaHint}` : ''}${unusedNote}`}
      value={money(row.total)}
      valueTone="num"
      href={href('/checkout', { reset: sub.id })}
    />
  )
}
