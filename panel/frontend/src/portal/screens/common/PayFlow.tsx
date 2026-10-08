import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { z } from 'zod'
import { isApiError } from '../../../core/api'
import { formatMoney } from '../../../core/format'
import { navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Modal, Skeleton } from '../../../ui'
import { methodKey, usePaymentMethods, type PaymentMethod } from './catalog'
import { detectDevice } from './clients'
import { queryFailure, queryOutcome, useQueryOrderPayment, type QueryOutcome } from './order-query'
import css from './PayFlow.module.css'
import { PAID_STATUSES, useCancelOrder, useOrder, type OrderDetail } from './orders'
import { QrCode } from './qr'
import { formatDate } from './traffic'

export type PayState =
  /** 下单成功、待外部支付：弹窗一打开就发起 pay 并跳收银台 */
  | { phase: 'redirect'; orderId: string; orderNo: string; amount: number; currency: string; method: PaymentMethod }
  /** 0 元订单已当场履约，或回跳后确认已支付（含在付款面板里轮询到已付） */
  | { phase: 'done'; orderId: string }
  /** 收银台回跳：轮询订单直到 paid / fulfilled，最多 2 分钟 */
  | { phase: 'confirm'; orderId: string }
  /** 已有的待支付订单（订单页「去支付」）：先选支付方式，选完进入 redirect */
  | { phase: 'choose'; orderId: string; orderNo: string; amount: number; currency: string }

/** 回跳地址：相对入口页解析，令门户在后台前缀或子路径下也成立；后端要求以 PublicBaseURL + "/" 开头 */
export function payReturnUrl(orderId: string, base = window.location.href): string {
  return new URL(`./#/orders/${encodeURIComponent(orderId)}?paid=1`, base).href
}

/** 支付成功文案，按订单种类（契约门户-03 支付条目） */
export function paySuccessText(o: Pick<OrderDetail, 'kind' | 'paid_amount' | 'total_amount' | 'currency' | 'subscription_period_end'>): string {
  switch (o.kind) {
    case 'topup':
      return `余额 +${formatMoney(o.paid_amount || o.total_amount, o.currency)}`
    case 'addon':
      return '流量包已到账'
    case 'upgrade':
      return '已换好，链接不变'
    default:
      return o.subscription_period_end ? `已开通，有效期至 ${formatDate(o.subscription_period_end)}` : '已开通'
  }
}

const intentSchema = z.object({
  intent_id: z.string(),
  http_method: z.enum(['GET', 'POST']),
  redirect_url: z.string(),
  form_fields: z.record(z.string(), z.string()),
  amount: z.number().int(),
  currency: z.string(),
  reused: z.boolean(),
})

/** 回跳后要刷新的读数：余额、订阅、流量包、订单全在 portal 前缀下 */
function useRefreshAccount() {
  const client = useQueryClient()
  return () => void client.invalidateQueries({ queryKey: ['portal'] })
}

const TITLES: Record<PayState['phase'], string> = { redirect: '付款', done: '支付成功', confirm: '正在确认支付结果', choose: '选择支付方式' }

/**
 * onUnpayable：支付接口回 409（订单已不可支付——别处取消、已超时或已付掉）时调用，
 * 结账与充值借它 forget() 记下的待支付单，下次同样的请求重新下单
 */
