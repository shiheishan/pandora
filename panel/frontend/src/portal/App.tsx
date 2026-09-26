/**
 * [INPUT]: 依赖 react 的 state / effect，依赖 ../core/api 的 isApiError，依赖 ../core/router 的 navigate，依赖 ../shell/runtime 的 RuntimeProvider / useRuntime / useSignedIn / AppRuntime，依赖 ./AuthPage、./Shell、./appearance、./entry-links
 * [OUTPUT]: 对外提供 App 组件
 * [POS]: 用户门户的根组件：装配运行时与主题令牌，入口页带 #/quick-login/<token> 时先消费令牌并抹掉 hash，再按登录态在 AuthPage 与 Shell 之间切换；邀请链接让登录页直接落在注册标签
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useEffect, useRef, useState } from 'react'
import { isApiError } from '../core/api'
import { navigate } from '../core/router'
import { RuntimeProvider, useRuntime, useSignedIn, type AppRuntime } from '../shell/runtime'
import { useAppearanceTheme } from './appearance'
import { AuthPage, consumeQuickLogin } from './AuthPage'
import { quickLoginTokenFromHash } from './entry-links'
import { Shell } from './Shell'

export function App({ runtime, invite }: { runtime: AppRuntime; invite: string | null }) {
  return (
    <RuntimeProvider runtime={runtime}>
      <Root invite={invite} />
    </RuntimeProvider>
  )
}

type QuickState = { phase: 'idle' } | { phase: 'pending' } | { phase: 'failed'; message: string }

function Root({ invite }: { invite: string | null }) {
  useAppearanceTheme()
  const quick = useQuickLoginFromUrl()
  const signedIn = useSignedIn()
  if (quick.phase === 'pending') return null
  if (signedIn) return <Shell />
  if (quick.phase === 'failed') return <AuthPage initialTab="quick" notice={quick.message} />
  return <AuthPage initialTab={invite ? 'reg' : 'login'} invite={invite} />
}

// 快捷登录链接 <门户根>/#/quick-login/<token>：加载即消费，成功失败都先把令牌从地址栏抹掉
function useQuickLoginFromUrl(): QuickState {
  const { api, tokens } = useRuntime()
  const [token] = useState(() => quickLoginTokenFromHash(window.location.hash))
  const [state, setState] = useState<QuickState>(token ? { phase: 'pending' } : { phase: 'idle' })
  const started = useRef(false)

  useEffect(() => {
    if (!token || started.current) return
    started.current = true
    navigate('/overview', { replace: true })
    consumeQuickLogin(api, tokens, token).then(
      () => setState({ phase: 'idle' }),
      (err: unknown) => setState({ phase: 'failed', message: isApiError(err) ? err.message : '快捷登录失败，请稍后重试' }),
    )
  }, [token, api, tokens])

  return state
}
