import { useState, type FormEvent } from 'react'
import { PasswordInput, useToast } from '../../../ui'
import { Btn, Sheet } from '../../kit'
import { usePortalMe } from '../../queries'
import css from './Account.module.css'
import { useChangePassword } from './api'
import { passwordErrors, validateNewPassword, type PasswordErrors } from './model'

export function PasswordSheet({ open, onClose }: { open: boolean; onClose: () => void }) {
  const toast = useToast()
  const email = usePortalMe().data?.email ?? ''
  const change = useChangePassword()
  const [oldPw, setOldPw] = useState('')
  const [newPw, setNewPw] = useState('')
  const [errors, setErrors] = useState<PasswordErrors>({})

  function close() {
    setOldPw('')
    setNewPw('')
    setErrors({})
    onClose()
  }

  function submit(event: FormEvent) {
    event.preventDefault()
    const local: PasswordErrors = {}
    if (!oldPw) local.old = '请输入当前密码'
    const rule = validateNewPassword(newPw)
    if (rule) local.next = rule
    else if (newPw === oldPw) local.next = '新密码不能与当前密码相同'
    setErrors(local)
    if (local.old || local.next) return

    change.mutate(
      { old_password: oldPw, new_password: newPw },
      {
        onSuccess: () => {
          close()
          toast('密码已更新，其他设备已下线')
        },
        onError: (e) => setErrors(passwordErrors(e)),
      },
    )
  }

  return (
    <Sheet
      open={open}
      onClose={close}
      title="修改密码"
      actions={
        <>
          <Btn tone="soft" onClick={close}>
            取消
          </Btn>
          <Btn tone="hot" type="submit" form="pw-form" busy={change.isPending}>
            更新密码
          </Btn>
        </>
      }
    >
      <p className={css.sheetLead}>改好后，除了这台设备，其他设备都会下线，需要重新登录。</p>
      <form id="pw-form" className={css.form} onSubmit={submit} noValidate>
        {/* 给密码管理器认账号用，不显示 */}
        <input type="email" name="username" autoComplete="username" value={email} readOnly hidden />
        <PasswordInput
          label="当前密码"
          autoComplete="current-password"
          value={oldPw}
          onChange={(e) => setOldPw(e.target.value)}
          error={errors.old}
        />
        <PasswordInput
          label="新密码"
          autoComplete="new-password"
          placeholder="至少 8 位，含字母和数字"
          value={newPw}
          onChange={(e) => setNewPw(e.target.value)}
          error={errors.next}
        />
        {errors.form && (
          <p className={css.formError} role="alert">
            {errors.form}
          </p>
        )}
      </form>
    </Sheet>
  )
}
