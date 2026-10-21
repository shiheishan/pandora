import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useEffect, useId, useRef, useState, type KeyboardEvent } from 'react'
import { z } from 'zod'
import { isApiError } from '../../../core/api'
import { formatMoney } from '../../../core/format'
import { navigate } from '../../../core/router'
import { useBalance } from '../../queries'
import { useApi } from '../../../shell/runtime'
import { Skeleton, useToast } from '../../../ui'
import { Btn, cx, Icon, Num, Sheet } from '../../kit'
import { methodKey, usePaymentMethods, belowMethodMinimum, usableMethods, type PaymentMethod } from './catalog'
import { detectDevice } from './clients'
import { feeLine, minLine, surchargeNote } from './fee'
import { queryFailure, queryOutcome, useQueryOrderPayment, type QueryOutcome } from './order-query'
import css from './PayFlow.module.css'
import { archivedRefundText, isPlanArchivedRefund, orderTitle, PAID_STATUSES, rebuyQuery, useCancelOrder, useOrder, type OrderDetail } from './orders'
import { afterOrderCheck, payFailureAction, PAY_IN_PROGRESS_MAX_RETRIES } from './payRetry'
import { QrCode } from './qr'
import { formatDate } from './traffic'

export type PayState =
  /** 下单成功、待外部支付：弹窗一打开就发起 pay 并跳收银台 */
  | { phase: 'redirect'; orderId: string; orderNo: string; amount: number; method: PaymentMethod }
  /** 0 元订单已当场履约，或回跳后确认已支付（含在付款面板里轮询到已付） */
  | { phase: 'done'; orderId: string }
  /** 收银台回跳：轮询订单直到 paid / fulfilled，最多 2 分钟 */
  | { phase: 'confirm'; orderId: string }
  /** 已有的待支付订单（订单页「去支付」）：先选支付方式，选完进入 redirect */
  | { phase: 'choose'; orderId: string; orderNo: string; amount: number }

/** 回跳地址：相对入口页解析，令门户在后台前缀或子路径下也成立；后端要求以 PublicBaseURL + "/" 开头 */
export function payReturnUrl(orderId: string, base = window.location.href): string {
  return new URL(`./#/orders/${encodeURIComponent(orderId)}?paid=1`, base).href
}

/** 支付成功文案，按订单种类（契约门户-03 支付条目） */
export function paySuccessText(o: Pick<OrderDetail, 'kind' | 'paid_amount' | 'total_amount' | 'subscription_period_end'>): string {
  switch (o.kind) {
    case 'topup':
      return `余额 +${formatMoney(o.paid_amount || o.total_amount)}`
    case 'stack':
      return '已叠加，总流量加上了'
    case 'traffic_reset':
      return '本期流量已清零'
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
  surcharge: z.number().int().nonnegative().default(0),
  reused: z.boolean(),
})

/** 回跳后要刷新的读数：余额、套餐、订单全在 portal 前缀下 */
function useRefreshAccount() {
  const client = useQueryClient()
  return () => void client.invalidateQueries({ queryKey: ['portal'] })
}

const TITLES: Record<PayState['phase'], string> = { redirect: '付款', done: '支付成功', confirm: '正在确认支付结果', choose: '去支付' }

/**
 * onUnpayable：支付接口确认订单已不可支付（别处取消、已超时或已付掉等）时调用，
 * 结账与充值借它 forget() 记下的待支付单，下次同样的请求重新下单。
 * 409 conflict 会先刷新订单，只有确认不是待付才调；payment_in_progress 从不调。
 */
