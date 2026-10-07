import { useState } from 'react'
import { formatDateTime } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, Input, Select, TextArea, useToast } from '../../../ui'
import { useFailure, useIntentKey } from '../../actions'
import { extendedSchema, type SubscriptionRow, type UserDetail } from './api'
import { ActionModal } from './dialogs'
import { EXTEND_DAYS_MAX, EXTEND_PRESETS, extendableSubscriptions, extendedEnd, extendReasonProblem, parseExtendDays, SUB_STATUS_VIEW } from './model'
import css from './Users.module.css'

// ---------------------------------------------------------------------------
// 加时长：POST v1/subscriptions/{id}/extend（billing.adjustment.write + reauth + 幂等 subscription_admin_extend）
// 订阅周期末、本周期流量配额、订阅链接有效期一起往后推；已用流量不清零，不开新周期
// ---------------------------------------------------------------------------
export function ExtendDialog({ user, open, onClose, onDone, now }: { user: UserDetail; open: boolean; onClose: () => void; onDone: () => void; now: Date }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const choices = extendableSubscriptions(user.subscriptions)
  const [picked, setPicked] = useState('')
  const [days, setDays] = useState('30')
  const [reason, setReason] = useState('')
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const sub = choices.find((s) => s.id === picked) ?? choices[0]
  const parsed = parseExtendDays(days)

  const close = () => {
    setPicked('')
    setDays('30')
    setReason('')
    setErrors({})
    intent.reset()
    onClose()
  }
  const submit = async () => {
    if (!sub) return
    const local: Record<string, string> = {}
    if (parsed === null) local.days = `天数是 1 到 ${EXTEND_DAYS_MAX} 之间的整数`
    const problem = extendReasonProblem(reason)
    if (problem) local.reason = problem
    if (Object.keys(local).length || parsed === null) return setErrors(local)
    const body = { days: parsed, reason: reason.trim() }
    setBusy(true)
    try {
      const r = await api.post(`v1/subscriptions/${encodeURIComponent(sub.id)}/extend`, extendedSchema, { body, idempotencyKey: intent.keyFor([sub.id, body]) })
      intent.reset()
      toast(`已为 ${r.user_email} 延长 ${r.days} 天，新到期 ${formatDateTime(r.period_end)}`)
      close()
      onDone()
    } catch (e) {
      // 409（订阅已不在生效中）与没有到期时间的 422 没有 fields，Toast 说服务端的话
      fail(e, { fields: setErrors, intent })
    } finally {
      setBusy(false)
    }
  }

  return (
    <ActionModal open={open} title="加时长" busy={busy} confirm="确认延长" disabled={!sub} onCancel={close} onConfirm={() => void submit()}>
      <p className={css.dialogText}>到期时间、本周期流量配额与订阅链接的有效期一起往后推；已用流量不清零。已过到期日的订阅从现在起算。</p>
      {choices.length > 1 && (
        <Select label="订阅" options={choices.map((s) => ({ value: s.id, label: subLabel(s) }))} value={sub?.id ?? ''} onChange={(e) => setPicked(e.target.value)} />
      )}
      {choices.length === 1 && sub && <p className={css.dialogText}>订阅：{subLabel(sub)}</p>}
      <Input
        label="延长天数"
        inputMode="numeric"
        mono
        value={days}
        onChange={(e) => {
          setDays(e.target.value)
          setErrors((f) => ({ ...f, days: '' }))
        }}
        error={errors.days || undefined}
        hint={`1 到 ${EXTEND_DAYS_MAX} 天`}
        data-autofocus=""
      />
      <div className={css.dialogField}>
        <span className={css.fieldLabel}>常用</span>
        {EXTEND_PRESETS.map((n) => (
          <Button key={n} size="sm" aria-pressed={parsed === n} onClick={() => setDays(String(n))}>
            {n} 天
          </Button>
        ))}
      </div>
      {sub?.current_period_end && parsed !== null && (
        <p className={css.dialogText}>
          原到期 {formatDateTime(sub.current_period_end)}，延长后约 {formatDateTime(extendedEnd(sub.current_period_end, parsed, now))}
        </p>
      )}
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

function subLabel(s: SubscriptionRow): string {
  return `${s.plan_name} · v${s.plan_version} · ${SUB_STATUS_VIEW[s.status].label}`
}
