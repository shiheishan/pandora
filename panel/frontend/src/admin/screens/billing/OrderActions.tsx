import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { formatMoney } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Input, Modal, TextArea, useToast } from '../../../ui'
import { useFailure, useIntentKey } from '../../actions'
import { useInvalidateUsers } from '../users/api'
import { cancelledSchema, markedPaidSchema, useInvalidateBilling, type OrderDetail } from './api'
import css from './Orders.module.css'
import { markPaidConfirm, markPaidToast, reasonProblem, referenceProblem } from './model'

/** 取消订单：原因必填，写入审计 */
export function CancelDialog({ o, open, onClose }: { o: OrderDetail; open: boolean; onClose: () => void }) {
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
      const r = await api.post(`v1/orders/${encodeURIComponent(o.id)}/cancel`, cancelledSchema, {
        body,
        idempotencyKey: intent.keyFor([o.id, body]),
      })
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

/** 标记已支付：先填凭证号与收款说明，「下一步」校验通过后再确认，确认后才提交 */
export function MarkPaidDialogs({ o, open, onClose }: { o: OrderDetail; open: boolean; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidateBilling()
  const invalidateUsers = useInvalidateUsers()
  const [reference, setReference] = useState('')
  const [reason, setReason] = useState('')
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [step, setStep] = useState<'form' | 'confirm'>('form')

  // 关闭时清空，下次打开从第一步开始（不在 effect 里 setState）
  const close = () => {
    setStep('form')
    setReference('')
    setReason('')
    setErrors({})
    onClose()
  }

  const next = () => {
    const local: Record<string, string> = {}
    const r = referenceProblem(reference)
    if (r) local.reference = r
    const why = reasonProblem(reason, '收款说明')
    if (why) local.reason = why
    if (Object.keys(local).length) return setErrors(local)
    setStep('confirm')
  }

  const submit = async () => {
    const body = { reason: reason.trim(), reference: reference.trim() }
    try {
      await api.post(`v1/orders/${encodeURIComponent(o.id)}/mark-paid`, markedPaidSchema, {
        body,
        idempotencyKey: intent.keyFor([o.id, body]),
      })
      intent.reset()
      toast(markPaidToast(o.kind))
      close()
    } catch (e) {
      setStep('form')
      fail(e, { fields: setErrors, intent })
    } finally {
      void invalidate()
      void invalidateUsers()
    }
  }

  return (
    <>
      <Modal
        open={open && step === 'form'}
        onClose={close}
        title="标记已支付"
        actions={
          <>
            <Button size="dialog" onClick={close}>
              取消
            </Button>
            <Button size="dialog" variant="primary" onClick={next}>
              下一步
            </Button>
          </>
        }
      >
        <div className={css.dialogBody}>
          <p className={css.dialogText}>
            已通过线下方式收到这笔钱时使用：填凭证号与收款说明，订单立即入账（{formatMoney(o.payable_amount)}）。同一张凭证不能用于别的订单。
          </p>
          <Input
            mono
            label="线下凭证号"
            placeholder="银行流水号、收据编号等"
            value={reference}
            onChange={(e) => {
              setReference(e.target.value)
              setErrors((f) => ({ ...f, reference: '' }))
            }}
            error={errors.reference}
            data-autofocus=""
          />
          <Input
            label="收款说明（写入审计）"
            placeholder="至少 5 个字"
            value={reason}
            onChange={(e) => {
              setReason(e.target.value)
              setErrors((f) => ({ ...f, reason: '' }))
            }}
            error={errors.reason}
          />
        </div>
      </Modal>
      <ConfirmModal
        open={open && step === 'confirm'}
        title="标记为已支付？"
        body={markPaidConfirm(o, reference)}
        confirmLabel="标记已支付"
        onCancel={() => setStep('form')}
        onConfirm={submit}
      />
    </>
  )
}
