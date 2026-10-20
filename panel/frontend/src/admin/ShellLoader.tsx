import { Component, lazy, Suspense, type ReactNode } from 'react'
import { isChunkLoadError } from '../shell/chunk-error'
import css from './ShellLoader.module.css'

let shellPromise: Promise<unknown> | null = null

export function preloadShell(): Promise<unknown> {
  if (!shellPromise) {
    shellPromise = import('./Shell')
  }
  return shellPromise
}

const Shell = lazy(() =>
  preloadShell()
    .then(() => import('./Shell'))
    .then((m) => ({ default: m.Shell })),
)

function ShellFallback() {
  return <div className={css.page} role="status" aria-label="加载中" />
}

interface ShellBoundaryProps {
  children: ReactNode
}

interface ShellBoundaryState {
  error: unknown
}

class ShellBoundary extends Component<ShellBoundaryProps, ShellBoundaryState> {
  state: ShellBoundaryState = { error: null }

  // 不实现 componentDidCatch：开发期 React 会把错误输出到控制台，生产环境暂无独立上报通道
  static getDerivedStateFromError(error: unknown): Partial<ShellBoundaryState> {
    return { error: error ?? new Error('unknown error') }
  }

  render() {
    const { error } = this.state
    if (error === null) return this.props.children

    const stale = isChunkLoadError(error)
    return (
      <div className={css.page} role="alert">
        <div className={css.message}>{stale ? '后台已更新，请刷新页面' : '后台加载失败'}</div>
        <button type="button" className={css.reload} onClick={() => window.location.reload()}>
          刷新页面
        </button>
      </div>
    )
  }
}

export function ShellLoader() {
  return (
    <ShellBoundary>
      <Suspense fallback={<ShellFallback />}>
        <Shell />
      </Suspense>
    </ShellBoundary>
  )
}
