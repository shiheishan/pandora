import { useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Input, TextArea, useToast } from '../../../ui'
import { useFailure, useIntentKey } from '../../actions'
import { trafficGrantedSchema, type SubscriptionRow, type UserDetail } from './api'
import { ActionModal } from './dialogs'
import { GRANT_GB_PRESETS, grantReasonProblem, parseGrantGB, planKind, subName } from './model'
import css from './Users.module.css'

export function GrantTrafficDialog({
  user,
  subscription,
  onClose,
  onDone,
}: {
  user: UserDetail
  subscription: SubscriptionRow | null
  onClose: () => void
  onDone: () => void
}) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const [gb, setGb] = useState('10')
  const [reason, setReason] = useState('')
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)

  const bytes = parseGrantGB(gb)
  const isOneTime = subscription ? planKind(subscription) === 'one_time' : false
  const name = subscription ? subName(subscription) : ''

  const close = () => {
    setGb('10')
    setReason('')
    setErrors({})
    intent.reset()
    onClose()
  }

  const submit = async () => {
    if (!subscription) return
    const local: Record<string, string> = {}
    if (bytes === null) local.bytes = '填 1 到 10240 的整数'
    const problem = grantReasonProblem(reason)
    if (problem) local.reason = problem
    if (Object.keys(local).length || bytes === null) return setErrors(local)

    const body = { bytes, reason: reason.trim() }
    setBusy(true)
    try {
      const r = await api.post(`v1/subscriptions/${encodeURIComponent(subscription.id)}/traffic-pack`, trafficGrantedSchema, {
        body,
        idempotencyKey: intent.keyFor([subscription.id, body]),
      })
      intent.reset()
      const nGb = Math.round(r.granted_bytes / (1024 ** 3))
      toast(`已为 ${user.email} 的${subName(subscription)}加 ${nGb} GB 流量`)
      close()
      onDone()
    } catch (e) {
      fail(e, { fields: setErrors, intent })
    } finally {
      setBusy(false)
    }
  }

  return (
    <ActionModal
      open={subscription !== null}
      title="加流量"
      busy={busy}
      confirm="确认发放"
      disabled={!subscription}
      onCancel={close}
      onConfirm={() => void submit()}
    >
      {subscription && (
        <p className={css.dialogText}>
          {isOneTime
            ? `加到 ${name} 的总流量上，不清零、用完即止。`
            : `加到 ${name} 的本周期流量上限，本周期结束作废。`}
        </p>
      )}
      <Input
        label="流量（GB）"
        inputMode="numeric"
        mono
        value={gb}
        onChange={(e) => {
          setGb(e.target.value)
          setErrors((f) => ({ ...f, bytes: '' }))
        }}
        error={errors.bytes || undefined}
        hint="1 到 10240 GB"
        data-autofocus=""
      />
      <div className={css.dialogField}>
        <span className={css.fieldLabel}>常用</span>
        {GRANT_GB_PRESETS.map((n) => (
          <Button
            key={n}
            size="sm"
            aria-pressed={gb === String(n)}
            onClick={() => {
              setGb(String(n))
              setErrors((f) => ({ ...f, bytes: '' }))
            }}
          >
            {n} GB
          </Button>
        ))}
      </div>
      <TextArea
        label="原因"
        rows={2}
        value={reason}
        onChange={(e) => {
          setReason(e.target.value)
          setErrors((f) => ({ ...f, reason: '' }))
        }}
        error={errors.reason || undefined}
        hint="写进审计日志，至少 5 个字"
      />
    </ActionModal>
  )
}
