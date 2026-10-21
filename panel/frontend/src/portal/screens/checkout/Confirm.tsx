import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState, type KeyboardEvent } from 'react'
import { href, navigate, useHashLocation } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Empty, Input, Skeleton } from '../../../ui'
import { usePageHead } from '../../head'
import { Btn, Icon, Sticker } from '../../kit'
import { LoadError } from '../common/Blocks'
import { methodKey, usableMethods, usePaymentMethods, type Plan } from '../common/catalog'
import { Callout } from '../common/Flow'
import type { Holdings } from '../common/holdings'
import { endsIntent, recallPayable, useIntentKey } from '../common/intent'
import { orderCreatedSchema, useOrderPayable } from '../common/orders'
import { daysLeft, money, nameIdeas, nameRequired, NEW_COPY_IDEAS, periodLabel, profileName } from '../common/purchase'
import { pickMethod } from '../common/PayFlow'
import { balanceSplit, isSubscriptionLimit, purchaseRefusal, refusalOf, useQuote, type Quote, type RefusalInfo } from '../common/quote'
import { isLive, type Subscription } from '../common/subscriptions'
import { Bill, CouponBox, NoteLine, Refusal } from './Bill'
import css from './Checkout.module.css'
import { confirmCopy, payCopy, tierNote, type ConfirmItem, type Note } from './copy'
import { doneQuery } from './result-copy'
import { defaultTier, intentOf, orderRequest, placedOrders, quoteRequest, sortTiers, type Target } from './model'

function titleOf(t: Target, h: Holdings, plans: readonly Plan[]): string {
  const sub = 'subId' in t && t.subId ? h.held.find((s) => s.id === t.subId) : undefined
  switch (t.kind) {
    case 'renew':
      return sub && !isLive(sub) ? '恢复使用' : '续费'
    case 'change':
      return `换成${plans.find((p) => p.id === t.planId)?.name ?? '别的套餐'}`
    case 'new':
      return h.held.length ? '再买一份' : `买${plans.find((p) => p.id === t.planId)?.name ?? '套餐'}`
    case 'stack':
      return '叠加'
    case 'reset':
      return '重置流量'
    default:
      return '确认'
  }
}

/**
 * 确认页（原型 scrConfirm）：会发生什么 → 买多久（换套餐每档写算式）→ 明细 → 提示 → 余额（默认打开）
 * → 付款方式 → 主按钮 → 退路。金额全取报价接口；建单带 as_of + expect，金额变了（409 quote_changed）
 * 就重新报价、在金额旁标「已更新」，让用户再点一次。
 */
export function ConfirmForm({ target, h, plans, requested }: { target: Target; h: Holdings; plans: readonly Plan[]; requested: string | null }) {
  const { query } = useHashLocation()
  // 用过的优惠码记在地址里（?coupon=）：去钱包充值再回来，码还在（首次点击测试）
  const initial = query.get('coupon')
  const [couponInput, setCouponInput] = useState(initial ?? '')
  const [coupon, setCouponState] = useState<string | null>(initial)
  const [couponOpen, setCouponOpen] = useState(initial !== null)
  const setCoupon = (code: string | null) => {
    setCouponState(code)
    navigate('/checkout', { query: { ...Object.fromEntries(query), coupon: code }, replace: true })
  }
  // 流量重置永远按原价，不收券，用户 10-14 定
  const couponAllowed = target.kind !== 'reset'
  const quote = useQuote(quoteRequest(target, h.held, couponAllowed ? coupon : null))
  usePageHead(titleOf(target, h, plans), target.kind === 'new' ? '/plans' : '/subs')

  const sub = 'subId' in target && target.subId ? h.held.find((s) => s.id === target.subId) : undefined
  const plan = target.kind === 'change' || target.kind === 'new' ? plans.find((p) => p.id === target.planId) : sub ? plans.find((p) => p.id === sub.plan_id) : undefined

  return (
    <div className={css.page} data-screen={`confirm-${target.kind}`}>
      {quote.isPending ? (
        <div className={css.ck}>
          <Skeleton height={320} />
          <Skeleton height={320} />
        </div>
      ) : quote.isError && purchaseRefusal(quote.error) === 'order_pending' ? (
        // 同款已有一张没付的新购单：报价阶段就拦（建单也拦），这里直接给「取消它 / 去付款」，不当加载失败
        <Refusal refusal={refusalOf(quote.error, 'order_pending')} onCleared={() => void quote.refetch()} />
      ) : quote.isError && isSubscriptionLimit(quote.error) ? (
        <Empty
          bare
          title="不能再开一份了"
          description={quote.error.message}
          action={<Btn href={href('/subs')} tone="soft">去我的套餐</Btn>}
        />
      ) : quote.isError ? (
        <LoadError error={quote.error} onRetry={() => void quote.refetch()} what="价格" />
      ) : quote.data.quotes.length === 0 ? (
        <Empty bare title="暂时没有可买的时长" description="稍后再来看看，或者选别的套餐。" action={<Btn href={href('/plans')} tone="soft">去选购</Btn>} />
      ) : (
        <Priced
          key={coupon ?? ''}
          target={target}
          h={h}
          quote={quote.data}
          sub={sub}
          plan={plan}
          oldPlan={sub ? plans.find((p) => p.id === sub.plan_id) : undefined}
          requested={requested}
          refetch={() => quote.refetch()}
          couponAllowed={couponAllowed}
          couponOpen={couponOpen}
          onCouponOpen={() => setCouponOpen(true)}
          couponInput={couponInput}
          onCouponInput={setCouponInput}
          coupon={couponAllowed ? coupon : null}
          onCouponApply={(code) => setCoupon(code || null)}
        />
      )}
    </div>
  )
}

