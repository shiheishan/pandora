import { useEffect, useRef, useState } from 'react'
import { z } from 'zod'
import { isApiError } from '../core/api'
import { navigate } from '../core/router'
import { RuntimeProvider, useRuntime, useSignedIn, type AppRuntime } from '../shell/runtime'
import { useAppearanceTheme } from './appearance'
import { AuthPage, consumeQuickLogin } from './AuthPage'
import { mailLoginTokenFromHash, quickLoginTokenFromHash } from './entry-links'
import { consumeMailLogin } from './MailLinkLogin'
import { Shell } from './Shell'

export function App({ runtime, invite }: { runtime: AppRuntime; invite: string | null }) {
  return (
    <RuntimeProvider runtime={runtime}>
      <Root invite={invite} />
    </RuntimeProvider>
  )
}

type UrlLoginState = { phase: 'idle' } | { phase: 'pending' } | { phase: 'failed'; message: string }

function Root({ invite }: { invite: string | null }) {
  useAppearanceTheme()
  const { api } = useRuntime()
  const quick = useQuickLoginFromUrl()
  const mail = useMailLoginFromUrl()
  const signedIn = useSignedIn()
  const inviteVisitStarted = useRef(false)

  // 访问量按码计数，后端按 IP 去重，失败不影响页面
  useEffect(() => {
    if (!invite || inviteVisitStarted.current) return
    inviteVisitStarted.current = true
    api.post('v1/invite/visit', z.object({ ok: z.literal(true) }), { auth: false, body: { code: invite } }).catch(() => {})
  }, [invite, api])

  if (quick.phase === 'pending' || mail.phase === 'pending') return null
  if (signedIn) return <Shell />
  if (mail.phase === 'failed') return <AuthPage initialTab="mail" notice={mail.message} />
  if (quick.phase === 'failed') return <AuthPage initialTab="quick" notice={quick.message} />
  return <AuthPage initialTab={invite ? 'reg' : 'login'} invite={invite} />
}

// 快捷登录链接 <门户根>/#/quick-login/<token>：加载即消费，成功失败都先把令牌从地址栏抹掉
function useQuickLoginFromUrl(): UrlLoginState {
  const { api, tokens } = useRuntime()
  const [token] = useState(() => quickLoginTokenFromHash(window.location.hash))
  const [state, setState] = useState<UrlLoginState>(token ? { phase: 'pending' } : { phase: 'idle' })
  const started = useRef(false)

  useEffect(() => {
    if (!token || started.current) return
    started.current = true
    navigate('/subs', { replace: true })
    consumeQuickLogin(api, tokens, token).then(
      () => setState({ phase: 'idle' }),
      (err: unknown) => setState({ phase: 'failed', message: isApiError(err) ? err.message : '快捷登录失败，请稍后重试' }),
    )
  }, [token, api, tokens])

  return state
}

// 邮件登录链接 <门户根>/#/mail-login/<token>：加载即消费，成功失败都先把令牌从地址栏抹掉
function useMailLoginFromUrl(): UrlLoginState {
  const { api, tokens } = useRuntime()
  const [token] = useState(() => mailLoginTokenFromHash(window.location.hash))
  const [state, setState] = useState<UrlLoginState>(token ? { phase: 'pending' } : { phase: 'idle' })
  const started = useRef(false)

  useEffect(() => {
    if (!token || started.current) return
    started.current = true
    navigate('/subs', { replace: true })
    consumeMailLogin(api, tokens, token).then(
      () => setState({ phase: 'idle' }),
      (err: unknown) =>
        setState({ phase: 'failed', message: isApiError(err) ? err.message : '邮件登录失败，请重新获取登录链接' }),
    )
  }, [token, api, tokens])

  return state
}
