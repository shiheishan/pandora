import { createShellLoader } from '../shell/ShellLoader'

export const { ShellLoader, preloadShell } = createShellLoader(() => import('./Shell'), {
  stale: '网站已更新，请刷新页面',
  failed: '页面加载失败',
})
