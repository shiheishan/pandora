/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../core/api 的 isApiError，依赖 ../../../core/format 的 formatMoney，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 Button / Input / Modal / Segmented / Select / TextArea / useToast，依赖 ../../actions 的 endsIntent / useFailure / useIntentKey，依赖 ./api 的写响应 schema 与类型，依赖 ./model，依赖 ./Users.module.css
 * [OUTPUT]: 对外提供 ActionModal（带表单的写操作对话框骨架，④ 的用户组 / 批量运营 / 流量重置共用）、StatusDialog、ResetPasswordDialog、RotateDialog、BalanceForm
 * [POS]: 用户抽屉的四个写操作（契约后台-03）：启用 / 停用 / 封禁（原因必填、挂 reauth）、替用户设新密码（D-B-2 未决前按方案 A：新密码 + 原因，挂 reauth）、换发订阅链接（保留规则 2 与 5.A D-B-1：只选订阅填原因，成功后不显示也拿不到新地址）、人工调账（元转分、原因必填、reauth + 幂等）。reauth 由外框对话框接管，取消时静默
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState, type ReactNode } from 'react'
import { isApiError } from '../../../core/api'
import { formatMoney } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, Input, Modal, Segmented, Select, TextArea, useToast } from '../../../ui'
import { endsIntent, useFailure, useIntentKey } from '../../actions'
import { balanceSchema, passwordResetSchema, rotatedSchema, statusSetSchema, type SubscriptionRow, type UserDetail } from './api'
import { liveSubscriptions, parseYuan, passwordProblem, REASON_MIN, SUB_STATUS_VIEW } from './model'
import css from './Users.module.css'

const reasonShort = (r: string) => [...r.trim()].length < REASON_MIN

