/**
 * [INPUT]: 依赖 react-dom/client 的 createRoot，依赖 ./App，依赖 ../shell/runtime 的 createAppRuntime，依赖 ./reauth 的 createReauthController，依赖 ../styles/index.css 的全局令牌与基础样式
 * [OUTPUT]: 无导出；把管理后台挂到 index.html 的 #root
 * [POS]: admin 入口的启动脚本，由 src/admin/index.html 以 module script 引用；在这里建本入口唯一一份运行时，并把 reauth 对话框接到 api 客户端上
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
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
