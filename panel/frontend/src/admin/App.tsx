import { RuntimeProvider, useSession, type AppRuntime } from '../shell/runtime'
import { SessionPending } from '../shell/SessionGate'
import { LoginPage } from './LoginPage'
import { ReauthDialog } from './ReauthDialog'
import type { ReauthController } from './reauth'
import { ShellLoader } from './ShellLoader'

export function App({ runtime, reauth }: { runtime: AppRuntime; reauth: ReauthController }) {
  return (
    <RuntimeProvider runtime={runtime}>
      <Root />
      <ReauthDialog controller={reauth} />
    </RuntimeProvider>
  )
}

function Root() {
  const session = useSession()
  if (session === 'signed-in') return <ShellLoader />
  if (session === 'signed-out') return <LoginPage />
  return <SessionPending state={session} />
}
