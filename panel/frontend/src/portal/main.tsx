/**
 * [INPUT]: 依赖 react-dom/client 的 createRoot，依赖 ./App 的 App
 * [OUTPUT]: 无导出；把用户门户挂到 index.html 的 #root
 * [POS]: portal 入口的启动脚本，由 src/portal/index.html 以 module script 引用
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './App'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
