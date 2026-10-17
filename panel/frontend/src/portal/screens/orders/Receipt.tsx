import { useState } from 'react'
import { href } from '../../../core/router'
import { Skeleton, useToast } from '../../../ui'
import { cx, Num } from '../../kit'
import { LoadError } from '../common/Blocks'
import { copyText } from '../common/clients'
import type { QueryOutcome } from '../common/order-query'
import { MethodMark } from '../common/PayFlow'
import { orderReceipt, useOrder } from '../common/orders'
import { PaymentRefresh, RefreshNote } from './Pending'
import css from './Orders.module.css'

/** 行内展开的收据（样稿 .rc）：结果一句、金额行、支付记录、订单号与工单入口 */
export function Receipt({ id }: { id: string }) {
  const detail = useOrder(id)
  const toast = useToast()
  const [outcome, setOutcome] = useState<QueryOutcome>()

  if (detail.isPending) {
    return (
      <div className={css.rc}>
        <Skeleton height={60} />
      </div>
    )
  }
  if (detail.isError) {
    return (
      <div className={css.rc}>
        <LoadError error={detail.error} onRetry={() => void detail.refetch()} what="订单明细" />
      </div>
    )
  }

  const r = orderReceipt(detail.data)
  const pendingRefresh = detail.data.status === 'pending_payment' && detail.data.has_payment_intent

  return (
    <div className={css.rc}>
      <div className={cx(css.rcRes, !r.result.ok && css.rcResOff)}>
        <i aria-hidden="true">{r.result.ok ? '✓' : '–'}</i>
        {r.result.text}
      </div>
      <div className={css.rcBox}>
        {r.lines.map((l) => (
          <div key={l.k} className={cx(css.rcLn, l.total && css.rcTot)}>
            <span>
              {l.k}
              {l.code && <code>{l.code}</code>}
            </span>
            <Num>{l.minus ? '−' : ''}{l.v}</Num>
          </div>
        ))}
      </div>
      {r.payments.length > 0 && (
        <>
          <div className={css.rcH}>支付记录</div>
          {r.payments.map((p, i) => (
            <div key={i} className={css.rcPay}>
              <MethodMark method={p.method} />
              <span className={css.pt}>
                {p.name}
                <small>{p.meta}</small>
              </span>
              <Num>{p.amount}</Num>
            </div>
          ))}
        </>
      )}
      {(pendingRefresh || outcome) && (
        <div className={css.rcActs}>
          {pendingRefresh && <PaymentRefresh orderId={id} onOutcome={setOutcome} size="sm" />}
          <RefreshNote outcome={outcome} />
        </div>
      )}
      <div className={css.rcFoot}>
        <span>
          订单号 <Num className={css.mono}>{detail.data.order_no}</Num>
          <button
            type="button"
            className={css.copy}
            onClick={() =>
              void copyText(detail.data.order_no).then((ok) =>
                toast(ok ? '订单号已复制' : '复制失败，请手动选择', ok ? undefined : 'danger'),
              )
            }
          >
            复制
          </button>
        </span>
        <a href={href('/tickets/new', { order: id })}>对这笔订单有疑问？提交工单 →</a>
      </div>
    </div>
  )
}
