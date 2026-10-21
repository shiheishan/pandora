import type { ReactNode } from 'react'
import { href } from '../../../core/router'
import { Input, Switch, useToast } from '../../../ui'
import { Btn } from '../../kit'
import { methodKey, type PaymentMethod } from '../common/catalog'
import { PayMethodPicker, pickMethod } from '../common/PayFlow'
import { money } from '../common/purchase'
import { useCancelOrder } from '../common/orders'
import type { QuoteRow, RefusalInfo } from '../common/quote'
import type { ConfirmCopy, Note, PayCopy } from './copy'
import css from './Checkout.module.css'

// ---------------------------------------------------------------------------
// 确认页右栏账单卡（样稿 .bill）
// ---------------------------------------------------------------------------

export interface BillProps {
  copy: ConfirmCopy
  pay: PayCopy
  /** 优惠码框（重置单不收券时为 null） */
  coupon: ReactNode
  onUseBalance: (v: boolean) => void
  /** 这单在线要付多少（分，不含手续费），付款方式据此置灰 */
  amount: number
  methods: {
    data?: readonly PaymentMethod[]
    isPending: boolean
    isError: boolean
    refetch: () => unknown
  }
  methodChoice: string | null
  onMethodChoice: (key: string) => void
  refusal: RefusalInfo | null
  onRefusalCleared: () => void
  updated: boolean
  busy: boolean
  blocked: string | null
  onSubmit: () => void
}

export function Bill({
  copy,
  pay,
  coupon,
  onUseBalance,
  amount,
  methods,
  methodChoice,
  onMethodChoice,
  refusal,
  onRefusalCleared,
  updated,
  busy,
  blocked,
  onSubmit,
}: BillProps) {
  const method = pickMethod(methods.data, methodChoice ?? '', amount)

  return (
    <aside className={css.bill} aria-label="账单">
      <h2 className={css.billTitle}>账单</h2>
      {copy.rows.map((r, i) =>
        r.calc ? (
          <div key={i} className={css.blCalc}>
            <div className={css.blRow}>
              <span>{r.k}</span>
              <span className={r.ok ? css.blOkVal : undefined}>{r.v}</span>
            </div>
            <details>
              <summary>怎么算的</summary>
              <p>{r.calc}</p>
            </details>
          </div>
        ) : (
          <div key={i} className={css.bl}>
            <span>{r.k}</span>
            <span className={r.ok ? css.blOkVal : undefined}>{r.v}</span>
          </div>
        ),
      )}
      {coupon !== null && <div className={css.cpn}>{coupon}</div>}
      {pay.balanceLine && (
        <div className={css.bal} id="balance-row">
          <span>
            {pay.balanceLine.strong && <b>{pay.balanceLine.strong}</b>}
            {pay.balanceLine.text}
          </span>
          <Switch
            aria-label="用余额"
            checked={pay.balanceLine.on}
            disabled={pay.balanceLine.locked}
            onChange={(e) => onUseBalance(e.target.checked)}
            id="switch-balance"
          />
        </div>
      )}
      <p className={css.due} id="pay-summary">
        {pay.sum.text}
        {pay.sum.strong && <b>{pay.sum.strong}</b>}
        {pay.sum.after}
        {updated && <span className={css.updated}>已更新</span>}
      </p>
      {updated && <p className={css.dueN}>金额刚变了，已按最新的算好，再点一次就行。</p>}
      {pay.keptNote && (
        <p className={css.dueN} id="min-pay-note">
          {pay.keptNote}
        </p>
      )}
      {pay.needsMethod && (
        <>
          <PayMethodPicker
            methods={methods}
            selected={method}
            onSelect={(m) => onMethodChoice(methodKey(m))}
            label="付款方式"
            amount={amount}
          />
          {method && method.fee !== undefined && method.fee > 0 && method.total !== undefined && (
            <p className={css.dueN}>
              选{method.label}另付手续费 {money(method.fee)}，合计 {money(method.total)}；用余额付不收手续费。
            </p>
          )}
        </>
      )}
      {refusal && <Refusal refusal={refusal} onCleared={onRefusalCleared} />}
      <Btn tone="hot" full className={css.submitBtn} busy={busy} disabled={blocked !== null} onClick={onSubmit} id="btn-submit">
        {blocked ?? pay.button}
      </Btn>
      {pay.tooSmall && (
        <a className={css.walletLink} href={href('/wallet')}>
          去钱包充值（充 ¥1 就够），充完回来再点 →
        </a>
      )}
      {copy.change && <p className={css.again}>↺ {copy.change}</p>}
    </aside>
  )
}