export function PaymentModal({ state: given, onClose, onUnpayable }: { state: PayState | null; onClose: () => void; onUnpayable?: () => void }) {
  // choose 态选定后在弹窗内部转成 redirect；换了订单或关掉就作废
  const [picked, setPicked] = useState<{ orderId: string; method: PaymentMethod } | null>(null)
  const state: PayState | null =
    given?.phase === 'choose' && picked?.orderId === given.orderId
      ? { phase: 'redirect', orderId: given.orderId, orderNo: given.orderNo, amount: given.amount, method: picked.method }
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
    <Sheet open={state !== null} onClose={close} title={title}>
      {state?.phase === 'choose' && <Choose state={state} onPick={(method) => setPicked({ orderId: state.orderId, method })} onLater={close} />}
      {state?.phase === 'redirect' &&
        (paidHere === state.orderId ? (
          <Done orderId={state.orderId} onClose={close} />
        ) : (
          <PayPanel
            orderId={state.orderId}
            amount={state.amount}
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
    </Sheet>
  )
}

const stateOrder = (s: PayState) => s.orderId

// ---------------------------------------------------------------------------
// 付款方式单选（样稿 .pays / .pay）
// ---------------------------------------------------------------------------

/** 选中的付款方式：先筛出付得了这笔应付的方式；按 key 在可用列表里找，找不到取第一个；没有可用方式时为 null */
export function pickMethod(list: readonly PaymentMethod[] | undefined, key: string, amount?: number): PaymentMethod | null {
  if (!list || list.length === 0) return null
  const usable = usableMethods(list, amount)
  if (usable.length === 0) return null
  if (key) {
    const hit = usable.find((m) => methodKey(m) === key)
    if (hit) return hit
  }
  return usable[0] ?? null
}

interface MethodsQuery {
  data?: readonly PaymentMethod[]
  isPending: boolean
  isError: boolean
  refetch: () => unknown
}

export function MethodMark({ method }: { method: string | undefined }) {
  if (method === 'alipay') {
    return (
      <span className={cx(css.pi, css.piAli)} aria-hidden="true">
        支
      </span>
    )
  }
  if (method === 'wxpay' || method === 'wechat') {
    return (
      <span className={cx(css.pi, css.piWx)} aria-hidden="true">
        微
      </span>
    )
  }
  return (
    <span className={cx(css.pi, css.piOther)} aria-hidden="true">
      <Icon name="card" size={17} />
    </span>
  )
}

/** 样稿 .sh-lbl + .pays：付款方式单选行（钱包充值、订单页去支付共用） */
export function PayMethodPicker({
  methods,
  selected,
  onSelect,
  label = '支付方式',
  /** 这单应付（分，不含手续费），给了就把低于最低额的方式置灰 */
  amount,
}: {
  methods: MethodsQuery
  selected: PaymentMethod | null
  onSelect: (m: PaymentMethod) => void
  label?: string
  amount?: number
}) {
  const labelId = useId()
  const list = methods.data ?? []
  const selectedKey = selected ? methodKey(selected) : ''

  const onRadiogroupKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const usable = usableMethods(list, amount)
    if (usable.length === 0) return
    const vertical = e.key === 'ArrowDown' || e.key === 'ArrowUp'
    const horizontal = e.key === 'ArrowRight' || e.key === 'ArrowLeft'
    if (!vertical && !horizontal) return
    e.preventDefault()
    const idx = usable.findIndex((m) => methodKey(m) === selectedKey)
    const cur = idx >= 0 ? idx : 0
    const next =
      e.key === 'ArrowDown' || e.key === 'ArrowRight' ? (cur + 1) % usable.length : (cur - 1 + usable.length) % usable.length
    const m = usable[next]
    if (!m) return
    onSelect(m)
    const buttons = e.currentTarget.querySelectorAll<HTMLButtonElement>('[role="radio"]:not([disabled])')
    const focusIdx = usable.findIndex((u) => methodKey(u) === methodKey(m))
    buttons[focusIdx]?.focus()
  }

  const firstUsableIdx = list.findIndex((m) => !belowMethodMinimum(m, amount))

  return (
    <div className={css.picker}>
      <div className={css.pickLabel} id={labelId}>{label}</div>
      {methods.isPending ? (
        <Skeleton height={56} radius="14px" />
      ) : methods.isError ? (
        <div className={css.error} role="alert">
          支付方式读取失败，
          <button type="button" className={css.inlineRetry} onClick={() => void methods.refetch()}>
            重试
          </button>
        </div>
      ) : list.length === 0 ? (
        <div className={css.hint}>暂无可用的支付方式，请稍后再试。</div>
      ) : (
        <div className={css.pays} role="radiogroup" aria-labelledby={labelId} onKeyDown={onRadiogroupKeyDown}>
          {list.map((m, i) => {
            const off = belowMethodMinimum(m, amount)
            const on = !off && selected !== null && methodKey(m) === methodKey(selected)
            const line = off ? minLine(m) : feeLine(m)
            const tab = on || (selected === null && i === firstUsableIdx) ? 0 : -1
            return (
              <button
                key={methodKey(m)}
                type="button"
                role="radio"
                aria-checked={on}
                aria-disabled={off ? 'true' : undefined}
                disabled={off}
                tabIndex={tab}
                className={cx(css.pay, on && css.payOn, off && css.payOff)}
                onClick={() => !off && onSelect(m)}
              >
                <MethodMark method={m.method} />
                <span className={css.pt}>
                  {m.label}
                  {line && <small>{line}</small>}
                </span>
                <span className={css.dot} aria-hidden="true" />
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 选支付方式：列当前可用的方式（GET v1/payment-methods，修订 R61）
// ---------------------------------------------------------------------------
function Choose({ state, onPick, onLater }: { state: Extract<PayState, { phase: 'choose' }>; onPick: (m: PaymentMethod) => void; onLater: () => void }) {
  const methods = usePaymentMethods(state.amount)
  const order = useOrder(state.orderId)
  const [key, setKey] = useState('')
  const method = pickMethod(methods.data, key, state.amount)
  const noUsable = methods.data !== undefined && methods.data.length > 0 && usableMethods(methods.data, state.amount).length === 0
  return (
    <div className={css.sheetBody}>
      <div className={css.ps}>
        <span className={css.psWhat}>
          {order.data ? orderTitle(order.data) : '订单'}
          <small>
            <Num>{state.orderNo}</Num>
          </small>
        </span>
        <Num className={css.psAmt}>{formatMoney(state.amount)}</Num>
      </div>
      <PayMethodPicker methods={methods} selected={method} amount={state.amount} onSelect={(m) => setKey(methodKey(m))} />
      {noUsable && (
        <p className={css.hint}>这张单的金额低于现在所有付款方式的最低额，在线付不了；可以取消后改用余额付。</p>
      )}
      <Btn tone="hot" full disabled={!method} onClick={() => method && onPick(method)}>
        去支付 <Num>{formatMoney(method?.total ?? state.amount)}</Num>
      </Btn>
      {order.data && <SwitchToBalance order={order.data} onLeave={onLater} />}
      <button type="button" className={css.later} onClick={onLater}>
        稍后支付
      </button>
    </div>
  )
}

/**
 * 改用余额付（用户 10-16 定：已下的单也能用余额付）。余额只在下单时冻结，已下的单补不进去，
 * 所以是取消这张（不扣钱、优惠码退回），带着同样的套餐与价格档回确认页重下，余额在那里默认用上。
 * 充值单、余额为 0、认不出原来买的是什么的不显示。onLeave：在弹窗里时先关弹窗。
 */
export function SwitchToBalance({ order, onLeave }: { order: OrderDetail; onLeave?: () => void }) {
  const balance = useBalance()
  const cancel = useCancelOrder()
  const toast = useToast()
  const rebuy = rebuyQuery(order)
  const left = balance.data?.balance ?? 0
  if (!rebuy || left <= 0 || order.status !== 'pending_payment') return null
  return (
    <div className={css.swb}>
      <p>
        余额还有 <b><Num>{formatMoney(left)}</Num></b>。想用余额付：先取消这张单（不扣钱，用过的优惠码退回），再按现在的价格回到确认页，余额自动用上。
      </p>
      <Btn
        tone="soft"
        size="sm"
        busy={cancel.isPending}
        onClick={() =>
          void cancel.mutateAsync(order.id).then(
            () => {
              onLeave?.()
              navigate('/checkout', { query: rebuy })
            },
            // 单已被付掉、网络断等：留在原处说一声，不吞掉
            (e: unknown) => toast((e instanceof Error && e.message) || '没取消成，请稍后再试', 'danger'),
          )
        }
      >
        取消这张单，改用余额付
      </Btn>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 付款面板（原型 pay）：确认页的付款页与订单页、钱包的支付弹窗共用。
//   手机：主按钮「打开支付宝付款」（顶层 GET 跳收银台），二维码收进「用另一台手机扫码」；
//   电脑：直接给收银台地址的二维码，用手机扫；下面是次按钮「在这台电脑上付款」（没带手机时用）；放不进二维码（易支付地址带签名、回跳与商品名，常常很长）
//         时退回「打开付款页」，收银台页面上自己有码。
//   付完回到这里会自动更新（每 3 秒查一次订单）；没更新再点「我已付款」主动查单。
// pay 不幂等，但服务层对同渠道复用在途意图（reused=true），重进页面是安全的。
// ---------------------------------------------------------------------------
const POLL_PAY_MS = 3000

/** 发起支付回 409 order_lapsed：这张单不能再付，只能取消（超过付款期限，或套餐已停售 fields.reason=plan_archived） */
export const isPaymentLapsed = (e: unknown) => isApiError(e, 'order_lapsed')

/** 不能再付时取消按钮的说法：套餐停售了不能「重新下单」 */
export function lapsedCancelLabel(e: unknown): string {
  return isApiError(e, 'order_lapsed') && e.fields.reason === 'plan_archived' ? '取消这张订单' : '取消这张单，重新下单'
}

/** 套餐归档后才付款（10-16）的结果一句话：不开通，钱已转进钱包余额 */
export function archivedPaidText(o: Pick<OrderDetail, 'surcharge_kept'>): string {
  return `套餐已停售，没有开通；${archivedRefundText(o.surcharge_kept)}。`
}

interface PayPanelProps {
  orderId: string
  amount: number
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

export function PayPanel({ orderId, amount, method, returnUrl, onPaid, onUnpayable, onOtherMethod, onLater, afterCancel }: PayPanelProps) {
  const cancel = useCancelOrder()
  const api = useApi()
  const started = useRef(false)
  const [opened, setOpened] = useState(false)
  const [outcome, setOutcome] = useState<QueryOutcome | null>(null)
  const [attempts, setAttempts] = useState(0)
  const attemptsRef = useRef(0)
  const [superseded, setSuperseded] = useState(false)
  const [conflictRefreshFailed, setConflictRefreshFailed] = useState(false)
  const retryTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
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
      const action = payFailureAction(e, attemptsRef.current)
      if (action.kind === 'retry_later') {
        if (retryTimer.current) clearTimeout(retryTimer.current)
        retryTimer.current = setTimeout(() => {
          attemptsRef.current += 1
          setAttempts(attemptsRef.current)
          pay.mutate()
        }, action.delayMs)
        return
      }
      if (action.kind === 'check_order') {
        void order.refetch().then((r) => {
          const a = afterOrderCheck(r.data?.status, r.isError)
          if (a === 'unpayable') onUnpayable?.()
          else if (a === 'still_payable') {
            setSuperseded(true)
            setConflictRefreshFailed(false)
          } else setConflictRefreshFailed(true)
        })
        return
      }
      if (action.kind === 'unpayable') onUnpayable?.()
    },
  })

  useEffect(() => {
    return () => {
      if (retryTimer.current) clearTimeout(retryTimer.current)
    }
  }, [])

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
  const inProgressWaiting = pay.isError && isApiError(pay.error, 'payment_in_progress') && attempts < PAY_IN_PROGRESS_MAX_RETRIES
  // below_method_minimum：换一种付款方式才能付，不给重试，靠「换个付款方式」；final 为真时不显示「重试」按钮，错误区走 payErrorText 显示后端原话
  const final =
    isApiError(pay.error) &&
    pay.error.status >= 400 &&
    pay.error.status < 500 &&
    !isApiError(pay.error, 'payment_in_progress') &&
    !(isApiError(pay.error, 'conflict') && (superseded || conflictRefreshFailed))
  const retryPay = () => {
    attemptsRef.current = 0
    setAttempts(0)
    setSuperseded(false)
    setConflictRefreshFailed(false)
    pay.mutate()
  }
  const archived = order.data !== undefined && isPlanArchivedRefund(order.data)
  const toWallet = () => {
    onLater?.()
    navigate('/wallet')
  }
  const error = closed
    ? archived
      ? archivedPaidText(order.data!)
      : order.data?.status === 'expired'
        ? '这单超过 30 分钟没付，已经自动取消了，没有扣钱。'
        : '这单已经取消了，没有扣钱。'
    : unsupported
      ? '这个付款方式暂时用不了，换一个付款方式。'
      : superseded
        ? '这次付款已被新的发起取代，请重试。'
        : pay.isError
          ? payErrorText(pay.error)
          : null
  const after = <p className={css.hint}>{opened ? '正在等付款结果……' : ''}付完回到这里会自动更新；没更新再点下面的「我已付款」。</p>
  const qr = redirect ? <QrCode value={redirect} label={`用${method.label}扫码付款`} className={css.qr} /> : null

  function check() {
    setOutcome(null)
    query.mutate(orderId, { onSuccess: (r) => setOutcome(queryOutcome(r)), onError: (e) => setOutcome(queryFailure(e)) })
  }

  const finalAmount = pay.data?.amount ?? method.total ?? amount
  const note = pay.data ? surchargeNote(pay.data.surcharge, pay.data.amount) : method.fee && method.fee > 0 ? feeLine(method) : ''

  return (
    <div className={css.body} data-screen="pay">
      <p className={css.note}>用{method.label}付</p>
      <div className={css.amount}>
        <Num>{formatMoney(finalAmount)}</Num>
      </div>
      {note && <div className={css.hint}>{note}</div>}
      {inProgressWaiting ? (
        <div className={css.hint}>正在发起支付，请稍候…</div>
      ) : error ? (
        <>
          <div className={css.error} role="alert">
            {error}
          </div>
          {!closed && !unsupported && !final && (
            <Btn tone="primary" full onClick={retryPay} busy={pay.isPending}>
              重试
            </Btn>
          )}
          {isPaymentLapsed(pay.error) && (
            <Btn tone="primary" full busy={cancel.isPending} onClick={() => cancel.mutate(orderId, { onSuccess: () => (afterCancel ? afterCancel() : onLater?.()) })}>
              {lapsedCancelLabel(pay.error)}
            </Btn>
          )}
          {archived && (
            <Btn tone="primary" full onClick={toWallet}>
              去钱包看看
            </Btn>
          )}
        </>
      ) : !redirect ? (
        <div className={css.hint}>正在准备付款…</div>
      ) : phone ? (
        <>
          <Btn href={redirect} tone="hot" full onClick={() => setOpened(true)} id="btn-open-pay">
            打开{method.label}付款
          </Btn>
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
          <a className={css.openHere} href={redirect} onClick={() => setOpened(true)} id="btn-open-pay-here">
            在这台电脑上付款
            <small>{payHereNote(method)}</small>
          </a>
        </>
      ) : (
        <>
          <Btn href={redirect} tone="hot" full onClick={() => setOpened(true)} id="btn-open-pay">
            打开{method.label}付款页
          </Btn>
          <p className={css.hint}>付款页上有二维码，用手机{method.label}扫一下。</p>
          {after}
        </>
      )}
      {!closed && (
        <Btn tone="soft" full onClick={check} busy={query.isPending} id="btn-paid">
          我已付款
        </Btn>
      )}
      {outcome && (
        <div className={outcome.kind === 'failed' ? css.error : css.hint} role="status">
          {outcome.text}
        </div>
      )}
      {outcome?.wallet && !archived && (
        <Btn tone="primary" full onClick={toWallet}>
          去钱包看看
        </Btn>
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

/** 电脑上的次按钮写清它是干什么的：没带手机时直接在这台电脑上付（二维码仍是主路径） */
export function payHereNote(method: Pick<PaymentMethod, 'method' | 'label'>): string {
  return method.method === 'alipay' ? '没带手机？在这台电脑上用支付宝网页付' : `没带手机？在这台电脑上打开${method.label}付款页，按页面提示付`
}

function payErrorText(error: unknown): string {
  if (isApiError(error, 'network_error')) return '网络连接失败，检查网络后重试。'
  if (isApiError(error, 'internal_error')) return '支付渠道暂时不可用，稍后重试或换一种支付方式。'
  if (isApiError(error, 'payment_in_progress')) return '支付还在发起，请稍后点重试。'
  return error instanceof Error && error.message ? error.message : '发起支付失败，请稍后重试。'
}

// ---------------------------------------------------------------------------
// 成功态
// ---------------------------------------------------------------------------
function Done({ orderId, onClose }: { orderId: string; onClose: () => void }) {
  const order = useOrder(orderId)
  const refresh = useRefreshAccount()
  // 打开即刷新一次账户读数（余额、套餐）
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
      <Btn
        tone="primary"
        full
        data-autofocus
        onClick={() => {
          onClose()
          navigate('/subs')
        }}
      >
        完成
      </Btn>
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
  // 转为已支付、或套餐停售转了余额时刷新余额、套餐
  const refunded = order.data !== undefined && isPlanArchivedRefund(order.data)
  useEffect(() => {
    if (paid || refunded) refresh()
  }, [paid, refunded]) // eslint-disable-line react-hooks/exhaustive-deps
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
        : order.data && isPlanArchivedRefund(order.data)
          ? archivedPaidText(order.data)
          : '订单已取消。'
      : timedOut
        ? '支付结果确认中，可稍后在订单中查看。'
        : null

  return (
    <div className={css.body}>
      {message ? <div className={css.note}>{message}</div> : <div className={css.hint}>收银台已返回，正在确认支付结果…</div>}
      {order.data && (
        <div className={css.note}>
          订单 {order.data.order_no} · {formatMoney(order.data.payable_amount)}
        </div>
      )}
      <Btn tone="soft" full onClick={onClose} data-autofocus>
        {message ? '查看订单' : '稍后查看'}
      </Btn>
      {order.data && isPlanArchivedRefund(order.data) && (
        <Btn
          tone="soft"
          full
          onClick={() => {
            onClose()
            navigate('/wallet')
          }}
        >
          去钱包看看
        </Btn>
      )}
    </div>
  )
}
