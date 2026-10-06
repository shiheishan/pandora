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
