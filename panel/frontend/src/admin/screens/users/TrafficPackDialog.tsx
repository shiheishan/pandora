import { useState } from 'react'
import { formatBytes } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, Input, Select, TextArea, useToast } from '../../../ui'
import { useFailure, useIntentKey } from '../../actions'
import { trafficGrantedSchema, type SubscriptionRow, type UserDetail } from './api'
import { ActionModal } from './dialogs'
import { SUB_STATUS_VIEW } from './model'
import { GRANT_GB_MAX, GRANT_GB_PRESETS, grantReasonProblem, parseGrantGB } from './trafficPack'
import css from './Users.module.css'

// ---------------------------------------------------------------------------
// 加流量包：POST v1/subscriptions/{id}/traffic-pack（billing.adjustment.write + reauth + 幂等
// subscription_admin_traffic_grant）。发一笔不过期的流量包，挂在订阅所属的用户身上，
// 每周期先扣套餐额度、再扣流量包；任何状态的订阅都能挑（余额在用户身上）
// ---------------------------------------------------------------------------
export function TrafficPackDialog({ user, open, onClose, onDone }: { user: UserDetail; open: boolean; onClose: () => void; onDone: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const choices = user.subscriptions
  const [picked, setPicked] = useState('')
  const [gb, setGb] = useState('10')
  const [reason, setReason] = useState('')
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const sub = choices.find((s) => s.id === picked) ?? choices[0]
  const bytes = parseGrantGB(gb)

  const close = () => {
    setPicked('')
    setGb('10')
    setReason('')
    setErrors({})
    intent.reset()
    onClose()
  }
  const submit = async () => {
    if (!sub) return
    const local: Record<string, string> = {}
    if (bytes === null) local.bytes = `流量是 1 到 ${GRANT_GB_MAX} 之间的整数 GB`
    const problem = grantReasonProblem(reason)
    if (problem) local.reason = problem
    if (Object.keys(local).length || bytes === null) return setErrors(local)
    const body = { bytes, reason: reason.trim() }
    setBusy(true)
    try {
      const r = await api.post(`v1/subscriptions/${encodeURIComponent(sub.id)}/traffic-pack`, trafficGrantedSchema, {
        body,
        idempotencyKey: intent.keyFor([sub.id, body]),
      })
      intent.reset()
      toast(`已为 ${r.user_email} 加 ${formatBytes(r.granted_bytes)} 流量包，流量包剩余合计 ${formatBytes(r.remaining_bytes_total)}`)
      close()
      onDone()
    } catch (e) {
      fail(e, { fields: setErrors, intent })
    } finally {
      setBusy(false)
    }
  }

  return (
    <ActionModal open={open} title="加流量包" busy={busy} confirm="确认发放" disabled={!sub} onCancel={close} onConfirm={() => void submit()}>
      <p className={css.dialogText}>发一笔不过期的流量包，挂在用户身上、用完为止；每个周期先扣套餐流量，再扣流量包。发出后不能撤回。</p>
      {choices.length > 1 && (
        <Select label="订阅" options={choices.map((s) => ({ value: s.id, label: subLabel(s) }))} value={sub?.id ?? ''} onChange={(e) => setPicked(e.target.value)} />
      )}
      {choices.length === 1 && sub && <p className={css.dialogText}>订阅：{subLabel(sub)}</p>}
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
        hint={`1 到 ${GRANT_GB_MAX} GB`}
        data-autofocus=""
      />
      <div className={css.dialogField}>
        <span className={css.fieldLabel}>常用</span>
        {GRANT_GB_PRESETS.map((n) => (
          <Button key={n} size="sm" aria-pressed={bytes === parseGrantGB(String(n))} onClick={() => setGb(String(n))}>
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

function subLabel(s: SubscriptionRow): string {
  return `${s.plan_name} · v${s.plan_version} · ${SUB_STATUS_VIEW[s.status].label}`
}
