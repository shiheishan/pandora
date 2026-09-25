/**
 * [INPUT]: 依赖 react 的 useState，依赖 @tanstack/react-query 的 useMutation / useQuery / useQueryClient，依赖 zod，依赖 ../../../core/api 的 isApiError，依赖 ../common/intent 的 useIntentKey / usePlacedOrder / endsIntent，依赖 ../../../core/format 的 formatMoney，依赖 ../../../core/router 的 href / useHashLocation，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 Button / Card / Empty / Input / Skeleton / Switch，依赖 ../../queries 的 useBalance / useSubscriptions，依赖 ../common 的目录、订单、支付弹窗与 LoadError，依赖 ./model 的模式与预览逻辑
 * [OUTPUT]: 默认导出 Checkout 页面组件（登记表 React.lazy 的目标）
 * [POS]: portal/screens/checkout 的入口：确认订单（门户-03 结账页）。按地址参数进四种模式——新购、续费（含遇改价）、变更套餐（服务端试算折算与退余额）、流量包；左栏选周期 / 容量、优惠码、余额抵扣开关、支付方式，右栏订单预览与提交；下单后交给 common/PayFlow 的支付弹窗；幂等键成功或 4xx 后丢弃，刚下的待支付单记在 usePlacedOrder，同样的请求 30 分钟内再点就重开它的支付，不下第二张
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState, type ReactNode } from 'react'
import { z } from 'zod'
import { isApiError } from '../../../core/api'
import { formatMoney } from '../../../core/format'
import { href, useHashLocation } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Card, Empty, Input, Skeleton, Switch } from '../../../ui'
import { useBalance, useSubscriptions } from '../../queries'
import { LoadError } from '../common/Blocks'
import { methodKey, periodName, periodOf, perGbNote, savingAmount, usePackCatalog, usePaymentMethods, usePlans } from '../common/catalog'
import { endsIntent, useIntentKey, usePlacedOrder } from '../common/intent'
import { orderCreatedSchema } from '../common/orders'
import { PaymentModal, type PayState } from '../common/PayFlow'
import { compactBytes, formatDate } from '../common/traffic'
import css from './Checkout.module.css'
import {
  buildQuote,
  couponNote,
  couponPreviewBody,
  defaultPriceId,
  isRepriced,
  normalizeCoupon,
  orderRequest,
  periodOptions,
  resolveMode,
  type ChangePreview,
  type CheckoutMode,
} from './model'

export default function Checkout() {
  const { query } = useHashLocation()
  const plans = usePlans()
  const packs = usePackCatalog()
  const subs = useSubscriptions()
  const queries = [plans, packs, subs]

  if (queries.some((q) => q.isPending)) {
    return (
      <div className={css.grid}>
        <Card aria-busy="true">
          <Skeleton width={180} height={18} />
          <Skeleton height={150} />
        </Card>
        <Card aria-busy="true">
          <Skeleton height={180} />
        </Card>
      </div>
    )
  }
  const failed = queries.find((q) => q.isError)
  if (failed) {
    return (
      <Card>
        <LoadError error={failed.error} onRetry={() => queries.forEach((q) => void q.refetch())} what="订单信息" />
      </Card>
    )
  }

  const result = resolveMode(query, plans.data!, packs.data!, subs.data!)
  if ('problem' in result) return <Problem kind={result.problem} />
  const mode = result.mode
  // 模式或目标变了就整块重建，选项、优惠码与幂等键都从头来
  const identity = mode.kind === 'pack' ? `pack:${mode.pack.id}` : `${mode.kind}:${mode.kind === 'new' ? '' : mode.sub.id}:${mode.plan?.id ?? ''}`
  return <CheckoutForm key={identity} mode={mode} requestedPrice={query.get('price')} />
}

const PROBLEMS = {
  missing: ['还没有选择要购买的商品', '先到选购页挑一个套餐或流量包。'],
  plan_gone: ['该套餐已停售', '请选购其他套餐。'],
  pack_gone: ['这个流量包已下架', '请选购其他容量的流量包。'],
  sub_gone: ['这条订阅已不能续费', '订阅可能已到期或已取消，可以重新选购套餐。'],
} as const

function Problem({ kind }: { kind: keyof typeof PROBLEMS }) {
  const [title, description] = PROBLEMS[kind]
  return (
    <Empty
      title={title}
      description={description}
      action={
        <a className={css.linkButton} href={href('/plans', kind === 'pack_gone' ? { tab: 'packs' } : undefined)}>
          去选购
        </a>
      }
    />
  )
}

// ---------------------------------------------------------------------------
// 优惠码试算 / 变更套餐试算的响应
// ---------------------------------------------------------------------------
const couponPreviewSchema = z.object({
  subtotal: z.number().int(),
  discount: z.number().int(),
  payable: z.number().int(),
  currency: z.string(),
  coupon: z.object({ code: z.string(), discount_type: z.enum(['percent', 'fixed']), discount_value: z.number().int() }).nullable().optional(),
})

const changePreviewSchema = z.object({
  direction: z.enum(['upgrade', 'downgrade']),
  currency: z.string(),
  subtotal: z.number().int(),
  proration_credit: z.number().int(),
  discount: z.number().int(),
  total: z.number().int(),
  balance_refund: z.number().int(),
  current_period_end: z.string(),
  new_period_start: z.string(),
  new_period_end: z.string(),
}) satisfies z.ZodType<ChangePreview>

function useChangePreview(mode: CheckoutMode, priceId: string | null, code: string | null) {
  const api = useApi()
  const change = mode.kind === 'change' ? mode : null
  return useQuery({
    queryKey: ['portal', 'change-preview', change?.sub.id, change?.plan.id, priceId, code],
    queryFn: ({ signal }) =>
      api.post(`v1/me/subscriptions/${encodeURIComponent(change!.sub.id)}/change-plan/preview`, changePreviewSchema, {
        body: { plan_id: change!.plan.id, price_id: priceId, ...(code ? { coupon_code: code } : {}) },
        signal,
      }),
    enabled: change !== null && priceId !== null,
    retry: false,
  })
}

// ---------------------------------------------------------------------------
// 结账表单
// ---------------------------------------------------------------------------
function CheckoutForm({ mode, requestedPrice }: { mode: CheckoutMode; requestedPrice: string | null }) {
  const api = useApi()
  const client = useQueryClient()
  const balance = useBalance()
  const methods = usePaymentMethods()
  const packs = usePackCatalog()

  const options = periodOptions(mode)
  const [priceId, setPriceId] = useState(() => defaultPriceId(mode, options, requestedPrice))
  const [packId, setPackId] = useState(mode.kind === 'pack' ? mode.pack.id : null)
  const [couponInput, setCouponInput] = useState('')
  const [appliedCode, setAppliedCode] = useState<string | null>(null)
  const [useBalanceOn, setUseBalance] = useState(false)
  const [methodChoice, setMethodChoice] = useState<string | null>(null)
  const [payState, setPayState] = useState<PayState | null>(null)
  const intentKey = useIntentKey()
  const placed = usePlacedOrder<Extract<PayState, { phase: 'redirect' }>>()

  // 流量包模式里换容量等于换商品
  const pack = mode.kind === 'pack' ? (packs.data?.find((p) => p.id === packId) ?? mode.pack) : null
  const target: CheckoutMode = pack ? { kind: 'pack', pack } : mode
  const price = options.find((p) => p.id === priceId) ?? null

  // 优惠码试算（变更套餐的优惠码走变更试算）
  const couponBody = appliedCode && target.kind !== 'change' ? couponPreviewBody(target, priceId, appliedCode) : null
  const coupon = useQuery({
    queryKey: ['portal', 'coupon-preview', couponBody],
    queryFn: ({ signal }) => api.post('v1/coupons/preview', couponPreviewSchema, { body: couponBody, signal }),
    enabled: couponBody !== null,
    retry: false,
  })
  const change = useChangePreview(target, priceId, null)
  const changeWithCoupon = useChangePreview(target, priceId, appliedCode)
  const changeData = appliedCode && changeWithCoupon.isSuccess ? changeWithCoupon.data : change.data

  const couponState = !appliedCode
    ? null
    : target.kind === 'change'
      ? changeWithCoupon.isError
        ? { ok: false, text: changeWithCoupon.error.message }
        : changeWithCoupon.isSuccess
          ? { ok: true, text: couponNote(appliedCode, null, changeWithCoupon.data.discount, (m) => formatMoney(m, changeWithCoupon.data.currency)) }
          : { ok: true, text: '正在校验优惠码…' }
      : coupon.isError
        ? { ok: false, text: coupon.error.message }
        : coupon.isSuccess
          ? { ok: true, text: couponNote(appliedCode, coupon.data.coupon, coupon.data.discount, (m) => formatMoney(m, coupon.data.currency)) }
          : { ok: true, text: '正在校验优惠码…' }
  const couponValid = appliedCode !== null && (target.kind === 'change' ? changeWithCoupon.isSuccess : coupon.isSuccess)

  const subtotal = pack ? pack.unit_amount : (price?.unit_amount ?? 0)
  const currency = pack ? pack.currency : (price?.currency ?? 'CNY')
  const quote = buildQuote({
    subtotal,
    currency,
    discount: couponValid && target.kind !== 'change' ? coupon.data!.discount : 0,
    change: target.kind === 'change' ? (changeData ?? null) : null,
    balance: balance.data?.balance ?? 0,
    useBalance: useBalanceOn,
  })
  const methodList = methods.data ?? []
  const method = methodList.find((m) => methodKey(m) === methodChoice) ?? methodList[0] ?? null
  const needsMethod = quote.payable > 0

  const create = useMutation({
    mutationFn: ({ path, body, key }: { path: string; body: Record<string, unknown>; key: string }) => api.post(path, orderCreatedSchema, { body, idempotencyKey: key }),
    onError: (e) => {
      if (endsIntent(e)) intentKey.reset()
    },
  })

  const changeBlocked = target.kind === 'change' && (change.isPending || change.isError)
  const renewBlocked = mode.kind === 'renew' && (mode.plan === null || mode.plan.allow_renewal === false || mode.sub.renewable === false)
  const canSubmit = (pack !== null || price !== null) && !changeBlocked && !renewBlocked && (!needsMethod || method !== null) && !create.isPending

  function submit() {
    const request = orderRequest(target, pack ? null : priceId, quote.balanceApplied, couponValid ? appliedCode : null)
    // 刚下过同样的单、还没过期：关掉支付弹窗后再点，重开这张单的支付，不下第二张
    const again = placed.recall(request)
    if (again && method) return setPayState({ ...again, method })
    // 一次用户意图一个幂等键：双击、断网或 5xx 重试复用；成功或 4xx 拒绝后丢弃
    create.mutate(
      { ...request, key: intentKey(request) },
      {
        onSuccess: (order) => {
          intentKey.reset()
          void client.invalidateQueries({ queryKey: ['portal', 'orders'] })
          if (order.status === 'fulfilled') return setPayState({ phase: 'done', orderId: order.order_id })
          if (!method) return
          const pay = { phase: 'redirect', orderId: order.order_id, orderNo: order.order_no, amount: order.payable_amount, currency: order.currency, method } as const
          placed.remember(request, pay)
          setPayState(pay)
        },
      },
    )
  }

  function applyCoupon() {
    const code = normalizeCoupon(couponInput)
    setAppliedCode(code || null)
  }

  if (renewBlocked) return <RenewBlocked mode={mode as Extract<CheckoutMode, { kind: 'renew' }>} />

  return (
    <div className={css.grid}>
      <Card className={css.form}>
        <div className={css.formTitle}>
          <span>{formTitle(target)}</span>
          {target.kind === 'change' && <span className={css.formSub}>从{target.sub.plan_name}变更，订阅地址不变</span>}
        </div>

        {pack ? (
          <OptionList label="选择容量">
            {(packs.data ?? [pack]).map((p) => (
              <Option key={p.id} selected={p.id === pack.id} onSelect={() => setPackId(p.id)} title={compactBytes(p.traffic_bytes)} note={perGbNote(p)} price={formatMoney(p.unit_amount, p.currency)} />
            ))}
          </OptionList>
        ) : options.length === 0 ? (
          <Empty bare title="这个套餐暂时没有可购买的周期" description="稍后再来看看，或选购其他套餐。" />
        ) : (
          <OptionList label="选择周期">
            {options.map((p) => {
              const save = target.kind !== 'pack' && target.plan ? savingAmount(target.plan, p) : 0
              return (
                <Option
                  key={p.id}
                  selected={p.id === priceId}
                  onSelect={() => setPriceId(p.id)}
                  title={periodName(periodOf(p))}
                  note={save > 0 ? `省 ${formatMoney(save, p.currency)}` : ''}
                  noteTone="ok"
                  price={formatMoney(p.unit_amount, p.currency)}
                />
              )
            })}
          </OptionList>
        )}

        <div className={css.field}>
          <div className={css.label}>优惠码</div>
          <div className={css.couponRow}>
            <Input
              mono
              aria-label="优惠码"
              placeholder="例如 AUTUMN26"
              value={couponInput}
              onChange={(e) => setCouponInput(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && applyCoupon()}
              fieldClassName={css.couponInput}
              className={css.upper}
            />
            {appliedCode ? (
              <Button
                onClick={() => {
                  setAppliedCode(null)
                  setCouponInput('')
                }}
              >
                移除
              </Button>
            ) : (
              <Button onClick={applyCoupon} disabled={!normalizeCoupon(couponInput)}>
                使用
              </Button>
            )}
          </div>
          {couponState && (
            <div className={couponState.ok ? css.couponOk : css.couponBad} role={couponState.ok ? 'status' : 'alert'}>
              {couponState.text}
            </div>
          )}
        </div>

        <div className={css.field}>
          <Switch
            checked={useBalanceOn}
            disabled={!balance.data || balance.data.balance <= 0}
            onChange={(e) => setUseBalance(e.target.checked)}
            label={
              <span>
                使用余额抵扣
                <span className={css.muted}>（可用 {balance.data ? formatMoney(balance.data.balance, balance.data.currency) : '—'}）</span>
              </span>
            }
          />
        </div>

        {needsMethod && (
          <div className={css.field}>
            <div className={css.label}>支付方式</div>
            {methods.isPending ? (
              <Skeleton height={40} radius="var(--radius-md)" />
            ) : methods.isError ? (
              <LoadError error={methods.error} onRetry={() => void methods.refetch()} what="支付方式" />
            ) : methodList.length === 0 ? (
              <div className={css.couponBad}>暂无可用的支付方式，可以用余额抵扣全额，或稍后再试。</div>
            ) : (
              <div className={css.methods} role="radiogroup" aria-label="支付方式">
                {methodList.map((m) => (
                  <button
                    key={methodKey(m)}
                    type="button"
                    role="radio"
                    aria-checked={method !== null && methodKey(m) === methodKey(method)}
                    className={css.method}
                    onClick={() => setMethodChoice(methodKey(m))}
                  >
                    {m.label}
                  </button>
                ))}
              </div>
            )}
          </div>
        )}
      </Card>

      <Card className={css.preview}>
        <div className={css.formTitle}>订单预览</div>
        <PreviewLines target={target} priceLabel={pack ? `流量包 ${compactBytes(pack.traffic_bytes)}` : price ? `${target.kind !== 'pack' ? target.plan?.name ?? '' : ''} · ${periodName(periodOf(price))}` : ''} quote={quote} change={changeData} changeError={target.kind === 'change' && change.isError ? change.error : null} couponCode={couponValid ? appliedCode : null} />
        <div className={css.divider} />
        <div className={css.total}>
          <span>应付</span>
          <span className={css.totalValue}>{target.kind === 'change' && change.isPending ? <Skeleton width={90} height={28} /> : changeBlocked ? '—' : formatMoney(quote.payable, quote.currency)}</span>
        </div>
        {create.isError && <SubmitError error={create.error} />}
        <Button variant="primary" block busy={create.isPending} disabled={!canSubmit} onClick={submit}>
          {needsMethod ? '提交订单并支付' : '确认支付'}
        </Button>
        <a className={css.back} href={href('/plans', pack ? { tab: 'packs' } : undefined)}>
          返回
        </a>
      </Card>

      <PaymentModal state={payState} onClose={() => setPayState(null)} />
    </div>
  )
}

function formTitle(target: CheckoutMode): string {
  switch (target.kind) {
    case 'pack':
      return '流量包 · 选择容量'
    case 'renew':
      return `${target.sub.plan_name} · 续费 · 选择周期`
    default:
      return `${target.plan.name} · 选择周期`
  }
}

// ---------------------------------------------------------------------------
// 选项：单选组，设计稿的圆点 + 名称 + 省额 / 单价 + 价格
// ---------------------------------------------------------------------------
function OptionList({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className={css.options} role="radiogroup" aria-label={label}>
      {children}
    </div>
  )
}

function Option({ selected, onSelect, title, note, noteTone, price }: { selected: boolean; onSelect: () => void; title: string; note: string; noteTone?: 'ok'; price: string }) {
  return (
    <button type="button" role="radio" aria-checked={selected} className={css.option} onClick={onSelect}>
      <span className={css.dot} aria-hidden="true" />
      <span className={css.optionTitle}>{title}</span>
      <span className={noteTone === 'ok' ? css.optionSave : css.optionNote}>{note}</span>
      <span className={css.optionPrice}>{price}</span>
    </button>
  )
}

// ---------------------------------------------------------------------------
// 订单预览行
// ---------------------------------------------------------------------------
function PreviewLines({
  target,
  priceLabel,
  quote,
  change,
  changeError,
  couponCode,
}: {
  target: CheckoutMode
  priceLabel: string
  quote: ReturnType<typeof buildQuote>
  change: ChangePreview | undefined
  changeError: Error | null
  couponCode: string | null
}) {
  const money = (m: number) => formatMoney(m, quote.currency)
  if (changeError) return <SubmitError error={changeError} />
  return (
    <dl className={css.lines}>
      <Line k={priceLabel} v={money(quote.subtotal)} />
      {target.kind === 'change' && <Line k="剩余天数折算" v={change ? `−${money(quote.credit)}` : '计算中…'} tone="muted" />}
      {quote.discount > 0 && <Line k={`优惠码 ${couponCode ?? ''}`} v={`−${money(quote.discount)}`} tone="ok" />}
      {quote.balanceApplied > 0 && <Line k="余额抵扣" v={`−${money(quote.balanceApplied)}`} tone="ok" />}
      {quote.refund > 0 && <Line k="差额退回余额" v={`+${money(quote.refund)}`} tone="ok" />}
      {target.kind === 'change' && change && <Line k="新周期" v={`今天起至 ${formatDate(change.new_period_end)}`} tone="muted" />}
      {target.kind === 'change' && change?.direction === 'downgrade' && <Line k="降级说明" v="差额退回余额，余额不可提现" tone="muted" />}
      {target.kind === 'renew' && <Line k="剩余天数折算" v="续期叠加" tone="muted" />}
      {isRepriced(target) && <Line k="价格调整" v="原价格已调整，按当前价格计费" tone="muted" />}
      {target.kind === 'new' && <Line k="生效" v="支付后立即开通" tone="muted" />}
      {target.kind === 'pack' && (
        <>
          <Line k="有效期" v="立即生效，用完为止" tone="muted" />
          <Line k="使用顺序" v="订阅流量用完后自动使用" tone="muted" />
        </>
      )}
    </dl>
  )
}

function Line({ k, v, tone }: { k: string; v: string; tone?: 'ok' | 'muted' }) {
  return (
    <div className={tone === 'ok' ? css.lineOk : tone === 'muted' ? css.lineMuted : css.line}>
      <dt>{k}</dt>
      <dd>{v}</dd>
    </div>
  )
}

function SubmitError({ error }: { error: Error }) {
  // 续费 / 变更互斥（修订 R37）：引导去订单页处理在途单
  const pendingOrder = isApiError(error, 'conflict') && error.message.includes('未完成')
  return (
    <div className={css.submitError} role="alert">
      {error.message || '下单失败，请稍后重试。'}
      {pendingOrder && (
        <a href={href('/orders')} className={css.inline}>
          去订单页处理 →
        </a>
      )}
    </div>
  )
}

function RenewBlocked({ mode }: { mode: Extract<CheckoutMode, { kind: 'renew' }> }) {
  const [title, description] =
    mode.plan === null
      ? ['该套餐已停售', '请选购其他套餐；当前订阅到期前仍可正常使用。']
      : mode.plan.allow_renewal === false
        ? ['该套餐当前不允许续费', '可以选购其他套餐，剩余天数会自动折算。']
        : ['这条订阅当前不能续费', '订阅状态不允许续费，可以选购其他套餐。']
  return (
    <Empty
      title={title}
      description={description}
      action={
        <a className={css.linkButton} href={href('/plans')}>
          去选购
        </a>
      }
    />
  )
}

