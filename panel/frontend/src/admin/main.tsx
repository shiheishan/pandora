import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { createAppRuntime } from '../shell/runtime'
import '../styles/index.css'
import { App } from './App'
import { createReauthController } from './reauth'
import { preloadShell } from './ShellLoader'

const reauth = createReauthController()
const runtime = createAppRuntime('admin', { requestReauth: () => reauth.request() })

// 有登录标志的冷启动（多半刷新后还登录着）并行取 Shell 块，不等开机恢复登录与根组件首次渲染
if (runtime.tokens.hasSessionHint()) preloadShell().catch(() => undefined) // 失败由 ShellLoader 的边界显示

// 仅开发期：把 api 客户端挂到 window，便于在浏览器里对假后端验证 reauth 重放；构建时整段被裁掉
if (import.meta.env.DEV) Object.assign(window, { __pandora: runtime })

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App runtime={runtime} reauth={reauth} />
  </StrictMode>,
)
