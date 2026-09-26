/**
 * [INPUT]: 依赖 react-dom/client 的 createRoot，依赖 ../styles/index.css 的全局样式，依赖 ../ui 的 ToastProvider，依赖 ./Showcase 的 Showcase
 * [OUTPUT]: 无导出；把演示页挂到 #root
 * [POS]: showcase 入口的启动脚本；vite --mode showcase 只允许 dev，构建时 vite.config.ts 直接拒绝，所以它永远不进产物
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import '../styles/index.css'
import { ToastProvider } from '../ui'
import { Showcase } from './Showcase'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ToastProvider>
      <Showcase />
    </ToastProvider>
  </StrictMode>,
)
