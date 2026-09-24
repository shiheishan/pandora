/**
 * [INPUT]: 依赖 react-dom/client 的 createRoot，依赖 ./App，依赖 ../shell/runtime 的 createAppRuntime，依赖 ./entry-links 的 takeInviteFromUrl，依赖 ../styles/index.css 的全局令牌与基础样式
 * [OUTPUT]: 无导出；把用户门户挂到 index.html 的 #root
 * [POS]: portal 入口的启动脚本，由 src/portal/index.html 以 module script 引用；建本入口唯一一份运行时（门户没有 reauth），并在渲染前取走邀请链接的查询串
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { createAppRuntime } from '../shell/runtime'
import '../styles/index.css'
import { App } from './App'
import { takeInviteFromUrl } from './entry-links'

const runtime = createAppRuntime('portal')
const invite = takeInviteFromUrl()

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App runtime={runtime} invite={invite} />
  </StrictMode>,
)
