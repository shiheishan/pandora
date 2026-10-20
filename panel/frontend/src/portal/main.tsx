import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { createAppRuntime } from '../shell/runtime'
import '../styles/index.css'
import '../styles/display-fonts.css'
import './home.css'
import { App } from './App'
import { takeInviteFromUrl } from './entry-links'
import { preloadShell } from './ShellLoader'

const runtime = createAppRuntime('portal')
const invite = takeInviteFromUrl()

// 已登录冷启动：外框分包与入口并行下载，不等 React 挂载后才发请求
if (runtime.tokens.get() !== null) preloadShell().catch(() => undefined) // 失败由 ShellLoader 的边界显示

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App runtime={runtime} invite={invite} />
  </StrictMode>,
)
