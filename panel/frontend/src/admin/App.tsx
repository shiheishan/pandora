/**
 * [INPUT]: 依赖 ../shell/runtime 的 RuntimeProvider / useSignedIn / AppRuntime，依赖 ./LoginPage、./Shell、./ReauthDialog、./reauth
 * [OUTPUT]: 对外提供 App 组件
 * [POS]: 管理后台的根组件：装配运行时，按登录态在登录页与外框之间切换；ReauthDialog 常驻，任何页面的请求被 reauth_required 拦下都由它接住
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { RuntimeProvider, useSignedIn, type AppRuntime } from '../shell/runtime'
import { LoginPage } from './LoginPage'
import { ReauthDialog } from './ReauthDialog'
import type { ReauthController } from './reauth'
import { Shell } from './Shell'

export function App({ runtime, reauth }: { runtime: AppRuntime; reauth: ReauthController }) {
  return (
    <RuntimeProvider runtime={runtime}>
      <Root />
      <ReauthDialog controller={reauth} />
    </RuntimeProvider>
  )
}

function Root() {
  return useSignedIn() ? <Shell /> : <LoginPage />
}
