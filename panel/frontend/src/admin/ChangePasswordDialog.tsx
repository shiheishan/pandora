/**
 * [INPUT]: 依赖 react 的 useState / FormEvent，依赖 zod，依赖 ../core/api 的 isApiError，依赖 ../shell/runtime 的 useRuntime，依赖 ../ui 的 Modal / Button / Input / useToast，依赖 ./ChangePasswordDialog.module.css
 * [OUTPUT]: 对外提供 ChangePasswordDialog 与 passwordStrength
 * [POS]: admin 账户菜单「修改我的密码」（管理后台.dc.html pwd 对话框）：POST v1/me/password，成功后后端吊销全部会话（保留规则 4），这里随即清令牌回登录页
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState, type FormEvent } from 'react'
import { z } from 'zod'
import { isApiError } from '../core/api'
import { useRuntime } from '../shell/runtime'
import { Button, Input, Modal, useToast } from '../ui'
import css from './ChangePasswordDialog.module.css'

const changedSchema = z.object({ ok: z.literal(true) })

/** 设计稿的四段强度条：每 4 个字符一段，封顶 4。 */
export function passwordStrength(value: string): number {
  return Math.min(4, Math.floor(value.length / 4))
}

// 前端先按契约拦：后台新密码至少 12 位、同时含字母和数字（后端 12 位规则是待补项，先在这里守住）
function validateNew(value: string): string | null {
  if (value.length < 12) return '新密码至少 12 位'
  if (!/[a-z]/i.test(value) || !/\d/.test(value)) return '新密码必须同时包含字母和数字'
  return null
}

export function ChangePasswordDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  return open ? <ChangePasswordForm onClose={onClose} /> : null
}

function ChangePasswordForm({ onClose }: { onClose: () => void }) {
  const { api, tokens } = useRuntime()
  const toast = useToast()
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [errors, setErrors] = useState<{ current?: string; next?: string }>({})
  const [busy, setBusy] = useState(false)
  const score = passwordStrength(next)

  const submit = async (event?: FormEvent) => {
    event?.preventDefault()
    const nextError = validateNew(next)
    if (!current || nextError) {
      setErrors({ current: current ? undefined : '请输入当前密码', next: nextError ?? undefined })
      return
    }
    setBusy(true)
    setErrors({})
    try {
      await api.post('v1/me/password', changedSchema, { passwordCheck: true, body: { old_password: current, new_password: next } })
      toast('密码已更新，请用新密码重新登录')
      // 后端已在同一事务里吊销全部会话（含当前）：不再发任何请求，直接回登录页
      tokens.clear()
    } catch (err) {
      setBusy(false)
      if (!isApiError(err)) return setErrors({ next: '修改失败，请稍后重试' })
      if (err.status === 401) return setErrors({ current: err.message })
      // 新密码错误的键是 password 而不是 new_password（契约后台外壳）
      const fieldNext = err.fields.password ?? err.fields.new_password
      if (err.status === 422 || err.status === 400) {
        return setErrors({ current: err.fields.old_password, next: fieldNext ?? (err.status === 400 ? err.message : undefined) })
      }
      setErrors({ next: err.message })
    }
  }

  return (
    <Modal
      open
      onClose={onClose}
      dismissible={!busy}
      title="修改我的密码"
      actions={
        <>
          <Button size="dialog" onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={busy} onClick={() => void submit()}>
            保存
          </Button>
        </>
      }
    >
      <form className={css.form} onSubmit={submit} noValidate>
        <Input
          label="当前密码"
          type="password"
          autoComplete="current-password"
          data-autofocus=""
          value={current}
          onChange={(e) => setCurrent(e.target.value)}
          error={errors.current}
        />
        <Input
          label="新密码"
          type="password"
          autoComplete="new-password"
          value={next}
          onChange={(e) => setNext(e.target.value)}
          error={errors.next}
        />
        <div className={css.bars} aria-hidden="true">
          {[0, 1, 2, 3].map((i) => (
            <span key={i} className={i < score ? (score >= 3 ? css.barStrong : css.barWeak) : css.bar} />
          ))}
        </div>
        <p className={css.hint}>至少 12 位，同时包含字母和数字。修改后所有会话（包括当前）都会登出，需要重新登录。</p>
        <button type="submit" hidden />
      </form>
    </Modal>
  )
}