export function PaymentModal({ state: given, onClose, onUnpayable }: { state: PayState | null; onClose: () => void; onUnpayable?: () => void }) {
  // choose 态选定后在弹窗内部转成 redirect；换了订单或关掉就作废
  const [picked, setPicked] = useState<{ orderId: string; method: PaymentMethod } | null>(null)
  const state: PayState | null =
    given?.phase === 'choose' && picked?.orderId === given.orderId
      ? { phase: 'redirect', orderId: given.orderId, orderNo: given.orderNo, amount: given.amount, currency: given.currency, method: picked.method }
      : given
  const close = () => {
    setPicked(null)
    onClose()
  }
  const confirm = useConfirmation(state?.phase === 'confirm' ? state.orderId : undefined)
  // 付款面板里轮询到已付：就地换成成功态
  const [paidHere, setPaidHere] = useState<string | null>(null)
  const title = !state ? '' : (state.phase === 'confirm' && confirm.paid) || paidHere === stateOrder(state) ? '支付成功' : TITLES[state.phase]
  return (
    <Modal open={state !== null} onClose={close} title={title} className={css.modal}>
      {state?.phase === 'choose' && <Choose state={state} onPick={(method) => setPicked({ orderId: state.orderId, method })} onLater={close} />}
      {state?.phase === 'redirect' &&
        (paidHere === state.orderId ? (
          <Done orderId={state.orderId} onClose={close} />
        ) : (
          <PayPanel
            orderId={state.orderId}
            amount={state.amount}
            currency={state.currency}
            method={state.method}
            returnUrl={payReturnUrl(state.orderId)}
            onPaid={() => setPaidHere(state.orderId)}
            onUnpayable={onUnpayable}
            onOtherMethod={picked ? () => setPicked(null) : undefined}
            onLater={() => {
              close()
              navigate('/orders')
            }}
          />
        ))}
      {state?.phase === 'done' && <Done orderId={state.orderId} onClose={close} />}
      {state?.phase === 'confirm' && <Confirming confirm={confirm} onClose={close} />}
    </Modal>
  )
}

const stateOrder = (s: PayState) => s.orderId

