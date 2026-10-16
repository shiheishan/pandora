import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { formatDateTime, formatMoney } from '../../../core/format'
import { href } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Empty, Input, Modal, Skeleton, TextArea, useToast } from '../../../ui'
import { useFailure, useIntentKey } from '../../actions'
import { useInvalidateUsers } from '../users/api'
import { cancelledSchema, markedPaidSchema, orderQueriedSchema, useInvalidateBilling, useOrder, useOrderPayments, type OrderDetail } from './api'
import css from './Billing.module.css'
import { canCancel, canMarkPaid, canQueryChannel, orderFacts, orderSubtitle, paymentLines, queriedView, reasonProblem, referenceProblem } from './model'

export function OrderDialog({ id, onClose }: { id: string | null; onClose: () => void }) {
  const q = useOrder(id)
  const open = id !== null

  if (!open) {
    return <Modal open={false} onClose={onClose} title="订单详情" />
  }

  if (q.isPending) {
    return (
      <Modal open onClose={onClose} size={560} divided title="订单详情">
        <div role="status" aria-label="加载中">
          <Skeleton height={160} />
          <Skeleton height={80} />
        </div>
      </Modal>
    )
  }

  if (q.isError) {
    return (
      <Modal open onClose={onClose} size={560} divided title="订单详情">
        {isApiError(q.error, 'not_found') ? (
          <Empty bare title="订单不存在" description="链接可能有误，或订单已被删除。" />
        ) : (
          <Empty
            bare
            title="订单读取失败"
            description={q.error.message}
            action={
              <Button size="sm" onClick={() => void q.refetch()}>
                重试
              </Button>
            }
          />
        )}
      </Modal>
    )
  }

  return <OrderDetailModal key={`${q.data.id}:${q.data.state_version}`} o={q.data} onClose={onClose} />
}

function OrderDetailModal({ o, onClose }: { o: OrderDetail; onClose: () => void }) {
  const [cancelling, setCancelling] = useState(false)
  const markPaid = useMarkPaidState(o)
  const cancellable = canCancel(o)
  const markable = canMarkPaid(o)

  return (
    <>
      <Modal
        open
        onClose={onClose}
        size={560}
        divided
        title={<span className={css.mono}>{o.order_no}</span>}
        subtitle={orderSubtitle(o)}
        footerStart={
          cancellable ? (
            <Button size="sm" variant="danger" onClick={() => setCancelling(true)}>
              取消订单
            </Button>
          ) : undefined
        }
        actions={
          <>
            <Button size="dialog" onClick={onClose}>
              关闭
            </Button>
            {markable && (
              <Button size="dialog" variant="primary" onClick={markPaid.check}>
                标记已支付
              </Button>
            )}
          </>
        }
      >
        <section className={css.detailSection}>
          <h3 className={css.detailSectionTitle}>订单信息</h3>
          {orderFacts(o).map(([k, v]) => (
            <div key={k} className={css.detailRow}>
              <span className={css.detailLabel}>{k}</span>
              <span className={css.detailValue}>
                {k === '用户' ? (
                  <a className={css.link} href={href(`/users/list/${encodeURIComponent(o.user_id)}`)}>
                    {v}
                  </a>
                ) : (
                  v
                )}
              </span>
            </div>
          ))}
        </section>

        {o.items.length > 1 && (
          <section className={css.detailSection}>
            <h3 className={css.detailSectionTitle}>订单项</h3>
            {o.items.map((i) => (
              <div key={i.id} className={css.detailRow}>
                <span className={css.detailLabel}>{i.plan_name ?? i.product_name}</span>
                <span className={css.detailValue}>
                  {formatMoney(i.line_amount)}
                  <span className={css.small}> × {i.quantity}</span>
                </span>
              </div>
            ))}
          </section>
        )}

        <PaymentsSection id={o.id} />

        {canQueryChannel(o) && <QueryChannel o={o} />}

        {markable && (
          <section className={css.detailSection}>
            <h3 className={css.detailSectionTitle}>手工标记已支付</h3>
            <p className={css.boxText}>
              已通过线下方式收到这笔钱时使用：填凭证号与收款说明，订单立即入账（{formatMoney(o.payable_amount)}）。同一张凭证不能用于别的订单。
            </p>
            <Input
              size="sm"
              mono
              label="线下凭证号"
              placeholder="银行流水号、收据编号等"
              value={markPaid.reference}
              onChange={(e) => {
                markPaid.setReference(e.target.value)
                markPaid.clearError('reference')
              }}
              error={markPaid.errors.reference}
            />
            <Input
              size="sm"
              label="收款说明（写入审计）"
              placeholder="至少 5 个字"
              value={markPaid.reason}
              onChange={(e) => {
                markPaid.setReason(e.target.value)
                markPaid.clearError('reason')
              }}
              error={markPaid.errors.reason}
            />
          </section>
        )}
      </Modal>

      <ConfirmModal
        open={markPaid.confirming}
        title="标记为已支付？"
        body={`${o.order_no} · ${formatMoney(o.payable_amount)}，凭证 ${markPaid.reference.trim()}。${o.kind === 'topup' ? '余额立即入账。' : o.kind === 'traffic_reset' ? '这一份的本期流量将立即清零。' : '订阅将立即开通。'}同一张凭证不能用于别的订单。`}
        confirmLabel="标记已支付"
        onCancel={() => markPaid.setConfirming(false)}
        onConfirm={markPaid.submit}
      />

      <CancelDialog o={o} open={cancelling && cancellable} onClose={() => setCancelling(false)} />
    </>
  )
}

