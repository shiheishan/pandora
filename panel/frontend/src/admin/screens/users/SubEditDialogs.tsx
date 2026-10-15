import { useState } from 'react'
import { formatBytes, formatDateTime } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Input, TextArea, useToast } from '../../../ui'
import { useFailure } from '../../actions'
import { okSchema, periodEndSetSchema, type SubscriptionRow } from './api'
import { ActionModal } from './dialogs'
import { REASON_MAX, REASON_MIN, subName, trafficQuota } from './model'
import { GiB, isoToLocalInput, localToRFC3339, parseLimitGiB, periodEndProblem } from './subEdit'
import css from './Users.module.css'

/** 原因 5–500 字（后端 fields.reason 同文案） */
function reasonProblem(reason: string): string | null {
  const n = [...reason.trim()].length
  return n < REASON_MIN || n > REASON_MAX ? `请写清原因，${REASON_MIN} 到 ${REASON_MAX} 个字` : null
}

// ---------------------------------------------------------------------------
// 改到期：POST v1/subscriptions/{id}/period-end（reauth）
// ---------------------------------------------------------------------------
export function PeriodEndDialog({
  subscription,
  open,
  onClose,
  onDone,
  now,
}: {
  subscription: SubscriptionRow | null
  open: boolean
  onClose: () => void
  onDone: () => void
  now: Date
}) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  // null 表示还没动过：显示当前到期（对话框常驻挂载，换一份订阅打开时自然取那一份的到期）
  const [draftEnd, setDraftEnd] = useState<string | null>(null)
  const localEnd = draftEnd ?? (subscription?.current_period_end ? isoToLocalInput(subscription.current_period_end) : '')
  const [reason, setReason] = useState('')
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)

  const close = () => {
    setDraftEnd(null)
    setReason('')
    setErrors({})
    onClose()
  }

  const submit = async () => {
    if (!subscription) return
    const local: Record<string, string> = {}
    const iso = localToRFC3339(localEnd)
    if (!iso) local.period_end = '时间格式不正确'
    else {
      const problem = periodEndProblem(iso, now)
      if (problem) local.period_end = problem
    }
    const badReason = reasonProblem(reason)
    if (badReason) local.reason = badReason
    if (Object.keys(local).length) return setErrors(local)

    setBusy(true)
    try {
      const out = await api.post(`v1/subscriptions/${encodeURIComponent(subscription.id)}/period-end`, periodEndSetSchema, {
        body: { period_end: iso!, reason: reason.trim() },
      })
      toast(`到期时间已改为 ${formatDateTime(out.period_end)}`)
      close()
      onDone()
    } catch (e) {
      fail(e, { fields: setErrors })
    } finally {
      setBusy(false)
    }
  }

  return (
    <ActionModal open={open} title="改到期时间" busy={busy} confirm="确认修改" disabled={!subscription} onCancel={close} onConfirm={() => void submit()}>
      {subscription?.current_period_end && <p className={css.dialogText}>当前到期：{formatDateTime(subscription.current_period_end)}</p>}
      <p className={css.dialogText}>可提前也可延后；只改到期时刻，不折算流量、不改状态。已过期的请用「加时长」救回。</p>
      <Input
        label="新到期时间"
        type="datetime-local"
        value={localEnd}
        onChange={(e) => {
          setDraftEnd(e.target.value)
          setErrors((f) => ({ ...f, period_end: '' }))
        }}
        error={errors.period_end || undefined}
        data-autofocus=""
      />
      <TextArea
        label="原因"
        rows={2}
        value={reason}
        onChange={(e) => {
          setReason(e.target.value)
          setErrors((f) => ({ ...f, reason: '' }))
        }}
        error={errors.reason || undefined}
        hint="写进审计日志，5 到 500 个字"
      />
    </ActionModal>
  )
}

// ---------------------------------------------------------------------------
// 改流量上限：POST v1/subscriptions/{id}/quota（reauth）
// ---------------------------------------------------------------------------
export function QuotaLimitDialog({
  subscription,
  open,
  onClose,
  onDone,
}: {
  subscription: SubscriptionRow | null
  open: boolean
  onClose: () => void
  onDone: () => void
}) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const q = subscription ? trafficQuota(subscription.quotas) : undefined
  // null 表示还没动过：显示当前上限（GiB）
  const [draftGiB, setDraftGiB] = useState<string | null>(null)
  const limitGiB = draftGiB ?? (q && q.limit !== null ? String(Number((q.limit / GiB).toFixed(3))) : '')
  const [reason, setReason] = useState('')
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)

  const close = () => {
    setDraftGiB(null)
    setReason('')
    setErrors({})
    onClose()
  }

  const submit = async () => {
    if (!subscription || !q) return
    const local: Record<string, string> = {}
    const parsed = parseLimitGiB(limitGiB)
    if ('error' in parsed) local.limit_bytes = parsed.error
    const badReason = reasonProblem(reason)
    if (badReason) local.reason = badReason
    if ('error' in parsed || Object.keys(local).length) return setErrors(local)

    const limit_bytes = parsed.bytes
    setBusy(true)
    try {
      await api.post(`v1/subscriptions/${encodeURIComponent(subscription.id)}/quota`, okSchema, {
        body: { metric: q.metric, period: q.period, limit_bytes, reason: reason.trim() },
      })
      toast('流量上限已更新')
      close()
      onDone()
    } catch (e) {
      fail(e, { fields: setErrors })
    } finally {
      setBusy(false)
    }
  }

  return (
    <ActionModal open={open} title="改流量上限" busy={busy} confirm="确认修改" disabled={!subscription || !q} onCancel={close} onConfirm={() => void submit()}>
      {subscription && q && (
        <>
          <p className={css.dialogText}>订阅：{subName(subscription)}</p>
          <p className={css.dialogText}>
            当前上限：{q.limit === null ? '不限' : formatBytes(q.limit)} · 本期已用：{formatBytes(q.consumed)}
          </p>
          <Input
            label="新上限（GiB，留空为不限）"
            inputMode="decimal"
            mono
            value={limitGiB}
            onChange={(e) => {
              setDraftGiB(e.target.value)
              setErrors((f) => ({ ...f, limit_bytes: '' }))
            }}
            error={errors.limit_bytes || undefined}
            data-autofocus=""
          />
          <TextArea
            label="原因"
            rows={2}
            value={reason}
            onChange={(e) => {
              setReason(e.target.value)
              setErrors((f) => ({ ...f, reason: '' }))
            }}
            error={errors.reason || undefined}
            hint="写进审计日志，5 到 500 个字"
          />
        </>
      )}
    </ActionModal>
  )
}
