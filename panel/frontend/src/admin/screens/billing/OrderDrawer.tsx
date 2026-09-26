/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../core/api 的 isApiError，依赖 ../../../core/format 的 formatDateTime / formatMoney / relativeTime，依赖 ../../../core/router 的 href，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 Button / ConfirmModal / Drawer / Empty / Input / Modal / Skeleton / Tag / TextArea / useToast，依赖 ../../actions 的 useCan / useFailure / useIntentKey，依赖 ../users/api 的 useInvalidateUsers，依赖 ./api，依赖 ./model，依赖 ./Billing.module.css
 * [OUTPUT]: 对外提供 OrderDrawer
 * [POS]: 订单抽屉（480 宽，后台-05）：头部（订单号、状态、创建时间）、facts（用户可点到用户抽屉、内容、金额与应付、渠道、来源「人工开单 · 开单人」、支付截止与取消原因）、多项订单的订单项、支付记录（billing.payment.read 才请求，没有就整块不画）、待支付单的「手工标记已支付」（凭证号 + 收款说明，reauth + 幂等）与底部「取消订单」（取消原因必填，带 state_version，幂等）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { formatDateTime, formatMoney, relativeTime } from '../../../core/format'
import { href } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Drawer, Empty, Input, Modal, Skeleton, Tag, TextArea, useToast } from '../../../ui'
import { useCan, useFailure, useIntentKey } from '../../actions'
import { useInvalidateUsers } from '../users/api'
import { cancelledSchema, markedPaidSchema, useInvalidateBilling, useOrder, useOrderPayments, type OrderDetail } from './api'
import css from './Billing.module.css'
import { canCancel, canMarkPaid, ORDER_STATUS_VIEW, orderFacts, paymentLines, reasonProblem, referenceProblem } from './model'

export function OrderDrawer({ id, onClose, now }: { id: string | null; onClose: () => void; now: Date }) {
  const q = useOrder(id)
  const can = useCan()
  const [cancelling, setCancelling] = useState(false)
  const o = q.data
  const cancellable = !!o && can('billing.order.write') && canCancel(o)
  return (
    <Drawer
      open={id !== null}
      onClose={onClose}
      title={o ? <Title o={o} now={now} /> : '订单详情'}
      actions={
        cancellable ? (
          <Button size="sm" variant="danger" onClick={() => setCancelling(true)}>
            取消订单
          </Button>
        ) : undefined
      }
    >
      {q.isPending ? (
        <div className={css.drawerBody} role="status" aria-label="加载中">
          <Skeleton height={160} />
          <Skeleton height={80} />
        </div>
      ) : q.isError ? (
        isApiError(q.error, 'not_found') ? (
          <Empty bare title="订单不存在" description="链接可能有误，或你没有查看订单的权限。" />
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
        )
      ) : (
        <Body key={`${q.data.id}:${q.data.state_version}`} o={q.data} />
      )}
      {o && <CancelDialog key={o.id} o={o} open={cancelling && cancellable} onClose={() => setCancelling(false)} />}
    </Drawer>
  )
}

function Title({ o, now }: { o: OrderDetail; now: Date }) {
  const st = ORDER_STATUS_VIEW[o.status]
  return (
    <span className={css.drawerTitle}>
      <span className={css.orderNo}>{o.order_no}</span>
      <span className={css.titleMeta}>
        <Tag tone={st.tone}>{st.label}</Tag>
        <span title={formatDateTime(o.created_at)}>{relativeTime(o.created_at, now)}</span>
      </span>
    </span>
  )
}

function Body({ o }: { o: OrderDetail }) {
  const can = useCan()
  return (
    <div className={css.drawerBody}>
      <dl className={css.facts}>
        {orderFacts(o).map(([k, v]) => (
          <Fact key={k} k={k}>
            {k === '用户' && can('iam.user.read') ? (
              <a className={css.link} href={href(`/users/list/${encodeURIComponent(o.user_id)}`)}>
                {v}
              </a>
            ) : (
              v
            )}
          </Fact>
        ))}
      </dl>
      {o.items.length > 1 && (
        <section className={css.section}>
          <h3 className={css.sectionTitle}>订单项</h3>
          <ul className={css.lines}>
            {o.items.map((i) => (
              <li key={i.id} className={css.line}>
                <span className={css.ellipsis}>{i.plan_name ?? i.product_name}</span>
                <span className={css.lineAmount}>{formatMoney(i.line_amount, i.currency)}</span>
                <span className={`${css.lineState} ${css.small}`}>× {i.quantity}</span>
              </li>
            ))}
          </ul>
        </section>
      )}
      {can('billing.payment.read') && <Payments id={o.id} />}
      {can('billing.order.write') && canMarkPaid(o) && <MarkPaid o={o} />}
    </div>
  )
}

