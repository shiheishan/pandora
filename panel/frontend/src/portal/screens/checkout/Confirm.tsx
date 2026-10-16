import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState, type ReactNode } from 'react'
import { href, navigate, useHashLocation } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Empty, Input, Skeleton, Switch, useToast } from '../../../ui'
import { usePageHead } from '../../head'
import { LoadError } from '../common/Blocks'
import { methodKey, usePaymentMethods, type Plan } from '../common/catalog'
import { feeLine } from '../common/fee'
import { Alt, Callout, flowCss, Notes, Rows } from '../common/Flow'
import type { Holdings } from '../common/holdings'
import { endsIntent, recallPayable, useIntentKey } from '../common/intent'
import { orderCreatedSchema, useCancelOrder, useOrderPayable } from '../common/orders'
import { daysLeft, money, nameIdeas, nameRequired, NEW_COPY_IDEAS, periodLabel, profileName } from '../common/purchase'
import { balanceSplit, purchaseRefusal, refusalOf, useQuote, type Quote, type QuoteRow, type RefusalInfo } from '../common/quote'
import { isLive, type Subscription } from '../common/subscriptions'
import css from './Checkout.module.css'
import { confirmCopy, payCopy, tierNote, type Note } from './copy'
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
  // 用过的优惠码记在地址里（?coupon=）：去钱包充值再回来，码还在（首次点击测试）
  const { query } = useHashLocation()
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
    <div className={`${flowCss.stack} ${flowCss.narrow}`} data-screen={`confirm-${target.kind}`}>
      {target.kind === 'new' && plan && <SamePlanTip plan={plan} h={h} />}
      {quote.isPending ? (
        <Skeleton height={260} />
      ) : quote.isError && purchaseRefusal(quote.error) === 'order_pending' ? (
        // 同款已有一张没付的新购单：报价阶段就拦（建单也拦），这里直接给「取消它 / 去付款」，不当加载失败
        <Refusal refusal={refusalOf(quote.error, 'order_pending')} onCleared={() => void quote.refetch()} />
      ) : quote.isError ? (
        <LoadError error={quote.error} onRetry={() => void quote.refetch()} what="价格" />
      ) : quote.data.quotes.length === 0 ? (
        <Empty bare title="暂时没有可买的时长" description="稍后再来看看，或者选别的套餐。" action={<a href={href('/plans')}>去选购</a>} />
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
          coupon={
            couponAllowed ? (
              <CouponBox
                open={couponOpen}
                onOpen={() => setCouponOpen(true)}
                input={couponInput}
                onInput={setCouponInput}
                applied={coupon}
                onApply={(code) => setCoupon(code || null)}
                rows={quote.data.quotes}
              />
            ) : null
          }
          couponCode={couponAllowed ? coupon : null}
        />
      )}
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
    <div className={flowCss.tip}>
      你已经有一份{plan.name}（{h.naming.multi ? `「${h.naming.sn(holder)}」，` : ''}
      {state}）。
      <div>
        <a className={flowCss.textButton} href={href('/checkout', { renew: holder.id })} id="btn-switch-renew">
          只想延长它？改成续费 →
        </a>
      </div>
    </div>
  )
}

