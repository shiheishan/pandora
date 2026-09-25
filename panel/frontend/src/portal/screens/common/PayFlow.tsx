/**
 * [INPUT]: 依赖 react 的 useEffect / useRef / useState，依赖 @tanstack/react-query 的 useMutation / useQueryClient，依赖 zod，依赖 ../../../core/api 的 isApiError，依赖 ../../../core/format 的 formatMoney，依赖 ../../../core/router 的 navigate，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 Button / Modal / Skeleton，依赖 ./orders 的 useOrder / PAID_STATUSES / OrderDetail，依赖 ./catalog 的 PaymentMethod / usePaymentMethods / methodKey，依赖 ./traffic 的 formatDate
 * [OUTPUT]: 对外提供 PayState、PaymentModal、payReturnUrl、paySuccessText
 * [POS]: portal/screens/common 的支付弹窗（用户门户.dc.html 外壳的支付弹窗，契约门户-03 支付条目）：结账页下单后打开它去收银台，0 元订单直接显示成功；订单页的「去支付」用 choose 态先选支付方式；订单页收到收银台回跳（#/orders/<id>?paid=1）时用它轮询确认。无二维码：拿到 GET 跳转就顶层导航，POST 跳转按「暂不可用」处理（CSP form-action 'self' 会拦自动提交表单）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { z } from 'zod'
import { isApiError } from '../../../core/api'
import { formatMoney } from '../../../core/format'
import { navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Modal, Skeleton } from '../../../ui'
import { methodKey, usePaymentMethods, type PaymentMethod } from './catalog'
import css from './PayFlow.module.css'
import { PAID_STATUSES, useOrder, type OrderDetail } from './orders'
import { formatDate } from './traffic'

export type PayState =
  /** 下单成功、待外部支付：弹窗一打开就发起 pay 并跳收银台 */
  | { phase: 'redirect'; orderId: string; orderNo: string; amount: number; currency: string; method: PaymentMethod }
  /** 0 元订单已当场履约，或回跳后确认已支付 */
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
      return '已开通，订阅地址不变'
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

const TITLES: Record<PayState['phase'], string> = { redirect: '正在前往收银台…', done: '支付成功', confirm: '正在确认支付结果', choose: '选择支付方式' }

export function PaymentModal({ state: given, onClose }: { state: PayState | null; onClose: () => void }) {
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
  const title = !state ? '' : state.phase === 'confirm' && confirm.paid ? '支付成功' : TITLES[state.phase]
  return (
    <Modal open={state !== null} onClose={close} title={title} className={css.modal}>
      {state?.phase === 'choose' && <Choose state={state} onPick={(method) => setPicked({ orderId: state.orderId, method })} onLater={close} />}
      {state?.phase === 'redirect' && <Redirecting state={state} onLater={close} onBack={picked ? () => setPicked(null) : undefined} />}
      {state?.phase === 'done' && <Done orderId={state.orderId} onClose={close} />}
      {state?.phase === 'confirm' && <Confirming confirm={confirm} onClose={close} />}
    </Modal>
  )
}

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
// 去收银台：pay 不幂等，但服务层对同渠道复用在途意图（reused=true），重试是安全的
// ---------------------------------------------------------------------------
function Redirecting({ state, onLater, onBack }: { state: Extract<PayState, { phase: 'redirect' }>; onLater: () => void; onBack?: () => void }) {
  const api = useApi()
  const started = useRef(false)
  const pay = useMutation({
    mutationFn: () =>
      api.post(`v1/orders/${encodeURIComponent(state.orderId)}/pay`, intentSchema, {
        body: { provider: state.method.provider, method: state.method.method, return_url: payReturnUrl(state.orderId) },
      }),
    onSuccess: (intent) => {
      // http_method 目前只有 GET；POST 要自动提交表单，会被入口页 CSP 的 form-action 'self' 拦下
      if (intent.http_method === 'GET') window.location.assign(intent.redirect_url)
    },
  })

  useEffect(() => {
    if (started.current) return
    started.current = true
    pay.mutate()
  }, [pay])

  const unsupported = pay.data?.http_method === 'POST'
  const error = unsupported ? '该支付方式暂不可用，请换一种支付方式。' : pay.isError ? payErrorText(pay.error) : null

  return (
    <div className={css.body}>
      <div className={css.amount}>{formatMoney(state.amount, state.currency)}</div>
      <div className={css.note}>
        订单 {state.orderNo} · 30 分钟内有效 · {state.method.label}
      </div>
      {error ? (
        <>
          <div className={css.error} role="alert">
            {error}
          </div>
          {!unsupported && (
            <Button variant="primary" block onClick={() => pay.mutate()} busy={pay.isPending}>
              重试
            </Button>
          )}
          {onBack && (
            <Button block onClick={onBack}>
              换一种支付方式
            </Button>
          )}
        </>
      ) : (
        <div className={css.hint}>{pay.isSuccess ? '正在打开收银台，完成支付后会自动回到这里。' : '正在创建支付…'}</div>
      )}
      <button
        type="button"
        className={css.later}
        onClick={() => {
          onLater()
          navigate('/orders')
        }}
      >
        稍后支付
      </button>
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
          navigate('/overview')
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
