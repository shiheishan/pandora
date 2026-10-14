import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { useApi } from '../../../shell/runtime'
import { Input, TextArea, useToast } from '../../../ui'
import { useFailure } from '../../actions'
import { emailSetSchema, okSchema, userDeletedSchema, useFindUserByEmail, type UserDetail } from './api'
import { ActionModal } from './dialogs'
import { REASON_MAX, REASON_MIN } from './model'
import css from './Users.module.css'

const EMAIL_RE = /^[^@\s]+@[^@\s]+\.[^@\s]+$/

/** 原因 5–500 字（后端 fields.reason 同文案） */
function reasonProblem(reason: string): string | null {
  const n = [...reason.trim()].length
  return n < REASON_MIN || n > REASON_MAX ? `请写清原因，${REASON_MIN} 到 ${REASON_MAX} 个字` : null
}

function resetOnClose(
  onClose: () => void,
  reset: () => void,
) {
  return () => {
    reset()
    onClose()
  }
}

// ---------------------------------------------------------------------------
// 改登录邮箱：POST v1/users/{id}/email（reauth）
// ---------------------------------------------------------------------------
export function EmailDialog({ user, open, onClose, onDone }: { user: UserDetail; open: boolean; onClose: () => void; onDone: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  // null 表示还没动过：显示当前邮箱（对话框常驻挂载，改完刷新后自然取新值）
  const [draft, setDraft] = useState<string | null>(null)
  const email = draft ?? user.email
  const setEmail = (v: string) => setDraft(v)
  const [reason, setReason] = useState('')
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)

  const close = resetOnClose(onClose, () => {
    setDraft(null)
    setReason('')
    setErrors({})
  })

  const submit = async () => {
    const local: Record<string, string> = {}
    const trimmed = email.trim()
    if (!EMAIL_RE.test(trimmed)) local.email = '邮箱格式不正确'
    const rp = reasonProblem(reason)
    if (rp) local.reason = rp
    if (Object.keys(local).length) return setErrors(local)
    setBusy(true)
    try {
      const r = await api.post(`v1/users/${encodeURIComponent(user.id)}/email`, emailSetSchema, { body: { email: trimmed, reason: reason.trim() } })
      toast(`登录邮箱已改为 ${r.email}`)
      close()
      onDone()
    } catch (e) {
      if (isApiError(e) && Object.keys(e.fields).length) setErrors({ ...e.fields })
      else fail(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <ActionModal open={open} title="改登录邮箱" busy={busy} confirm="确认" onCancel={close} onConfirm={() => void submit()}>
      <p className={css.dialogText}>改后新邮箱为未验证状态，用户用新邮箱登录；原因会写进审计。</p>
      <Input
        label="新邮箱"
        type="email"
        autoComplete="off"
        value={email}
        onChange={(e) => {
          setEmail(e.target.value)
          setErrors((f) => ({ ...f, email: '' }))
        }}
        error={errors.email || undefined}
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
// 显示名与备注：POST v1/users/{id}/note（无 reauth）
// ---------------------------------------------------------------------------
export function NoteDialog({ user, open, onClose, onDone }: { user: UserDetail; open: boolean; onClose: () => void; onDone: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const nameInit = user.display_name ?? ''
  const noteInit = user.admin_note ?? ''
  // null 表示还没动过：显示当前值
  const [nameDraft, setNameDraft] = useState<string | null>(null)
  const [noteDraft, setNoteDraft] = useState<string | null>(null)
  const displayName = nameDraft ?? nameInit
  const adminNote = noteDraft ?? noteInit
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)

  const close = resetOnClose(onClose, () => {
    setNameDraft(null)
    setNoteDraft(null)
    setErrors({})
  })

  const changed = displayName !== nameInit || adminNote !== noteInit

  const submit = async () => {
    const body: { display_name?: string; admin_note?: string } = {}
    if (displayName !== nameInit) body.display_name = displayName
    if (adminNote !== noteInit) body.admin_note = adminNote
    if (!Object.keys(body).length) return
    setBusy(true)
    try {
      await api.post(`v1/users/${encodeURIComponent(user.id)}/note`, okSchema, { body })
      toast('已保存')
      close()
      onDone()
    } catch (e) {
      fail(e, setErrors)
    } finally {
      setBusy(false)
    }
  }

  return (
    <ActionModal open={open} title="显示名与备注" busy={busy} confirm="保存" disabled={!changed} onCancel={close} onConfirm={() => void submit()}>
      <Input
        label="显示名"
        value={displayName}
        maxLength={64}
        onChange={(e) => {
          setNameDraft(e.target.value)
          setErrors((f) => ({ ...f, display_name: '' }))
        }}
        error={errors.display_name || undefined}
        hint="最多 64 字"
        data-autofocus=""
      />
      <TextArea
        label="后台备注"
        rows={5}
        value={adminNote}
        maxLength={2000}
        onChange={(e) => {
          setNoteDraft(e.target.value)
          setErrors((f) => ({ ...f, admin_note: '' }))
        }}
        error={errors.admin_note || undefined}
        hint="只有后台能看到"
      />
    </ActionModal>
  )
}

// ---------------------------------------------------------------------------
// 改邀请人：POST v1/users/{id}/referrer（reauth）
// ---------------------------------------------------------------------------
export function ReferrerDialog({ user, open, onClose, onDone }: { user: UserDetail; open: boolean; onClose: () => void; onDone: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const findUserByEmail = useFindUserByEmail()
  const [email, setEmail] = useState('')
  const [reason, setReason] = useState('')
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)

  const close = resetOnClose(onClose, () => {
    setEmail('')
    setReason('')
    setErrors({})
  })

  const submit = async () => {
    const local: Record<string, string> = {}
    const rp = reasonProblem(reason)
    if (rp) local.reason = rp
    if (Object.keys(local).length) return setErrors(local)
    setBusy(true)
    try {
      const trimmed = email.trim()
      let referrer_id: string | null = null
      if (trimmed) {
        const found = await findUserByEmail(trimmed)
        if (!found) {
          setErrors({ email: '没有这个邮箱的用户' })
          setBusy(false)
          return
        }
        referrer_id = found.id
      }
      await api.post(`v1/users/${encodeURIComponent(user.id)}/referrer`, okSchema, { body: { referrer_id, reason: reason.trim() } })
      toast('邀请人已更新')
      close()
      onDone()
    } catch (e) {
      if (isApiError(e) && Object.keys(e.fields).length) {
        const next: Record<string, string> = { ...e.fields }
        if (next.referrer_id) {
          next.email = next.referrer_id
          delete next.referrer_id
        }
        setErrors(next)
      } else fail(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <ActionModal open={open} title="改邀请人" busy={busy} confirm="确认" onCancel={close} onConfirm={() => void submit()}>
      <p className={css.dialogText}>当前邀请人：{user.referrer?.email ?? '无'}</p>
      <p className={css.dialogText}>只影响以后订单的佣金，已记的佣金不变。不能设成他自己邀请来的人。</p>
      <Input
        label="新邀请人邮箱"
        type="email"
        autoComplete="off"
        value={email}
        onChange={(e) => {
          setEmail(e.target.value)
          setErrors((f) => ({ ...f, email: '' }))
        }}
        error={errors.email || undefined}
        hint="留空表示清除邀请人"
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
// 注销账号：POST v1/users/{id}/delete（reauth）
// ---------------------------------------------------------------------------
export function DeleteDialog({ user, open, onClose, onDone }: { user: UserDetail; open: boolean; onClose: () => void; onDone: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const [reason, setReason] = useState('')
  const [confirmEmail, setConfirmEmail] = useState('')
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)

  const close = resetOnClose(onClose, () => {
    setReason('')
    setConfirmEmail('')
    setErrors({})
  })

  const canDelete = confirmEmail === user.email && reasonProblem(reason) === null

  const submit = async () => {
    const local: Record<string, string> = {}
    const rp = reasonProblem(reason)
    if (rp) local.reason = rp
    if (confirmEmail !== user.email) local.confirm = '请输入该用户的邮箱以确认'
    if (Object.keys(local).length) return setErrors(local)
    setBusy(true)
    try {
      const r = await api.post(`v1/users/${encodeURIComponent(user.id)}/delete`, userDeletedSchema, { body: { reason: reason.trim() } })
      toast(`已注销 #${r.short_id}，停掉 ${r.terminated_subscriptions} 份订阅`)
      close()
      onDone()
    } catch (e) {
      if (isApiError(e) && Object.keys(e.fields).length) setErrors({ ...e.fields })
      else fail(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <ActionModal open={open} title="注销该账号？" busy={busy} confirm="注销" tone="danger" disabled={!canDelete} onCancel={close} onConfirm={() => void submit()}>
      <p className={css.dialogText}>
        将停掉全部订阅、解绑 Telegram、全部登录失效、邮箱换成占位、清掉显示名与备注、停用邀请码；订单、账本、佣金与审计保留。<strong>不可撤销。</strong>
      </p>
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
      <Input
        label="输入该用户邮箱以确认"
        type="email"
        autoComplete="off"
        value={confirmEmail}
        onChange={(e) => {
          setConfirmEmail(e.target.value)
          setErrors((f) => ({ ...f, confirm: '' }))
        }}
        error={errors.confirm || undefined}
      />
    </ActionModal>
  )
}
