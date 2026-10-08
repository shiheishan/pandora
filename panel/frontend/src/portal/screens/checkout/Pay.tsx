import { useEffect } from 'react'
import { href, navigate, useHashLocation } from '../../../core/router'
import { Card, Empty, Skeleton } from '../../../ui'
import { usePageHead } from '../../head'
import { LoadError } from '../common/Blocks'
import { methodKey, usePaymentMethods } from '../common/catalog'
import { flowCss } from '../common/Flow'
import { isPayable, PAID_STATUSES, useOrder } from '../common/orders'
import { PayPanel } from '../common/PayFlow'
import { placedOrders } from './model'

/** 收银台付完回跳到完成页（相对入口页解析，门户在子路径下也成立） */
export function doneReturnUrl(orderId: string, query: URLSearchParams, base = window.location.href): string {
  const q = new URLSearchParams(query)
  q.delete('m')
  const search = q.toString()
  return new URL(`./#/checkout/done/${encodeURIComponent(orderId)}${search ? `?${search}` : ''}`, base).href
}

/**
 * 付款（原型 pay）：#/checkout/pay/<订单>?m=<付款方式>&<完成页上下文>。付完自动去完成页；
 * 「换个付款方式」退回确认页（同样的单会被认出来，不下第二张）。
 */
export function Pay({ orderId }: { orderId: string }) {
  usePageHead('付款', '/orders')
  const { query } = useHashLocation()
  const order = useOrder(orderId)
  const methods = usePaymentMethods()
  const doneTo = () => navigate(`/checkout/done/${orderId}`, { query: Object.fromEntries([...query].filter(([k]) => k !== 'm')), replace: true })
  const paid = order.data !== undefined && PAID_STATUSES.has(order.data.status)
  useEffect(() => {
    if (paid) doneTo()
  }, [paid]) // eslint-disable-line react-hooks/exhaustive-deps
  if (order.isPending || methods.isPending || paid) return <Skeleton height={260} />
  const failed = order.isError ? order : methods.isError ? methods : null
  if (failed) return <LoadError error={failed.error} onRetry={() => void failed.refetch()} what="订单" />
  const o = order.data!
  const method = methods.data!.find((m) => methodKey(m) === query.get('m')) ?? methods.data![0]
  if (!isPayable(o) || !method) {
    return (
      <Empty
        title={!method ? '暂时没有能用的付款方式' : '这单已经不能付了'}
        description={!method ? '稍后再试，或者在确认页打开余额付。' : '可能已经超过 30 分钟自动取消了，没有扣钱。回去重新确认一次就行。'}
        action={<a href={href('/subs')}>回到我的套餐</a>}
      />
    )
  }
  return (
    <Card className={flowCss.narrow}>
      <PayPanel
        orderId={orderId}
        amount={o.payable_amount}
        currency={o.currency}
        method={method}
        returnUrl={doneReturnUrl(orderId, query)}
        onPaid={doneTo}
        onUnpayable={() => placedOrders.forget()}
        onOtherMethod={() => window.history.back()}
      />
    </Card>
  )
}