// ---------------------------------------------------------------------------
// 优惠码（在 .cpn 里由 Bill 包一层）
// ---------------------------------------------------------------------------

export function CouponBox({
  open,
  onOpen,
  input,
  onInput,
  applied,
  onApply,
  rows,
}: {
  open: boolean
  onOpen: () => void
  input: string
  onInput: (v: string) => void
  applied: string | null
  onApply: (code: string) => void
  rows: readonly QuoteRow[]
}) {
  if (!open && !applied) {
    return (
      <button type="button" className={css.cpnToggle} onClick={onOpen}>
        有优惠码？
      </button>
    )
  }
  const row = rows[0]
  return (
    <div className={css.cpnBody}>
      <div className={css.cpnRow}>
        <Input
          mono
          aria-label="优惠码"
          placeholder="例如 AUTUMN26"
          value={input}
          onChange={(e) => onInput(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && onApply(input.trim().toUpperCase())}
          fieldClassName={css.cpnGrow}
          className={css.upper}
        />
        {applied ? (
          <Btn
            tone="soft"
            onClick={() => {
              onInput('')
              onApply('')
            }}
          >
            不用了
          </Btn>
        ) : (
          <Btn tone="soft" onClick={() => onApply(input.trim().toUpperCase())} disabled={!input.trim()}>
            使用
          </Btn>
        )}
      </div>
      {applied && row && (row.coupon_error ? <p className={css.bad}>{row.coupon_error}</p> : row.discount > 0 ? <p className={css.good}>已用优惠码 {applied}，省 {money(row.discount)}</p> : null)}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 报价或建单被拒
// ---------------------------------------------------------------------------

/**
 * 报价或建单被拒：同一套餐有一张还没付款的新购单，或同一份有一张没付的续费、叠加、换套餐单（order_pending，fields.order_id）时给「去付款 / 取消它」；
 * 那张已超过付款期限（fields.lapsed）时只给「取消它」。取消后重新报价，可以直接再点一次。
 */
export function Refusal({ refusal, onCleared }: { refusal: RefusalInfo; onCleared: () => void }) {
  const toast = useToast()
  const cancel = useCancelOrder()
  const lapsed = refusal.lapsed
  return (
    <div className={css.refuse} role="alert">
      {refusal.text}
      {refusal.kind === 'order_pending' && refusal.orderId && (
        <div className={css.refuseActions}>
          <Btn
            tone="soft"
            size="sm"
            busy={cancel.isPending}
            // 用 mutateAsync 而不是 mutate 的回调：取消成功会让报价重新拉取，报价阶段的这个提示框随之卸载，
            // 卸载后 mutate 的回调不再触发（提示「已取消」会时有时无）；promise 照样兑现
            onClick={() =>
              void cancel.mutateAsync(refusal.orderId!).then(
                () => {
                  toast('那张单已取消，没有扣钱。现在可以重新下单了')
                  onCleared()
                },
                (e: unknown) => toast((e instanceof Error && e.message) || '没取消成，请稍后再试', 'danger'),
              )
            }
          >
            取消它
          </Btn>
          {!lapsed && (
            <Btn tone="soft" size="sm" href={href(`/orders/${refusal.orderId}`)}>
              去付款
            </Btn>
          )}
        </div>
      )}
    </div>
  )
}

export function NoteLine({ note }: { note: Note }) {
  return typeof note === 'string' ? (
    <>{note}</>
  ) : (
    <>
      <b>{note.b}</b>
      {note.rest}
    </>
  )
}