function Fact({ k, children }: { k: string; children: React.ReactNode }) {
  return (
    <>
      <dt>{k}</dt>
      <dd>{children}</dd>
    </>
  )
}

/** 支付记录：以支付尝试为行，有入账看入账；线下入账标「人工确认」（契约后台-05） */
function Payments({ id }: { id: string }) {
  const q = useOrderPayments(id, true)
  return (
    <section className={css.section}>
      <h3 className={css.sectionTitle}>支付记录</h3>
      {q.isPending ? (
        <Skeleton height={44} />
      ) : q.isError ? (
        // 404 = 没有 billing.payment.read（进来前已判过，这里只可能是订单刚被删）：不当报错
        <span className={css.small}>{isApiError(q.error, 'not_found') ? '无权查看或记录不存在' : `读取失败：${q.error.message}`}</span>
      ) : paymentLines(q.data).length === 0 ? (
        <span className={css.small}>尚无支付尝试</span>
      ) : (
        <ul className={css.lines}>
          {paymentLines(q.data).map((l) => (
            <li key={l.key} className={css.line}>
              <span className={css.stack}>
                <span>{l.channel}</span>
                <span className={`${css.mono} ${css.small} ${css.ellipsis}`} title={l.ref}>
                  {l.ref}
                </span>
              </span>
              <span className={css.lineAmount}>{formatMoney(l.amount, l.currency)}</span>
              <span className={`${css.lineState} ${css[`tone_${l.tone}`]}`}>{l.label}</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

// ---------------------------------------------------------------------------
// 手工标记已支付：POST v1/orders/{id}/mark-paid（billing.order.write + reauth + 幂等）
// 金额由服务端从订单读；设计稿只有凭证号，契约补必填的收款说明
// ---------------------------------------------------------------------------
function MarkPaid({ o }: { o: OrderDetail }) {
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
      const r = await api.post(`v1/orders/${encodeURIComponent(o.id)}/mark-paid`, markedPaidSchema, { body, idempotencyKey: intent.keyFor([o.id, body]) })
      intent.reset()
      setConfirming(false)
      toast(r.already_handled ? '这张凭证已经入过账，没有重复记账' : o.kind === 'topup' ? '已标记为已支付，余额已入账' : '已标记为已支付，订阅已开通')
    } catch (e) {
      setConfirming(false)
      fail(e, { fields: setErrors, intent })
    } finally {
      void invalidate()
      void invalidateUsers()
    }
  }

  return (
    <div className={css.box}>
      <h3 className={css.sectionTitle}>手工标记已支付</h3>
      <p className={css.boxText}>仅在确认线下到账或渠道回调丢失时使用：按线下收款入账 {formatMoney(o.payable_amount, o.currency)}，订阅立即开通，写入审计。</p>
      <Input
        size="sm"
        mono
        aria-label="渠道流水号 / 转账凭证"
        placeholder="渠道流水号 / 转账凭证"
        value={reference}
        onChange={(e) => {
          setReference(e.target.value)
          setErrors((f) => ({ ...f, reference: '' }))
        }}
        error={errors.reference}
      />
      <Input
        size="sm"
        aria-label="收款说明"
        placeholder="收款说明（写入审计，至少 5 个字）"
        value={reason}
        onChange={(e) => {
          setReason(e.target.value)
          setErrors((f) => ({ ...f, reason: '' }))
        }}
        error={errors.reason}
      />
      <Button size="sm" variant="primary" onClick={check}>
        标记已支付
      </Button>
      <ConfirmModal
        open={confirming}
        title="标记为已支付？"
        body={`${o.order_no} · ${formatMoney(o.payable_amount, o.currency)}，凭证 ${reference.trim()}。${o.kind === 'topup' ? '余额立即入账。' : '订阅将立即开通。'}同一张凭证不能用于别的订单。`}
        confirmLabel="标记已支付"
        onCancel={() => setConfirming(false)}
        onConfirm={submit}
      />
    </div>
  )
}

// ---------------------------------------------------------------------------
// 取消订单：POST v1/orders/{id}/cancel（billing.order.write + 幂等，不挂 reauth）
// 设计确认框没有原因输入，契约补必填的取消原因（后端 5–500 字，错了回 400 不是 422）
// ---------------------------------------------------------------------------
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
      // 409（已有入账、状态或版本变了）：关框，抽屉按最新状态重画
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
        <p className={css.dialogText}>{o.order_no} 取消后不能恢复，未完成的支付尝试会一并关闭；取消后才到账的钱会进「挂账」。</p>
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
