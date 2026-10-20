import { Component, lazy, Suspense, type ComponentType, type ReactElement, type ReactNode } from 'react'
import { isChunkLoadError } from './chunk-error'
import css from './ShellLoader.module.css'

export interface ShellLoaderText {
  /** 新版本已发布、旧分包取不到时的提示 */
  stale: string
  /** 其它加载错误的提示 */
  failed: string
}

/**
 * 外框懒加载：登录首屏只下载登录页，外框（各页面、导航、实时连接）登录后才取。
 * preloadShell 只调一次 load、缓存那个 promise，lazy 复用它：已登录冷启动时入口可以先调它，与 React 挂载并行下载。
 * 分包取不到（多半是发了新版本、旧文件已删）时边界提示刷新，不白屏。
 */
export function createShellLoader(
  load: () => Promise<{ Shell: ComponentType }>,
  text: ShellLoaderText,
): { ShellLoader: () => ReactElement; preloadShell: () => Promise<unknown> } {
  let shellPromise: Promise<{ Shell: ComponentType }> | null = null

  function preloadShell(): Promise<{ Shell: ComponentType }> {
    if (!shellPromise) {
      shellPromise = load()
    }
    return shellPromise
  }

  const Shell = lazy(() => preloadShell().then((m) => ({ default: m.Shell })))

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
          <div className={css.message}>{stale ? text.stale : text.failed}</div>
          <button type="button" className={css.reload} onClick={() => window.location.reload()}>
            刷新页面
          </button>
        </div>
      )
    }
  }

  function ShellLoader() {
    return (
      <ShellBoundary>
        <Suspense fallback={<ShellFallback />}>
          <Shell />
        </Suspense>
      </ShellBoundary>
    )
  }

  return { ShellLoader, preloadShell }
}
