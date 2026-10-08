import { useEffect } from 'react'
import { href, navigate, useHashLocation } from '../../../core/router'
import { Button, Card, Empty, Skeleton } from '../../../ui'
import { usePageHead } from '../../head'
import { LoadError } from '../common/Blocks'
import { methodKey, usePaymentMethods } from '../common/catalog'
import { flowCss } from '../common/Flow'
import { isPayable, PAID_STATUSES, useCancelOrder, useOrder } from '../common/orders'
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
  const cancel = useCancelOrder()
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
    // 还挂着待支付、但过了付款期限：给「取消这张单」，取消后回选购页（A 路：发起支付会回 409）
    const lapsed = method !== undefined && o.status === 'pending_payment'
    return (
      <Empty
        title={!method ? '暂时没有能用的付款方式' : lapsed ? '这张单已超过付款期限' : '这单已经不能付了'}
        description={!method ? '稍后再试，或者在确认页打开余额付。' : lapsed ? '取消它（不会扣钱），再重新下单就行。' : '可能已经超过 30 分钟自动取消了，没有扣钱。回去重新确认一次就行。'}
        action={
          lapsed ? (
            <Button variant="primary" busy={cancel.isPending} onClick={() => cancel.mutate(orderId, { onSuccess: () => navigate('/plans', { replace: true }) })}>
              取消这张单，重新下单
            </Button>
          ) : (
            <a href={href('/subs')}>回到我的套餐</a>
          )
        }
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
        afterCancel={() => navigate('/plans', { replace: true })}
      />
    </Card>
  )
}