function PaymentsSection({ id }: { id: string }) {
  const q = useOrderPayments(id)
  return (
    <section className={css.detailSection}>
      <h3 className={css.detailSectionTitle}>支付记录</h3>
      {q.isPending ? (
        <Skeleton height={44} />
      ) : q.isError ? (
        <span className={css.small}>{isApiError(q.error, 'not_found') ? '记录不存在' : `读取失败：${q.error.message}`}</span>
      ) : paymentLines(q.data).length === 0 ? (
        <span className={css.small}>尚无支付尝试</span>
      ) : (
        paymentLines(q.data).map((l) => (
          <div key={l.key} className={css.detailRow}>
            <span className={css.stack}>
              <span>{l.channel}</span>
              <span className={`${css.mono} ${css.small} ${css.ellipsis}`} title={l.ref}>
                {l.ref}
              </span>
            </span>
            <span className={css.detailValue}>
              <span className={css[`tone_${l.tone}`]}>
                {l.label} · {formatDateTime(l.at)}
              </span>
              <span className={css.mono}>
                {formatMoney(l.amount)}
                {l.surcharge ? <span className={css.small}>（含手续费 {formatMoney(l.surcharge)}）</span> : null}
              </span>
            </span>
          </div>
        ))
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
      const r = await api.post(`v1/orders/${encodeURIComponent(o.id)}/query`, orderQueriedSchema, { idempotencyKey: intent.keyFor([o.id, 'query']) })
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
    <section className={css.detailSection}>
      <h3 className={css.detailSectionTitle}>向渠道查单</h3>
      <p className={css.boxText}>
        回调没到时用：向 {o.provider_name ?? o.provider_code} 查询这笔订单，渠道确认已付款就按回调同一流程补记，金额不符会被拒绝。
      </p>
      {result && (
        <p className={`${css.boxText} ${css[`tone_${result.tone}`]}`} role="status">
          {result.text}
        </p>
      )}
      <Button size="sm" busy={busy} onClick={() => void run()}>
        向渠道查单
      </Button>
    </section>
  )
}

function useMarkPaidState(o: OrderDetail) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidateBilling()
  const invalidateUsers = useInvalidateUsers()
  const [reference, setReference] = useState('')
  const [reason, setReason] = useState('')
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [confirming, setConfirming] = useState(false)

  const clearError = (key: string) => setErrors((f) => ({ ...f, [key]: '' }))

  const check = () => {
    const local: Record<string, string> = {}
    const r = referenceProblem(reference)
    if (r) local.reference = r
    const why = reasonProblem(reason, '收款说明')
    if (why) local.reason = why
    if (Object.keys(local).length) return setErrors(local)
    setConfirming(true)
  }

  const submit = async () => {
    const body = { reason: reason.trim(), reference: reference.trim() }
    try {
      await api.post(`v1/orders/${encodeURIComponent(o.id)}/mark-paid`, markedPaidSchema, { body, idempotencyKey: intent.keyFor([o.id, body]) })
      intent.reset()
      setConfirming(false)
      toast(o.kind === 'topup' ? '已标记为已支付，余额已入账' : o.kind === 'traffic_reset' ? '已标记为已支付，本期流量已清零' : '已标记为已支付，订阅已开通')
    } catch (e) {
      setConfirming(false)
      fail(e, { fields: setErrors, intent })
    } finally {
      void invalidate()
      void invalidateUsers()
    }
  }

  return {
    reference,
    setReference,
    reason,
    setReason,
    errors,
    clearError,
    confirming,
    setConfirming,
    check,
    submit,
  }
}

function CancelDialog({ o, open, onClose }: { o: OrderDetail; open: boolean; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidateBilling()
  const [reason, setReason] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    const problem = reasonProblem(reason, '取消原因')
    if (problem) return setError(problem)
    const body = { expected_state_version: o.state_version, reason: reason.trim() }
    setBusy(true)
    try {
      const r = await api.post(`v1/orders/${encodeURIComponent(o.id)}/cancel`, cancelledSchema, { body, idempotencyKey: intent.keyFor([o.id, body]) })
      intent.reset()
      toast(r.already_terminal ? '订单早已取消' : `订单 ${o.order_no} 已取消`)
      setReason('')
      onClose()
    } catch (e) {
      fail(e, { intent })
      if (isApiError(e, 'conflict')) onClose()
    } finally {
      setBusy(false)
      void invalidate()
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      dismissible={!busy}
      title="取消订单？"
      actions={
        <>
          <Button size="dialog" onClick={onClose} disabled={busy}>
            不取消
          </Button>
          <Button size="dialog" variant="danger" busy={busy} onClick={() => void submit()}>
            取消订单
          </Button>
        </>
      }
    >
      <div className={css.dialogBody}>
        <p className={css.dialogText}>
          {o.order_no} 取消后不能恢复，未完成的支付尝试会一并关闭；取消后才到账的钱会进「待处理到账」。
        </p>
        <TextArea
          label="取消原因（写入审计）"
          rows={3}
          value={reason}
          onChange={(e) => {
            setReason(e.target.value)
            setError('')
          }}
          error={error}
          data-autofocus=""
        />
      </div>
    </Modal>
  )
}
