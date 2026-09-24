/**
 * [INPUT]: 依赖 react 的 useState / useSyncExternalStore / FormEvent，依赖 @tanstack/react-query 的 useQueryClient，依赖 ../core/api 的 isApiError，依赖 ../shell/runtime 的 useApi，依赖 ./reauth 的 ReauthController，依赖 ./me 的 ME_QUERY_KEY，依赖 ../ui 的 Modal / Button / Input
 * [OUTPUT]: 对外提供 ReauthDialog
 * [POS]: admin 的「敏感操作 · 需要重新认证」对话框（管理后台.dc.html ask({reauth:true})）：由 api.ts 的 reauth_required 经 ReauthController 唤起，POST v1/auth/reauth 换新令牌后 resolve(true)，api.ts 随即用原幂等键重放被拦下的请求
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useQueryClient } from '@tanstack/react-query'
import { useState, useSyncExternalStore, type FormEvent } from 'react'
import { isApiError } from '../core/api'
import { useApi } from '../shell/runtime'
import { Button, Input, Modal } from '../ui'
import { ME_QUERY_KEY } from './me'
import css from './ReauthDialog.module.css'
import type { ReauthController } from './reauth'

// 设计是「先弹框后执行」，契约 1.6 改为「先请求、按需弹框」：弹出时原操作已经被拦下，
// 文案因此说「验证后继续」。口令错（401）在框内显示，不触发全局登出。
export function ReauthDialog({ controller }: { controller: ReauthController }) {
  const open = useSyncExternalStore(controller.subscribe, controller.isPending, () => false)
  return open ? <ReauthForm controller={controller} /> : null
}

function ReauthForm({ controller }: { controller: ReauthController }) {
  const api = useApi()
  const queryClient = useQueryClient()
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const submit = async (event?: FormEvent) => {
    event?.preventDefault()
    if (!password) {
      setError('请输入密码后继续')
      return
    }
    setBusy(true)
    setError(null)
    try {
      await api.reauth(password)
      void queryClient.invalidateQueries({ queryKey: ME_QUERY_KEY })
      controller.resolve(true)
    } catch (err) {
      setError(isApiError(err) ? (err.fields.password ?? err.message) : '验证失败，请稍后重试')
      setBusy(false)
    }
  }

  return (
    <Modal
      open
      onClose={() => controller.resolve(false)}
      dismissible={!busy}
      eyebrow="敏感操作 · 需要重新认证"
      title="验证身份后继续"
      actions={
        <>
          <Button size="dialog" onClick={() => controller.resolve(false)} disabled={busy}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={busy} onClick={() => void submit()}>
            验证并继续
          </Button>
        </>
      }
    >
      <form onSubmit={submit} noValidate>
        <p className={css.lead}>这项操作需要确认是你本人。输入当前登录密码后，刚才的操作会自动继续执行。</p>
        <Input
          label="当前登录密码"
          type="password"
          autoComplete="current-password"
          data-autofocus=""
          value={password}
          onChange={(e) => {
            setPassword(e.target.value)
            setError(null)
          }}
          error={error ?? undefined}
        />
      </form>
    </Modal>
  )
}
