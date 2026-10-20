import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { z } from 'zod'
import { isApiError } from '../core/api'
import { navigate, parseHash, useHashLocation } from '../core/router'
import { RuntimeProvider, useRuntime, useSignedIn, type AppRuntime } from '../shell/runtime'
import { useAppearanceTheme } from './appearance'
import { AuthPage, consumeQuickLogin } from './AuthPage'
import { authTabFromPath, isHomePath, LOGIN_PATH, mailLoginTokenFromHash, quickLoginTokenFromHash } from './entry-links'
import { hasServerHome, setHomeHidden } from './home'
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

  const { path } = useHashLocation()
  const [serverHome] = useState(() => hasServerHome())
  const hashTab = authTabFromPath(path)
  // 未登录、地址没有页面、不是邀请或链接登录、服务端给了首页片段：#root 不渲染，露出首页
  const showHome = serverHome && !signedIn && !invite && quick.phase === 'idle' && mail.phase === 'idle' && isHomePath(path)

  useLayoutEffect(() => {
    setHomeHidden(!showHome)
  }, [showHome])

  // 退出登录（含令牌失效被清掉）时地址停在空地址上就换成登录页，免得一退出就露出首页；
  // 停在别的页面地址上不动：登录页照常显示，重新登录后回到原来要去的页面（邮件里的深链）
  const wasSignedIn = useRef(signedIn)
  useEffect(() => {
    if (wasSignedIn.current && !signedIn && isHomePath(parseHash(window.location.hash).path)) {
      navigate(LOGIN_PATH, { replace: true })
    }
    wasSignedIn.current = signedIn
  }, [signedIn])

  if (quick.phase === 'pending' || mail.phase === 'pending') return null
  if (signedIn) return <Shell />
  if (mail.phase === 'failed') return <AuthPage initialTab="mail" notice={mail.message} />
  if (quick.phase === 'failed') return <AuthPage initialTab="quick" notice={quick.message} />
  if (showHome) return null
  // key 随地址里的页签变：#/login 与 #/register 之间切换时重新挂载、换到对应页签
  return <AuthPage key={hashTab ?? 'login'} initialTab={invite ? 'reg' : (hashTab ?? 'login')} invite={invite} />
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
