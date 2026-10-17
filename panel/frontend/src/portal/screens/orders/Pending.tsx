import { useEffect, useState } from 'react'
import { formatMoney } from '../../../core/format'
import { ConfirmModal, Tag, useToast } from '../../../ui'
import { Btn, cx, GroupHeader, Num } from '../../kit'
import { LoadError } from '../common/Blocks'
import { queryFailure, queryOutcome, useQueryOrderPayment, type QueryOutcome } from '../common/order-query'
import type { PayState } from '../common/PayFlow'
import {
  expiryNote,
  orderTitle,
  shortDateTime,
  useCancelOrder,
  useOpenOrders,
  type OrderRow,
} from '../common/orders'
import css from './Orders.module.css'

/** 每 30 秒刷新一次「现在」，让待支付倒计时重算 */
function useNow(ms: number): Date {
  const [now, setNow] = useState(() => new Date())
  useEffect(() => {
    const id = setInterval(() => setNow(new Date()), ms)
    return () => clearInterval(id)
  }, [ms])
  return now
}

/** 取消确认写明退回什么：用过的优惠码、冻结的余额各自写；都没有就说没有扣钱 */
export function cancelBody(
  o: Pick<OrderRow, 'kind' | 'plan_name' | 'item_name' | 'interval' | 'interval_count' | 'payable_amount' | 'balance_applied' | 'discount_amount'>,
): string {
  let text = `${orderTitle(o)} · ${formatMoney(o.payable_amount)}。`
  const balance = o.balance_applied
  const discount = o.discount_amount
  if (balance > 0 && discount > 0) {
    text += `取消后用过的优惠码和冻结的 ${formatMoney(balance)} 余额会立即退回。`
  } else if (balance > 0) {
    text += `取消后冻结的 ${formatMoney(balance)} 余额会立即退回。`
  } else if (discount > 0) {
    text += `取消后用过的优惠码会退回，可以再用。`
  } else {
    text += `取消后这张单作废，没有扣钱。`
  }
  return text
}

/**
 * 「我已支付，刷新状态」：付完钱订单还挂着（回调丢了或还没到）时，让后端去渠道查一次。
 * 结果分三种显示在结果条里：已到账、渠道尚未确认、查询失败。
 */
export function PaymentRefresh({
  orderId,
  onOutcome,
  size,
}: {
  orderId: string
  onOutcome: (o: QueryOutcome) => void
  size?: 'sm' | 'md'
}) {
  const query = useQueryOrderPayment()
  const toast = useToast()
  const run = async () => {
    try {
      const outcome = queryOutcome(await query.mutateAsync(orderId))
      onOutcome(outcome)
      // 到账后订单离开待支付，卡片与按钮随之消失，结果条可能来不及被看到：再弹一条
      if (outcome.kind === 'settled') toast(outcome.text)
    } catch (e) {
      onOutcome(queryFailure(e))
    }
  }
  return (
    <Btn tone="soft" size={size ?? 'md'} busy={query.isPending} onClick={() => void run()}>
      {query.isPending ? '正在向支付渠道查询…' : '我已支付，刷新状态'}
    </Btn>
  )
}

export function RefreshNote({ outcome: o }: { outcome: QueryOutcome | undefined }) {
  if (!o) return null
  return (
    <p className={css.pendNote} data-kind={o.kind} role="status">
      {o.text}
    </p>
  )
}

/** 待支付卡片置顶：每张单一张，后端 30 分钟不付自动取消 */
export function PendingOrders({ onPay }: { onPay: (s: PayState) => void }) {
  const open = useOpenOrders()
  const cancel = useCancelOrder()
  const toast = useToast()
  const now = useNow(30_000)
  const [confirming, setConfirming] = useState<OrderRow | null>(null)
  const [outcomes, setOutcomes] = useState<Record<string, QueryOutcome>>({})

  if (open.isPending) return null
  if (open.isError) return <LoadError error={open.error} onRetry={() => void open.refetch()} what="待支付订单" />
  if (!open.data.orders.length) return null

  async function doCancel(o: OrderRow) {
    try {
      const r = await cancel.mutateAsync(o.id)
      toast(r.already_terminal ? '订单已经取消了' : '订单已取消')
    } catch (e) {
      toast(e instanceof Error ? e.message : '取消失败', 'danger')
    } finally {
      setConfirming(null)
    }
  }

  return (
    <>
      <GroupHeader className={css.pendHead} aside="30 分钟内不付会自动取消">
        待支付 {open.data.count} 笔
      </GroupHeader>
      {open.data.orders.map((o) => (
        <section key={o.id} className={css.pend} aria-label={orderTitle(o)}>
          <div className={css.pendTop}>
            <Tag tone="warn">待支付</Tag>
            <span className={css.cd}>{expiryNote(o.expires_at, now)}</span>
          </div>
          <div className={css.pendMain}>
            <div>
              <b className={css.pendT}>{orderTitle(o)}</b>
              <small>
                <Num className={css.mono}>{o.order_no}</Num> · {shortDateTime(o.created_at)}
              </small>
            </div>
            <Num className={css.pendAmt}>{formatMoney(o.payable_amount)}</Num>
          </div>
          <div className={cx(css.pendActs, o.has_payment_intent && css.pendActsTwo)}>
            <Btn
              onClick={() => onPay({ phase: 'choose', orderId: o.id, orderNo: o.order_no, amount: o.payable_amount })}
            >
              去支付
            </Btn>
            {o.has_payment_intent && (
              <PaymentRefresh orderId={o.id} onOutcome={(r) => setOutcomes((m) => ({ ...m, [o.id]: r }))} />
            )}
            {o.cancellable && (
              <button type="button" className={css.lnk} onClick={() => setConfirming(o)}>
                取消订单
              </button>
            )}
          </div>
          <RefreshNote outcome={outcomes[o.id]} />
        </section>
      ))}
      <ConfirmModal
        open={confirming !== null}
        title="取消这笔订单？"
        body={confirming ? cancelBody(confirming) : ''}
        confirmLabel="取消订单"
        cancelLabel="再想想"
        tone="danger"
        onConfirm={() => (confirming ? doCancel(confirming) : undefined)}
        onCancel={() => setConfirming(null)}
      />
    </>
  )
}