function CouponBox({ open, onOpen, input, onInput, applied, onApply, rows }: { open: boolean; onOpen: () => void; input: string; onInput: (v: string) => void; applied: string | null; onApply: (code: string) => void; rows: readonly QuoteRow[] }) {
  if (!open && !applied) {
    return (
      <button type="button" className={flowCss.textButton} onClick={onOpen}>
        有优惠码？
      </button>
    )
  }
  const row = rows[0]
  return (
    <div className={css.field}>
      <span className={css.fieldLabel}>优惠码</span>
      <div className={css.couponRow}>
        <Input mono aria-label="优惠码" placeholder="例如 AUTUMN26" value={input} onChange={(e) => onInput(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && onApply(input.trim().toUpperCase())} fieldClassName={css.grow} className={css.upper} />
        {applied ? (
          <Button
            onClick={() => {
              onInput('')
              onApply('')
            }}
          >
            不用了
          </Button>
        ) : (
          <Button onClick={() => onApply(input.trim().toUpperCase())} disabled={!input.trim()}>
            使用
          </Button>
        )}
      </div>
      {applied && row && (row.coupon_error ? <p className={css.bad}>{row.coupon_error}</p> : row.discount > 0 ? <p className={css.good}>已用优惠码 {applied}，省 {money(row.discount)}</p> : null)}
    </div>
  )
}

function NoteLine({ note }: { note: Note }) {
  return typeof note === 'string' ? (
    <>{note}</>
  ) : (
    <>
      <b>{note.b}</b>
      {note.rest}
    </>
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
  coupon,
  couponCode,
}: {
  target: Target
  h: Holdings
  quote: Quote
  sub: Subscription | undefined
  plan: Plan | undefined
  oldPlan: Plan | undefined
  requested: string | null
  refetch: () => Promise<unknown>
  coupon: ReactNode
  couponCode: string | null
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
  const method = methodList.find((m) => methodKey(m) === methodChoice) ?? methodList[0] ?? null
  const pay = payCopy(quote, row, split, useBalance, copy.verb, methodList.map((m) => m.label).join('、'))
  const blocked = pay.tooSmall ? pay.button : required && !name.trim() ? '先给它起个名字' : pay.needsMethod && !method ? '暂时没有能用的付款方式' : null
  const periods = target.kind !== 'reset' && tiers.length > 1

  const create = useMutation({
    mutationFn: ({ path, body, key }: { path: string; body: Record<string, unknown>; key: string }) => api.post(path, orderCreatedSchema, { body, idempotencyKey: key }),
  })

  async function submit() {
    if (blocked || target.kind === 'choose') return
    setRefusal(null)
    const request = orderRequest({ target, quote, row, split, coupon: couponCode, label: target.kind === 'new' ? name : undefined, newCopy: target.kind === 'new' && h.held.length > 0 })
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

  return (
    <>
      <Callout id="what-happens">
        {copy.callout.map((line, i) => (
          <p key={i} className={css.line}>
            {line}
          </p>
        ))}
      </Callout>
      {periods && (
        <div className={css.periods} role="radiogroup" aria-label="买多久">
          {tiers.map((r) => {
            const [a, b] = tierNote(target, r)
            return (
              <button key={r.price_id} type="button" role="radio" aria-checked={r.price_id === row.price_id} className={css.period} onClick={() => setPriceId(r.price_id)} id={`period-${r.price_id}`}>
                <b>{periodLabel(r.interval, r.interval_count)}</b>
                <span>{money(r.subtotal)}</span>
                <small>
                  {a}
                  {b && (
                    <>
                      <br />
                      {b}
                    </>
                  )}
                </small>
              </button>
            )
          })}
        </div>
      )}
      {copy.formula && (
        <p className={css.formula} id="change-formula">
          {copy.formula}
        </p>
      )}
      {target.kind === 'new' && h.held.length > 0 && plan && <NameField required={required} name={name} onName={setName} plan={plan} h={h} />}
      <Rows
        rows={copy.rows.map((r) => ({
          k: r.k,
          v: r.v,
          ok: r.ok,
          extra: r.calc ? (
            <details className={flowCss.calc}>
              <summary>怎么算的</summary>
              <p>{r.calc}</p>
            </details>
          ) : undefined,
        }))}
      />
      <Notes items={copy.notes.map((n, i) => <NoteLine key={i} note={n} />)} />
      {coupon}
      <div className={css.payBlock}>
        {pay.balanceLine && (
          <div className={pay.balanceLine.on ? css.balanceOn : css.balance} id="balance-row">
            <span>
              {pay.balanceLine.strong && <b>{pay.balanceLine.strong}</b>}
              {pay.balanceLine.text}
            </span>
            <Switch aria-label="用余额" checked={pay.balanceLine.on} disabled={pay.balanceLine.locked} onChange={(e) => setUseBalance(e.target.checked)} id="switch-balance" />
          </div>
        )}
        <p className={css.sum} id="pay-summary">
          {pay.sum.text}
          {pay.sum.strong && <b>{pay.sum.strong}</b>}
          {pay.sum.after}
          {updated && <span className={css.updated}>已更新</span>}
        </p>
        {updated && <p className={css.sumNote}>金额刚变了，已按最新的算好，再点一次就行。</p>}
        {pay.keptNote && (
          <p className={css.sumNote} id="min-pay-note">
            {pay.keptNote}
          </p>
        )}
        {pay.needsMethod &&
          (methods.isPending ? (
            <Skeleton height={40} radius="var(--radius-md)" />
          ) : methods.isError ? (
            <LoadError error={methods.error} onRetry={() => void methods.refetch()} what="付款方式" />
          ) : methodList.length === 0 ? (
            <p className={css.bad}>暂时没有能用的付款方式，可以打开余额付，或稍后再试。</p>
          ) : (
            <div className={css.methods} role="radiogroup" aria-label="付款方式">
              {methodList.map((m) => {
                const line = feeLine(m)
                return (
                  <button key={methodKey(m)} type="button" role="radio" aria-checked={method !== null && methodKey(m) === methodKey(method)} className={css.method} onClick={() => setMethodChoice(methodKey(m))}>
                    {m.label}
                    {line && (
                      <>
                        <br />
                        <small className={css.sumNote}>{line}</small>
                      </>
                    )}
                  </button>
                )
              })}
            </div>
          ))}
        {pay.needsMethod && method && method.fee !== undefined && method.fee > 0 && method.total !== undefined && (
          <p className={css.sumNote}>
            选{method.label}另付手续费 {money(method.fee)}，合计 {money(method.total)}；用余额付不收手续费。
          </p>
        )}
        {refusal && <Refusal refusal={refusal} onCleared={() => setRefusal(null)} />}
        <Button variant="primary" block busy={busy} disabled={blocked !== null} onClick={() => void submit()} id="btn-submit">
          {blocked ?? pay.button}
        </Button>
        {pay.tooSmall && (
          <a className={flowCss.textButton} href={href('/wallet')}>
            去钱包充值（充 ¥1 就够），充完回来再点 →
          </a>
        )}
        {copy.change && <p className={flowCss.canChange}>↺ {copy.change}</p>}
      </div>
      {copy.alt && <Alt href={copy.alt.href}>{copy.alt.text}</Alt>}
    </>
  )
}

/** 另买一份时起名：同款时预填一个没被用过的建议名；会重名时必填，清空则按钮置灰并写明后果 */
function NameField({ required, name, onName, plan, h }: { required: boolean; name: string; onName: (v: string) => void; plan: Plan; h: Holdings }) {
  return (
    <div className={css.field}>
      <label className={css.fieldLabel} htmlFor="name-input-new">
        给它起个名字 <small>{required ? '必填' : '选填，方便分清是谁的'}</small>
      </label>
      <Input id="name-input-new" maxLength={16} value={name} placeholder="例如：妈妈的 iPad" onChange={(e) => onName(e.target.value)} />
      {/* 只在空着时说重名的后果（首次点击测试：已经填了名字还挂着提醒，像没生效） */}
      {required && !name.trim() && <p className={flowCss.warnBox}>不起名的话，App 里会有两个「{profileName(h.site, '', plan.name)}」，分不清哪个是哪个。</p>}
      <div className={flowCss.chips}>
        {nameIdeas(h.held, null, NEW_COPY_IDEAS)
          .slice(0, 3)
          .map((v) => (
            <button key={v} type="button" className={flowCss.chip} onClick={() => onName(v)}>
              {v}
            </button>
          ))}
      </div>
      <p className={flowCss.faint}>App 里会显示成「{profileName(h.site, name, plan.name)}」。名字以后随时能改。</p>
    </div>
  )
}

/**
 * 报价或建单被拒：同一套餐有一张还没付款的新购单，或同一份有一张没付的续费、叠加、换套餐单（order_pending，fields.order_id）时给「去付款 / 取消它」；
 * 那张已超过付款期限（fields.lapsed）时只给「取消它」。取消后重新报价，可以直接再点一次。
 */
function Refusal({ refusal, onCleared }: { refusal: RefusalInfo; onCleared: () => void }) {
  const toast = useToast()
  const cancel = useCancelOrder()
  const lapsed = refusal.lapsed
  return (
    <div className={flowCss.errorBox} role="alert">
      {refusal.text}
      {refusal.kind === 'order_pending' && refusal.orderId && (
        <div className={css.refusalActions}>
          <Button
            size="sm"
            busy={cancel.isPending}
            onClick={() =>
              // 用 mutateAsync 而不是 mutate 的回调：取消成功会让报价重新拉取，报价阶段的这个提示框随之卸载，
              // 卸载后 mutate 的回调不再触发（提示「已取消」会时有时无）；promise 照样兑现
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
          </Button>
          {!lapsed && (
            <a className={flowCss.mini} href={href(`/orders/${refusal.orderId}`)}>
              去付款
            </a>
          )}
        </div>
      )}
    </div>
  )
}
