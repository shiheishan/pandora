import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { createAppRuntime } from '../shell/runtime'
import '../styles/index.css'
import { App } from './App'
import { createReauthController } from './reauth'

const reauth = createReauthController()
const runtime = createAppRuntime('admin', { requestReauth: () => reauth.request() })

// 仅开发期：把 api 客户端挂到 window，便于在浏览器里对假后端验证 reauth 重放；构建时整段被裁掉
if (import.meta.env.DEV) Object.assign(window, { __pandora: runtime })

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App runtime={runtime} reauth={reauth} />
  </StrictMode>,
)