// ---------------------------------------------------------------------------
// 选支付方式：只列能收该币种的方式（GET v1/payment-methods，修订 R61）
// ---------------------------------------------------------------------------
function Choose({ state, onPick, onLater }: { state: Extract<PayState, { phase: 'choose' }>; onPick: (m: PaymentMethod) => void; onLater: () => void }) {
  const methods = usePaymentMethods()
  return (
    <div className={css.body}>
      <div className={css.amount}>{formatMoney(state.amount, state.currency)}</div>
      <div className={css.note}>订单 {state.orderNo}</div>
      {methods.isPending ? (
        <Skeleton height={40} radius="var(--radius-md)" />
      ) : methods.isError ? (
        <div className={css.error} role="alert">
          支付方式读取失败，
          <button type="button" className={css.inlineRetry} onClick={() => void methods.refetch()}>
            重试
          </button>
        </div>
      ) : methods.data.length === 0 ? (
        <div className={css.hint}>暂无可用的支付方式，请稍后再试。</div>
      ) : (
        <div className={css.methods}>
          {methods.data.map((m) => (
            <Button key={methodKey(m)} block onClick={() => onPick(m)}>
              {m.label}
            </Button>
          ))}
        </div>
      )}
      <button type="button" className={css.later} onClick={onLater}>
        稍后支付
      </button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 付款面板（原型 pay）：确认页的付款页与订单页、钱包的支付弹窗共用。
//   手机：主按钮「打开支付宝付款」（顶层 GET 跳收银台），二维码收进「用另一台手机扫码」；
//   电脑：直接给收银台地址的二维码，用手机扫；放不进二维码（易支付地址带签名、回跳与商品名，常常很长）
//         时退回「打开付款页」，收银台页面上自己有码。
//   付完回到这里会自动更新（每 3 秒查一次订单）；没更新再点「我已付款」主动查单。
// pay 不幂等，但服务层对同渠道复用在途意图（reused=true），重进页面是安全的。
// ---------------------------------------------------------------------------
const POLL_PAY_MS = 3000

/** 发起支付回 409「这张订单已超过付款期限，请取消后重新下单」（billing.ErrOrderPaymentExpired，没有单独的码） */
export const isPaymentLapsed = (e: unknown) => isApiError(e, 'conflict') && e.message.includes('超过付款期限')

export interface PayPanelProps {
  orderId: string
  amount: number
  currency: string
  method: PaymentMethod
  /** 收银台付完回跳到哪：确认页是完成页，订单页与钱包是订单页的 ?paid=1 */
  returnUrl: string
  onPaid: () => void
  onUnpayable?: () => void
  /** 换个付款方式（确认页退回去重选、弹窗回到选方式） */
  onOtherMethod?: () => void
  /** 稍后再付 */
  onLater?: () => void
  /** 订单过了付款期限、取消之后去哪（确认页的付款页回选购页） */
  afterCancel?: () => void
}

export function PayPanel({ orderId, amount, currency, method, returnUrl, onPaid, onUnpayable, onOtherMethod, onLater, afterCancel }: PayPanelProps) {
  const cancel = useCancelOrder()
  const api = useApi()
  const started = useRef(false)
  const [opened, setOpened] = useState(false)
  const [outcome, setOutcome] = useState<QueryOutcome | null>(null)
  const order = useOrder(orderId, POLL_PAY_MS)
  const query = useQueryOrderPayment()
  const paid = order.data !== undefined && PAID_STATUSES.has(order.data.status)
  const closed = order.data?.status === 'cancelled' || order.data?.status === 'expired'
  const phone = (() => {
    const d = detectDevice(navigator.userAgent, navigator.maxTouchPoints)
    return d === 'ios' || d === 'android'
  })()
  const pay = useMutation({
    mutationFn: () => api.post(`v1/orders/${encodeURIComponent(orderId)}/pay`, intentSchema, { body: { provider: method.provider, method: method.method, return_url: returnUrl } }),
    onError: (e) => {
      if (isApiError(e, 'conflict')) onUnpayable?.()
    },
  })

  useEffect(() => {
    if (started.current) return
    started.current = true
    pay.mutate()
  }, [pay])

  useEffect(() => {
    if (paid) onPaid()
  }, [paid]) // eslint-disable-line react-hooks/exhaustive-deps

  const redirect = pay.data?.http_method === 'GET' ? pay.data.redirect_url : null
  const unsupported = pay.data?.http_method === 'POST'
  const final = isApiError(pay.error) && pay.error.status >= 400 && pay.error.status < 500
  const error = closed ? (order.data?.status === 'expired' ? '这单超过 30 分钟没付，已经自动取消了，没有扣钱。' : '这单已经取消了，没有扣钱。') : unsupported ? '这个付款方式暂时用不了，换一个付款方式。' : pay.isError ? payErrorText(pay.error) : null
  const after = <p className={css.hint}>{opened ? '正在等付款结果……' : ''}付完回到这里会自动更新；没更新再点下面的「我已付款」。</p>
  const qr = redirect ? <QrCode value={redirect} label={`用${method.label}扫码付款`} className={css.qr} /> : null

  function check() {
    setOutcome(null)
    query.mutate(orderId, { onSuccess: (r) => setOutcome(queryOutcome(r)), onError: (e) => setOutcome(queryFailure(e)) })
  }

  return (
    <div className={css.body} data-screen="pay">
      <p className={css.note}>用{method.label}付</p>
      <div className={css.amount}>{formatMoney(amount, currency)}</div>
      {error ? (
        <>
          <div className={css.error} role="alert">
            {error}
          </div>
          {!closed && !unsupported && !final && (
            <Button variant="primary" block onClick={() => pay.mutate()} busy={pay.isPending}>
              重试
            </Button>
          )}
          {isPaymentLapsed(pay.error) && (
            <Button variant="primary" block busy={cancel.isPending} onClick={() => cancel.mutate(orderId, { onSuccess: () => (afterCancel ? afterCancel() : onLater?.()) })}>
              取消这张单，重新下单
            </Button>
          )}
        </>
      ) : !redirect ? (
        <div className={css.hint}>正在准备付款…</div>
      ) : phone ? (
        <>
          <a className={css.open} href={redirect} onClick={() => setOpened(true)} id="btn-open-pay">
            打开{method.label}付款
          </a>
          {after}
          <details className={css.others}>
            <summary>用另一台手机扫码</summary>
            {qr ?? <p className={css.hint}>这个付款地址太长，放不进二维码，请直接点上面的按钮。</p>}
          </details>
        </>
      ) : qr ? (
        <>
          {qr}
          <p>
            <b>用手机{method.label}扫码付款</b>
          </p>
          {after}
          <a className={css.textLink} href={redirect} onClick={() => setOpened(true)}>
            不方便扫码？在这台电脑上打开付款页
          </a>
        </>
      ) : (
        <>
          <a className={css.open} href={redirect} onClick={() => setOpened(true)} id="btn-open-pay">
            打开{method.label}付款页
          </a>
          <p className={css.hint}>付款页上有二维码，用手机{method.label}扫一下。</p>
          {after}
        </>
      )}
      {!closed && (
        <Button block onClick={check} busy={query.isPending} id="btn-paid">
          我已付款
        </Button>
      )}
      {outcome && (
        <div className={outcome.kind === 'failed' ? css.error : css.hint} role="status">
          {outcome.text}
        </div>
      )}
      {onOtherMethod && !closed && (
        <button type="button" className={css.later} onClick={onOtherMethod}>
          换个付款方式
        </button>
      )}
      {onLater && !closed && (
        <button type="button" className={css.later} onClick={onLater}>
          稍后再付
        </button>
      )}
      <p className={css.hint}>30 分钟内没付，订单自动取消，不会扣钱。</p>
    </div>
  )
}

function payErrorText(error: unknown): string {
  if (isApiError(error, 'network_error')) return '网络连接失败，检查网络后重试。'
  if (isApiError(error, 'internal_error')) return '支付渠道暂时不可用，稍后重试或换一种支付方式。'
  return error instanceof Error && error.message ? error.message : '发起支付失败，请稍后重试。'
}

// ---------------------------------------------------------------------------
// 成功态
// ---------------------------------------------------------------------------
function Done({ orderId, onClose }: { orderId: string; onClose: () => void }) {
  const order = useOrder(orderId)
  const refresh = useRefreshAccount()
  // 打开即刷新一次账户读数（余额、订阅、流量包）
  useEffect(() => refresh(), []) // eslint-disable-line react-hooks/exhaustive-deps
  return <SuccessBody order={order.data} loading={order.isPending} onClose={onClose} />
}

function SuccessBody({ order, loading, onClose }: { order: OrderDetail | undefined; loading: boolean; onClose: () => void }) {
  return (
    <div className={css.body}>
      <div className={css.check} aria-hidden="true">
        ✓
      </div>
      <div className={css.note}>{loading ? <Skeleton width={160} height={16} /> : order ? paySuccessText(order) : '订单已完成'}</div>
      <Button
        variant="primary"
        block
        data-autofocus
        onClick={() => {
          onClose()
          navigate('/subs')
        }}
      >
        完成
      </Button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 回跳确认：3 秒一次、最多 2 分钟；orders.changed 到了也会立即重拉
// ---------------------------------------------------------------------------
const POLL_MS = 3000
const POLL_LIMIT_MS = 120_000

function useConfirmation(orderId: string | undefined) {
  const [timedOut, setTimedOut] = useState(false)
  const order = useOrder(orderId, timedOut ? false : POLL_MS)
  const refresh = useRefreshAccount()
  const status = order.data?.status
  const paid = status !== undefined && PAID_STATUSES.has(status)
  const closed = status === 'cancelled' || status === 'expired'

  useEffect(() => {
    if (orderId === undefined) return
    const timer = setTimeout(() => setTimedOut(true), POLL_LIMIT_MS)
    return () => clearTimeout(timer)
  }, [orderId])
  // 转为已支付时刷新余额、订阅、流量包
  useEffect(() => {
    if (paid) refresh()
  }, [paid]) // eslint-disable-line react-hooks/exhaustive-deps
  return { order, status, paid, closed, timedOut }
}

function Confirming({ confirm, onClose }: { confirm: ReturnType<typeof useConfirmation>; onClose: () => void }) {
  const { order, status, paid, closed, timedOut } = confirm
  if (paid) return <SuccessBody order={order.data} loading={false} onClose={onClose} />

  const message = order.isError
    ? isApiError(order.error, 'not_found')
      ? '找不到这个订单。'
      : '暂时查不到支付结果，可稍后在订单中查看。'
    : closed
      ? status === 'expired'
        ? '订单已超时取消，没有扣款。'
        : '订单已取消。'
      : timedOut
        ? '支付结果确认中，可稍后在订单中查看。'
        : null

  return (
    <div className={css.body}>
      {message ? <div className={css.note}>{message}</div> : <div className={css.hint}>收银台已返回，正在确认支付结果…</div>}
      {order.data && (
        <div className={css.note}>
          订单 {order.data.order_no} · {formatMoney(order.data.payable_amount, order.data.currency)}
        </div>
      )}
      <Button block onClick={onClose} data-autofocus>
        {message ? '查看订单' : '稍后查看'}
      </Button>
    </div>
  )
}
