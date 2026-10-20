import { createShellLoader } from '../shell/ShellLoader'

export const { ShellLoader, preloadShell } = createShellLoader(() => import('./Shell'), {
  stale: '后台已更新，请刷新页面',
  failed: '后台加载失败',
})