function isWarn(n: Note) {
  return typeof n === 'string' && n.startsWith('你的流量用得比天数快')
}

function Item({ item }: { item: ConfirmItem }) {
  return (
    <div className={css.item}>
      <Sticker size="md">{item.name}</Sticker>
      <small className={css.facts}>{item.facts}</small>
      {item.pill && <span className={css.pill2}>{item.pill}</span>}
    </div>
  )
}

function Swap({ swap }: { swap: { from: ConfirmItem; to: ConfirmItem } }) {
  return (
    <div className={css.swap}>
      <div>
        <span className={css.k}>现在</span>
        <b>{swap.from.name}</b>
        <small>{swap.from.facts}</small>
      </div>
      <Icon name="chev" size={18} />
      <div className={css.to}>
        <span className={css.k}>换成</span>
        <b>{swap.to.name}</b>
        <small>{swap.to.facts}</small>
      </div>
    </div>
  )
}

/** 另买同款：上方提示「只想延长它？改成续费 →」，和底部主按钮拉开距离 */
function SamePlanTip({ plan, h }: { plan: Plan; h: Holdings }) {
  const holder = h.held.find((s) => s.plan_id === plan.id && s.renew_until !== null)
  if (!holder) return null
  const left = daysLeft(holder.current_period_end)
  const state = left === null ? '长期有效' : left <= 0 ? '已过期' : `还剩 ${left} 天`
  return (
    <div className={css.tip}>
      你已经有一份{plan.name}（{h.naming.multi ? `「${h.naming.sn(holder)}」，` : ''}
      {state}）。
      <div>
        <a href={href('/checkout', { renew: holder.id })} id="btn-switch-renew">
          只想延长它？改成续费 →
        </a>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 有报价之后的整块
// ---------------------------------------------------------------------------

function Priced({
  target,
  h,
  quote,
  sub,
  plan,
  oldPlan,
  requested,
  refetch,
  couponAllowed,
  couponOpen,
  onCouponOpen,
  couponInput,
  onCouponInput,
  coupon,
  onCouponApply,
}: {
  target: Target
  h: Holdings
  quote: Quote
  sub: Subscription | undefined
  plan: Plan | undefined
  oldPlan: Plan | undefined
  requested: string | null
  refetch: () => Promise<unknown>
  couponAllowed: boolean
  couponOpen: boolean
  onCouponOpen: () => void
  couponInput: string
  onCouponInput: (v: string) => void
  coupon: string | null
  onCouponApply: (code: string) => void
}) {
  const api = useApi()
  const client = useQueryClient()
  const payable = useOrderPayable()
  const intentKey = useIntentKey()
  const [priceId, setPriceId] = useState<string | null>(() => defaultTier(quote.quotes, sub, requested)?.price_id ?? null)
  const [useBalance, setUseBalance] = useState(true)
  const [methodChoice, setMethodChoice] = useState<string | null>(null)
  const required = target.kind === 'new' && nameRequired(h.held, target.planId)
  const [name, setName] = useState(() => (target.kind === 'new' && h.held.some((s) => s.plan_id === target.planId) ? (nameIdeas(h.held, null, NEW_COPY_IDEAS)[0] ?? '') : ''))
  const [updated, setUpdated] = useState(false)
  const [refusal, setRefusal] = useState<RefusalInfo | null>(null)
  const [busy, setBusy] = useState(false)

  const tiers = sortTiers(quote.quotes)
  const row = tiers.find((r) => r.price_id === priceId) ?? defaultTier(tiers, sub, requested) ?? tiers[0]!
  const split = balanceSplit(row, useBalance)
  const methods = usePaymentMethods(split.payable > 0 ? split.payable : undefined)
  const copy = confirmCopy({ target, sub, plan, oldPlan, row, naming: h.naming, held: h.held, now: new Date() })
  const methodList = methods.data ?? []
  const method = pickMethod(methodList, methodChoice ?? '', split.payable)
  const pay = payCopy(quote, row, split, useBalance, copy.verb, usableMethods(methodList, split.payable).map((m) => m.label).join('、'))
  const blocked = pay.tooSmall ? pay.button : required && !name.trim() ? '先给它起个名字' : pay.needsMethod && !method ? '暂时没有能用的付款方式' : null
  const periods = target.kind !== 'reset' && tiers.length > 1

  const create = useMutation({
    mutationFn: ({ path, body, key }: { path: string; body: Record<string, unknown>; key: string }) => api.post(path, orderCreatedSchema, { body, idempotencyKey: key }),
  })

  async function submit() {
    if (blocked || target.kind === 'choose') return
    setRefusal(null)
    const request = orderRequest({ target, quote, row, split, coupon, label: target.kind === 'new' ? name : undefined, newCopy: target.kind === 'new' && h.held.length > 0 })
    const intent = intentOf(request)
    const ctx = doneQuery({
      kind: target.kind,
      subId: 'subId' in target ? target.subId : null,
      was: sub?.current_period_end ?? null,
      oldPlan: target.kind === 'change' ? (sub?.plan_name ?? null) : null,
      refund: row.refund,
      waived: split.waived,
      revive: (target.kind === 'renew' || target.kind === 'change') && sub !== undefined && !isLive(sub),
      stackBytes: target.kind === 'stack' ? (plan?.traffic_bytes ?? 0) : 0,
    })
    const toPay = (orderId: string) => navigate(`/checkout/pay/${orderId}`, { query: { ...ctx, m: method ? methodKey(method) : '' } })
    setBusy(true)
    try {
      // 刚下过同样的单、还能付（付款页「换个付款方式」退回来）：重开它，不下第二张
      const again = await recallPayable(placedOrders, intent, (p) => payable(p.orderId))
      if (again) return toPay(again.orderId)
      const order = await create.mutateAsync({ ...request, key: intentKey.keyFor(request) })
      intentKey.reset()
      void client.invalidateQueries({ queryKey: ['portal'] })
      if (order.status === 'fulfilled') return navigate(`/checkout/done/${order.order_id}`, { query: ctx })
      placedOrders.remember(intent, { orderId: order.order_id })
      toPay(order.order_id)
    } catch (e) {
      if (endsIntent(e)) intentKey.reset()
      const kind = purchaseRefusal(e)
      if (kind === 'quote_changed') {
        setUpdated(true)
        await refetch()
      } else {
        setRefusal(refusalOf(e, kind))
      }
    } finally {
      setBusy(false)
    }
  }

  const onTierKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (tiers.length === 0) return
    const vertical = e.key === 'ArrowDown' || e.key === 'ArrowUp'
    const horizontal = e.key === 'ArrowRight' || e.key === 'ArrowLeft'
    if (!vertical && !horizontal) return
    e.preventDefault()
    const idx = tiers.findIndex((r) => r.price_id === row.price_id)
    const cur = idx >= 0 ? idx : 0
    const next = e.key === 'ArrowDown' || e.key === 'ArrowRight' ? (cur + 1) % tiers.length : (cur - 1 + tiers.length) % tiers.length
    const r = tiers[next]
    if (!r) return
    setPriceId(r.price_id)
    const buttons = e.currentTarget.querySelectorAll<HTMLButtonElement>('[role="radio"]')
    buttons[next]?.focus()
  }

  const couponNode = couponAllowed ? (
    <CouponBox open={couponOpen} onOpen={onCouponOpen} input={couponInput} onInput={onCouponInput} applied={coupon} onApply={onCouponApply} rows={quote.quotes} />
  ) : null

  return (
    <div className={css.ck}>
      <div className={css.left}>
        {target.kind === 'new' && plan && <SamePlanTip plan={plan} h={h} />}
        {copy.swap ? <Swap swap={copy.swap} /> : copy.item ? <Item item={copy.item} /> : null}
        <Callout id="what-happens">
          {copy.callout.map((line, i) => (
            <p key={i} className={css.line}>
              {line}
            </p>
          ))}
        </Callout>
        {periods && (
          <>
            <h3 className={css.h3}>买多久</h3>
            <div className={css.tiers} role="radiogroup" aria-label="买多久" onKeyDown={onTierKeyDown}>
              {tiers.map((r) => {
                const on = r.price_id === row.price_id
                const [a, b] = tierNote(target, r)
                return (
                  <button
                    key={r.price_id}
                    type="button"
                    role="radio"
                    aria-checked={on}
                    tabIndex={on ? 0 : -1}
                    className={css.tier}
                    onClick={() => setPriceId(r.price_id)}
                    id={`period-${r.price_id}`}
                  >
                    <span className={css.dot} aria-hidden="true" />
                    <b>{periodLabel(r.interval, r.interval_count)}</b>
                    <small>{a}{b && ` ${b}`}</small>
                    <span className={css.tierPrice}>{money(r.subtotal)}</span>
                  </button>
                )
              })}
            </div>
          </>
        )}
        {copy.formula && (
          <p className={css.formula} id="change-formula">
            {copy.formula}
          </p>
        )}
        {target.kind === 'new' && h.held.length > 0 && plan && <NameField required={required} name={name} onName={setName} plan={plan} h={h} />}
        {copy.notes.length > 0 && (
          <ul className={css.notes}>
            {copy.notes.map((n, i) => (
              <li key={i} className={isWarn(n) ? css.warn : undefined}>
                <NoteLine note={n} />
              </li>
            ))}
          </ul>
        )}
        {copy.alt && (
          <p className={css.alt}>
            <a href={copy.alt.href}>{copy.alt.text} ›</a>
          </p>
        )}
      </div>
      <Bill
        copy={copy}
        pay={pay}
        coupon={couponNode}
        onUseBalance={setUseBalance}
        amount={split.payable}
        methods={methods}
        methodChoice={methodChoice}
        onMethodChoice={setMethodChoice}
        refusal={refusal}
        onRefusalCleared={() => setRefusal(null)}
        updated={updated}
        busy={busy}
        blocked={blocked}
        onSubmit={() => void submit()}
      />
    </div>
  )
}

/** 另买一份时起名：同款时预填一个没被用过的建议名；会重名时必填，清空则按钮置灰并写明后果 */
function NameField({ required, name, onName, plan, h }: { required: boolean; name: string; onName: (v: string) => void; plan: Plan; h: Holdings }) {
  return (
    <div className={css.name}>
      <label className={css.nameLabel} htmlFor="name-input-new">
        给它起个名字 <small>{required ? '必填' : '选填，方便分清是谁的'}</small>
      </label>
      <Input id="name-input-new" maxLength={16} value={name} placeholder="例如：妈妈的 iPad" onChange={(e) => onName(e.target.value)} />
      {/* 只在空着时说重名的后果（首次点击测试：已经填了名字还挂着提醒，像没生效） */}
      {required && !name.trim() && <p className={css.tip}>不起名的话，App 里会有两个「{profileName(h.site, '', plan.name)}」，分不清哪个是哪个。</p>}
      <div className={css.ideas}>
        {nameIdeas(h.held, null, NEW_COPY_IDEAS)
          .slice(0, 3)
          .map((v) => (
            <button key={v} type="button" className={css.idea} onClick={() => onName(v)}>
              {v}
            </button>
          ))}
      </div>
      <p className={css.nameHint}>App 里会显示成「{profileName(h.site, name, plan.name)}」。名字以后随时能改。</p>
    </div>
  )
}
