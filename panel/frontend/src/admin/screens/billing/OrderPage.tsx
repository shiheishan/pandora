import { RefreshIcon, Tick02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { formatDateTime, formatMoney } from '../../../core/format'
import { href } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, QueryView, Skeleton, Tag, useToast } from '../../../ui'
import { useFailure, useIntentKey } from '../../actions'
import { Callout, DetailPage, KvList, KvRow, Row, RowList, SectionTitle, SideFact, SideFacts } from '../../kit'
import { useInvalidateUsers } from '../users/api'
import { CancelDialog, MarkPaidDialogs } from './OrderActions'
import { orderQueriedSchema, useInvalidateBilling, useOrder, useOrderPayments, type OrderDetail } from './api'
import css from './Orders.module.css'
import {
  ORDER_STATUS_VIEW,
  canCancel,
  canMarkPaid,
  canQueryChannel,
  orderAmountFacts,
  orderMeta,
  orderSideFacts,
  paymentLines,
  queriedView,
} from './model'

/** 订单详情整页（样稿 .dpage[data-dfor="orders"]）：地址 /billing/orders/<id> */
export function OrderPage({
  id,
  back,
  now,
}: {
  id: string
  back: { href: string; label: string }
  now: Date
}) {
  const q = useOrder(id)

  if (!q.data) {
    return (
      <DetailPage back={back} title="订单">
        <QueryView query={q} rows={6} empty={null}>{() => null}</QueryView>
      </DetailPage>
    )
  }

  return <OrderView key={`${q.data.id}:${q.data.state_version}`} o={q.data} back={back} now={now} />
}

function OrderView({
  o,
  back,
  now,
}: {
  o: OrderDetail
  back: { href: string; label: string }
  now: Date
}) {
  const [cancelling, setCancelling] = useState(false)
  const [marking, setMarking] = useState(false)
  const cancellable = canCancel(o)
  const markable = canMarkPaid(o)

  const actions =
    cancellable || markable
      ? (
          <>
            {cancellable && (
              <Button variant="danger" size="sm" onClick={() => setCancelling(true)}>
                取消订单
              </Button>
            )}
            {markable && (
              <Button variant="primary" size="sm" onClick={() => setMarking(true)}>
                <HugeiconsIcon icon={Tick02Icon} size={16} strokeWidth={1.6} aria-hidden />
                标记已支付
              </Button>
            )}
          </>
        )
      : undefined

  return (
    <>
      <DetailPage
        back={back}
        title={<span className={css.mono}>{o.order_no}</span>}
        meta={
          <>
            <Tag tone={ORDER_STATUS_VIEW[o.status].tone}>{ORDER_STATUS_VIEW[o.status].label}</Tag>
            <span>{orderMeta(o, now)}</span>
          </>
        }
        actions={actions}
        side={
          <>
            <SectionTitle>详情</SectionTitle>
            <SideFacts label="订单详情">
              {orderSideFacts(o, now).map((f) => (
                <SideFact key={f.k} label={f.k}>
                  {f.userLink ? (
                    <a href={href(`/users/list/${encodeURIComponent(o.user_id)}`)}>{f.v}</a>
                  ) : (
                    f.v
                  )}
                  {f.hint != null && <span className={css.muted}> · {f.hint}</span>}
                </SideFact>
              ))}
            </SideFacts>
          </>
        }
      >
        <section>
          <SectionTitle>金额</SectionTitle>
          <KvList boxed>
            {orderAmountFacts(o).map(([k, v]) => (
              <KvRow key={k} label={k}>
                <span className={k === '内容' ? undefined : k === '应付' ? `${css.num} ${css.strong}` : css.num}>{v}</span>
              </KvRow>
            ))}
          </KvList>
        </section>

        {o.items.length > 1 && (
          <section>
            <SectionTitle>订单项</SectionTitle>
            <KvList boxed>
              {o.items.map((i) => (
                <KvRow key={i.id} label={i.plan_name ?? i.product_name}>
                  <span className={css.num}>
                    {formatMoney(i.line_amount)}
                    <span className={css.muted}> × {i.quantity}</span>
                  </span>
                </KvRow>
              ))}
            </KvList>
          </section>
        )}

        <PaymentsSection id={o.id} />

        {canQueryChannel(o) && <QueryChannel o={o} />}
      </DetailPage>
      <CancelDialog o={o} open={cancelling && cancellable} onClose={() => setCancelling(false)} />
      <MarkPaidDialogs o={o} open={marking && markable} onClose={() => setMarking(false)} />
    </>
  )
}

function PaymentsSection({ id }: { id: string }) {
  const q = useOrderPayments(id)
  return (
    <section>
      <SectionTitle>支付记录</SectionTitle>
      {q.isPending ? (
        <Skeleton height={44} />
      ) : q.isError ? (
        <span className={css.muted}>{isApiError(q.error, 'not_found') ? '记录不存在' : `读取失败：${q.error.message}`}</span>
      ) : paymentLines(q.data).length === 0 ? (
        <p className={css.muted}>尚无支付尝试</p>
      ) : (
        <RowList label="支付记录">
          {paymentLines(q.data).map((l) => (
            <Row
              key={l.key}
              title={l.channel}
              meta={<span className={css.mono} title={l.ref}>{l.ref}</span>}
              extra={
                <span className={css[`tone_${l.tone}`]}>
                  {l.label} · {formatDateTime(l.at)}
                </span>
              }
              value={
                <span className={css.num}>
                  {formatMoney(l.amount)}
                  {l.surcharge ? <small className={css.muted}>（含手续费 {formatMoney(l.surcharge)}）</small> : null}
                </span>
              }
            />
          ))}
        </RowList>
      )}
    </section>
  )
}

function QueryChannel({ o }: { o: OrderDetail }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidateBilling()
  const invalidateUsers = useInvalidateUsers()
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<{ tone: string; text: string } | null>(null)

  const run = async () => {
    setBusy(true)
    try {
      const r = await api.post(`v1/orders/${encodeURIComponent(o.id)}/query`, orderQueriedSchema, {
        idempotencyKey: intent.keyFor([o.id, 'query']),
      })
      intent.reset()
      const view = queriedView(r)
      setResult(view)
      if (r.reconciled) {
        toast(view.text)
        void invalidate()
        void invalidateUsers()
      }
    } catch (e) {
      setResult({ tone: 'danger', text: e instanceof Error ? `查单失败：${e.message}` : '查单失败' })
      fail(e, { intent })
    } finally {
      setBusy(false)
    }
  }

  return (
    <section>
      <Callout
        action={
          <Button size="sm" busy={busy} onClick={() => void run()}>
            <HugeiconsIcon icon={RefreshIcon} size={16} strokeWidth={1.6} aria-hidden />
            向渠道查单
          </Button>
        }
      >
        用户说已经付了却没到账时，向 {o.provider_name ?? o.provider_code} 查一次：渠道确认已付款就按回调同一流程自动入账，金额不符会被拒绝。
      </Callout>
      {result != null && (
        <p role="status" className={css[`tone_${result.tone}`]}>
          {result.text}
        </p>
      )}
    </section>
  )
}
