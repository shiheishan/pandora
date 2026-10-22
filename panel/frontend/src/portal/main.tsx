import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { createAppRuntime } from '../shell/runtime'
import '../styles/index.css'
import '../styles/display-fonts.css'
import './home.css'
import { App } from './App'
import { mailLoginTokenFromHash, quickLoginTokenFromHash, takeInviteFromUrl } from './entry-links'
import { preloadShell } from './ShellLoader'

// 地址带着快捷 / 邮件登录令牌时不开机恢复旧会话：免得旧会话的 refresh 与这次登录赛跑，最后令牌与 cookie 不是同一个人。
// 这里只读地址判断，消费令牌仍在 App.tsx
const urlLogin =
  quickLoginTokenFromHash(window.location.hash) !== null || mailLoginTokenFromHash(window.location.hash) !== null
const runtime = createAppRuntime('portal', { restoreSession: !urlLogin })
const invite = takeInviteFromUrl()

// 有登录标志的冷启动（多半刷新后还登录着）并行取 Shell 块，不等开机恢复登录与根组件首次渲染
if (runtime.tokens.hasSessionHint() && !urlLogin) preloadShell().catch(() => undefined) // 失败由 ShellLoader 的边界显示

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App runtime={runtime} invite={invite} />
  </StrictMode>,
)
