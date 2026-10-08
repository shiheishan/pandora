import { useEffect, useState } from 'react'
import { href, useHashLocation } from '../../../core/router'
import { Card, Empty, Skeleton } from '../../../ui'
import { usePageHead } from '../../head'
import { useBalance } from '../../queries'
import { LoadError } from '../common/Blocks'
import { usePlans } from '../common/catalog'
import { flowCss } from '../common/Flow'
import { useHoldings } from '../common/holdings'
import { ImportSheet, type ImportTarget } from '../common/ImportSheet'
import { LinkBox } from '../common/LinkBox'
import { PAID_STATUSES, useOrder } from '../common/orders'
import { ResultView } from '../common/Result'
import { doneLines, findNewSub, readDone } from './result-copy'

const POLL_MS = 3000
const POLL_LIMIT_MS = 120_000

/**
 * 完成（原型 result）：#/checkout/done/<订单>?k=…。收银台回跳时订单可能还没入账，3 秒查一次、最多 2 分钟。
 * 「发生了什么 / 你要做的 / 没变的」；新买一份把新链接与「添加到 App」放在最上面。
 */
export function Done({ orderId }: { orderId: string }) {
  usePageHead('完成', '/subs')
  const { query } = useHashLocation()
  const [timedOut, setTimedOut] = useState(false)
  const order = useOrder(orderId, timedOut ? false : POLL_MS)
  const h = useHoldings()
  const plans = usePlans()
  const balance = useBalance()
  const [importing, setImporting] = useState<ImportTarget | null>(null)
  const ctx = readDone(query)
  const paid = order.data !== undefined && PAID_STATUSES.has(order.data.status)

  useEffect(() => {
    const timer = setTimeout(() => setTimedOut(true), POLL_LIMIT_MS)
    return () => clearTimeout(timer)
  }, [])

  if (order.isPending || h.subs.isPending) return <Skeleton height={260} />
  if (order.isError) return <LoadError error={order.error} onRetry={() => void order.refetch()} what="订单" />
  const o = order.data!
  if (!paid) {
    const closed = o.status === 'cancelled' || o.status === 'expired'
    return (
      <Card className={flowCss.narrow}>
        <Empty
          bare
          title={closed ? '这单没有付成' : timedOut ? '还没收到付款结果' : '正在确认付款结果…'}
          description={closed ? '订单已经取消了，没有扣钱。' : timedOut ? '如果已经付了，过一会儿在订单里看看；30 分钟内没付会自动取消，不会扣钱。' : '付完一般几秒就到，这一页会自己更新。'}
          action={<a href={href(closed ? '/subs' : `/orders/${orderId}`)}>{closed ? '回到我的套餐' : '去订单里看看'}</a>}
        />
      </Card>
    )
  }
  const sub = ctx.kind === 'new' ? findNewSub(o, h.held) : h.held.find((s) => s.id === ctx.subId)
  const plan = plans.data?.find((p) => p.id === sub?.plan_id)
  const lines = doneLines(ctx, o, sub, plan, h.held, h.naming, balance.data?.balance ?? null)
  const next =
    lines.showNewLink && sub ? (
      <>
        <p className={flowCss.lead}>在要用它的设备上把新链接添加到 App：</p>
        <LinkBox
          sub={sub}
          url={h.urlOf(sub.id)}
          naming={h.naming}
          label="新链接"
          onImport={() => {
            const url = h.urlOf(sub.id)
            if (url) setImporting({ sub, url })
          }}
        />
      </>
    ) : lines.updateHint ? (
      <p className={flowCss.lead}>{lines.updateHint}</p>
    ) : undefined
  return (
    <>
      <ResultView info={{ title: lines.title, next, happened: lines.happened, kept: lines.kept, changeable: lines.changeable }} />
      <ImportSheet target={importing} naming={h.naming} onClose={() => setImporting(null)} />
    </>
  )
}
