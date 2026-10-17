import { useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Input, Modal, useToast } from '../../../ui'
import { useFailure } from '../../actions'
import { okSchema, type SubscriptionRow } from './api'
import { deviceLimitLabel, subName } from './model'
import css from './Users.module.css'

/** 来源文案：覆盖 / 跟套餐 / 未设 */
const sourceText = (source: 'override' | 'plan' | 'none') =>
  source === 'override' ? '已单独设置' : source === 'plan' ? '跟套餐' : '未设'

/**
 * 改设备上限：POST v1/subscriptions/{id}/device-limit（无 reauth、无幂等，与原订阅卡上的 DeviceLimit 同一请求）。
 * subscription 非 null 即打开；能设具体台数、不限（0）、恢复套餐默认（null）
 */
export function DeviceLimitDialog({
  subscription,
  onClose,
  onDone,
}: {
  subscription: SubscriptionRow | null
  onClose: () => void
  onDone: () => void
}) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const [value, setValue] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  // 打开（或换了一份）时按当前生效上限填初值（0 或 null 表示不限，留空）；只认订阅 id，
  // 详情因实时事件重拉时对象会换新，不能因此清掉正在输入的值
  const [openedFor, setOpenedFor] = useState<string | null>(null)
  const subId = subscription?.id ?? null
  if (subId !== openedFor) {
    setOpenedFor(subId)
    const cur = subscription ? (subscription.device_limit_override ?? subscription.plan_max_devices) : null
    setValue(cur !== null && cur > 0 ? String(cur) : '')
    setError(null)
  }

  if (!subscription) return null
  const s = subscription
  const shown = deviceLimitLabel(s)

  const set = async (limit: number | null) => {
    setBusy(true)
    try {
      await api.post(`v1/subscriptions/${encodeURIComponent(s.id)}/device-limit`, okSchema, { body: { limit } })
      toast(limit === null ? '已恢复套餐默认的设备上限' : limit === 0 ? '已改为不限设备数' : `设备上限已改为 ${limit} 台`)
      onDone()
      onClose()
    } catch (e) {
      fail(e)
    } finally {
      setBusy(false)
    }
  }

  const submit = () => {
    const v = value.trim()
    // 1 到 1000 的整数（/^\d{1,4}$/ 先挡掉超长，再查范围）
    if (!/^\d{1,4}$/.test(v) || Number(v) < 1 || Number(v) > 1000) {
      setError('请填 1 到 1000 的整数')
      return
    }
    void set(Number(v))
  }

  return (
    <Modal
      open
      onClose={onClose}
      dismissible={!busy}
      size="sm"
      title="改设备上限"
      subtitle={subName(s)}
      footerStart={
        <>
          <Button size="dialog" variant="ghost" disabled={busy || s.device_limit_override === 0} onClick={() => void set(0)}>
            不限
          </Button>
          <Button size="dialog" variant="ghost" disabled={busy || s.device_limit_override === null} onClick={() => void set(null)}>
            恢复套餐默认
          </Button>
        </>
      }
      actions={
        <>
          <Button size="dialog" onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={busy} onClick={submit}>
            保存
          </Button>
        </>
      }
    >
      <p className={css.dialogText}>
        在线 {s.online_devices} 台 · 当前上限 {shown.text}（{sourceText(shown.source)}）
      </p>
      <Input
        label="上限（台）"
        inputMode="numeric"
        mono
        value={value}
        onChange={(e) => {
          setValue(e.target.value)
          setError(null)
        }}
        error={error ?? undefined}
        hint="1 到 1000 的整数"
        data-autofocus=""
      />
    </Modal>
  )
}