/** 对话框的通用骨架：进行中不可关、按钮用动词 */
export function ActionModal({
  open,
  title,
  busy,
  confirm,
  tone = 'primary',
  disabled = false,
  onCancel,
  onConfirm,
  children,
}: {
  open: boolean
  title: string
  busy: boolean
  confirm: string
  tone?: 'primary' | 'danger'
  disabled?: boolean
  onCancel: () => void
  onConfirm: () => void
  children: ReactNode
}) {
  return (
    <Modal
      open={open}
      onClose={onCancel}
      dismissible={!busy}
      title={title}
      actions={
        <>
          <Button size="dialog" onClick={onCancel} disabled={busy}>
            取消
          </Button>
          <Button size="dialog" variant={tone} busy={busy} disabled={disabled} onClick={onConfirm}>
            {confirm}
          </Button>
        </>
      }
    >
      <div className={css.dialogBody}>{children}</div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// 启用 / 停用 / 封禁：POST v1/users/{id}/status（iam.user.write + reauth，无幂等）
// ---------------------------------------------------------------------------
export function StatusDialog({ user, open, onClose, onDone }: { user: UserDetail; open: boolean; onClose: () => void; onDone: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const enabling = user.status === 'suspended' || user.status === 'banned'
  const [target, setTarget] = useState<'suspended' | 'banned'>('suspended')
  const [reason, setReason] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const close = () => {
    setReason('')
    setError(null)
    onClose()
  }
  const submit = async () => {
    if (!enabling && !reason.trim()) return setError('停用或封禁必须填写原因，会写进审计')
    setBusy(true)
    try {
      const body = enabling ? { status: 'active' as const, reason: reason.trim() } : { status: target, reason: reason.trim() }
      await api.post(`v1/users/${encodeURIComponent(user.id)}/status`, statusSetSchema, { body })
      toast(enabling ? '账号已启用' : target === 'banned' ? '账号已封禁，登录会话已全部失效' : '账号已停用，登录会话已全部失效')
      close()
      onDone()
    } catch (e) {
      // 422（缺原因）没有 fields；409 是「自己」或「最后一个管理员」
      if (isApiError(e, 'validation_failed')) setError(e.message)
      else fail(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <ActionModal
      open={open}
      title={enabling ? '启用该账号？' : '停用该账号？'}
      busy={busy}
      confirm={enabling ? '启用' : target === 'banned' ? '封禁' : '停用'}
      tone={enabling ? 'primary' : 'danger'}
      onCancel={close}
      onConfirm={() => void submit()}
    >
      <p className={css.dialogText}>
        {enabling ? `${user.email} 可以重新登录。` : `${user.email} 的全部登录会话会立即失效，之后无法登录。`}
      </p>
      {!enabling && (
        <div className={css.dialogField}>
          <span className={css.fieldLabel}>方式</span>
          <Segmented
            size="sm"
            label="停用方式"
            options={[
              { value: 'suspended', label: '停用' },
              { value: 'banned', label: '封禁' },
            ]}
            value={target}
            onChange={setTarget}
          />
        </div>
      )}
      <TextArea
        label={enabling ? '原因（选填）' : '原因'}
        rows={2}
        value={reason}
        onChange={(e) => {
          setReason(e.target.value)
          setError(null)
        }}
        error={error ?? undefined}
        hint="写进审计日志"
      />
    </ActionModal>
  )
}

// ---------------------------------------------------------------------------
// 设新密码：POST v1/users/{id}/reset-password（iam.user.write + reauth，无幂等）
// D-B-2 未决前按方案 A：管理员直接设新密码，不发邮件；新密码不回显
// ---------------------------------------------------------------------------
export function ResetPasswordDialog({ user, open, onClose }: { user: UserDetail; open: boolean; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const [password, setPassword] = useState('')
  const [reason, setReason] = useState('')
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)

  const close = () => {
    setPassword('')
    setReason('')
    setErrors({})
    onClose()
  }
  const submit = async () => {
    const local: Record<string, string> = {}
    const problem = passwordProblem(password)
    if (problem) local.password = problem
    if (reasonShort(reason)) local.reason = `请写清原因，至少 ${REASON_MIN} 个字`
    if (Object.keys(local).length) return setErrors(local)
    setBusy(true)
    try {
      await api.post(`v1/users/${encodeURIComponent(user.id)}/reset-password`, passwordResetSchema, { body: { new_password: password, reason: reason.trim() } })
      toast('新密码已生效，该用户的登录会话已全部失效')
      close()
    } catch (e) {
      // 注意键名：密码策略错误在 fields.password，不是 new_password
      fail(e, setErrors)
    } finally {
      setBusy(false)
    }
  }

  return (
    <ActionModal open={open} title="重置密码" busy={busy} confirm="设为新密码" onCancel={close} onConfirm={() => void submit()}>
      <p className={css.dialogText}>为 {user.email} 设一个新密码。生效后该用户的全部会话立即失效；新密码不会再显示，请通过安全渠道告诉用户。</p>
      <Input
        label="新密码"
        type="text"
        autoComplete="new-password"
        spellCheck={false}
        mono
        value={password}
        onChange={(e) => {
          setPassword(e.target.value)
          setErrors((f) => ({ ...f, password: '' }))
        }}
        error={errors.password || undefined}
        hint="至少 8 位，同时包含字母和数字"
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
        hint="写进审计日志，至少 5 个字"
      />
    </ActionModal>
  )
}

// ---------------------------------------------------------------------------
// 换发订阅链接：POST v1/subscriptions/{id}/rotate（iam.user.write + reauth，无幂等）
// 保留规则 2：后台看不到订阅地址；响应只有 user_email / old_revoked（schema 用 strict 守住）
// ---------------------------------------------------------------------------
export function RotateDialog({ user, open, onClose, onDone }: { user: UserDetail; open: boolean; onClose: () => void; onDone: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const choices = liveSubscriptions(user.subscriptions)
  const [picked, setPicked] = useState<string>('')
  const [reason, setReason] = useState('')
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const subId = picked || choices[0]?.id || ''

  const close = () => {
    setPicked('')
    setReason('')
    setErrors({})
    onClose()
  }
  const submit = async () => {
    if (!subId) return
    if (reasonShort(reason)) return setErrors({ reason: `请写清原因，至少 ${REASON_MIN} 个字` })
    setBusy(true)
    try {
      const r = await api.post(`v1/subscriptions/${encodeURIComponent(subId)}/rotate`, rotatedSchema, { body: { reason: reason.trim() } })
      toast(`已换发，请让 ${r.user_email} 到门户重新复制订阅地址`)
      close()
      onDone()
    } catch (e) {
      fail(e, setErrors)
    } finally {
      setBusy(false)
    }
  }

  return (
    <ActionModal open={open} title="更换订阅地址？" busy={busy} confirm="更换" tone="danger" disabled={!subId} onCancel={close} onConfirm={() => void submit()}>
      <p className={css.dialogText}>旧地址立即失效，用户需要到门户重新复制地址、在客户端重新导入。后台看不到新地址。</p>
      {choices.length > 1 && (
        <Select label="订阅" options={choices.map((s) => ({ value: s.id, label: subLabel(s) }))} value={subId} onChange={(e) => setPicked(e.target.value)} />
      )}
      {choices.length === 1 && <p className={css.dialogText}>订阅：{subLabel(choices[0]!)}</p>}
      <TextArea
        label="原因"
        rows={2}
        value={reason}
        onChange={(e) => {
          setReason(e.target.value)
          setErrors({})
        }}
        error={errors.reason}
        hint="写进审计日志，至少 5 个字"
      />
    </ActionModal>
  )
}

function subLabel(s: SubscriptionRow): string {
  return `${s.plan_name} · v${s.plan_version} · ${SUB_STATUS_VIEW[s.status].label}`
}

// ---------------------------------------------------------------------------
// 人工调账：POST v1/users/{id}/balance（billing.provider.write + reauth + 幂等 admin_user_balance_adjust）
// 抽屉头部下方的行内表单（设计稿），提交前再确认一次金额
// ---------------------------------------------------------------------------
export function BalanceForm({ user, onClose, onDone }: { user: UserDetail; onClose: () => void; onDone: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const [amount, setAmount] = useState('')
  const [reason, setReason] = useState('')
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [confirming, setConfirming] = useState<number | null>(null)
  const [busy, setBusy] = useState(false)

  const check = () => {
    const cents = parseYuan(amount)
    const local: Record<string, string> = {}
    if (cents === null) local.amount = '金额写成 +50 或 -20，最多两位小数，不能为 0'
    if (reasonShort(reason)) local.reason = `原因至少 ${REASON_MIN} 个字`
    if (Object.keys(local).length) return setErrors(local)
    setConfirming(cents)
  }
  const submit = async () => {
    if (confirming === null) return
    const body = { amount: confirming, currency: user.currency, reason: reason.trim() }
    setBusy(true)
    try {
      const r = await api.post(`v1/users/${encodeURIComponent(user.id)}/balance`, balanceSchema, { body, idempotencyKey: intent.keyFor([user.id, body]) })
      intent.reset()
      toast(`余额已调整为 ${formatMoney(r.balance, user.currency)}`)
      setConfirming(null)
      onDone()
      onClose()
    } catch (e) {
      if (endsIntent(e)) intent.reset()
      setConfirming(null)
      // 金额为 0 的 422 没有 fields；409 余额不足扣减
      if (isApiError(e, 'validation_failed') && Object.keys(e.fields).length === 0) setErrors({ amount: e.message })
      else fail(e, setErrors)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className={css.balanceForm}>
      <Input
        aria-label="调整金额（元）"
        placeholder="+50 或 -20"
        mono
        value={amount}
        onChange={(e) => {
          setAmount(e.target.value)
          setErrors((f) => ({ ...f, amount: '' }))
        }}
        error={errors.amount || undefined}
        data-autofocus=""
      />
      <Input
        aria-label="调整原因"
        placeholder="调整原因（写入审计）"
        value={reason}
        onChange={(e) => {
          setReason(e.target.value)
          setErrors((f) => ({ ...f, reason: '' }))
        }}
        error={errors.reason || undefined}
      />
      <div className={css.balanceActions}>
        <Button size="sm" onClick={onClose}>
          取消
        </Button>
        <Button size="sm" variant="primary" onClick={check}>
          确认
        </Button>
      </div>
      <ActionModal
        open={confirming !== null}
        title="调整余额"
        busy={busy}
        confirm="确认调整"
        tone={confirming !== null && confirming < 0 ? 'danger' : 'primary'}
        onCancel={() => setConfirming(null)}
        onConfirm={() => void submit()}
      >
        <p className={css.dialogText}>
          {confirming !== null && confirming < 0 ? '扣减' : '增加'} {formatMoney(Math.abs(confirming ?? 0), user.currency)}，当前余额 {formatMoney(user.balance, user.currency)}。
        </p>
        <p className={css.dialogText}>原因：{reason.trim()}</p>
      </ActionModal>
    </div>
  )
}
