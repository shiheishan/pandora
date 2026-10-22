import { Component, Suspense, type ReactNode } from 'react'
import { Button, Empty, Skeleton } from '../ui'
import { isChunkLoadError } from './chunk-error'
import css from './ScreenFrame.module.css'

/** 页面块加载中的骨架：一行工具栏 + 一块内容，300ms 内不显现（ui/Skeleton 自带延迟）。 */
function ScreenFallback() {
  return (
    <div className={css.fallback} role="status" aria-label="加载中">
      <Skeleton width={220} height={28} />
      <Skeleton height={240} radius="var(--radius-card)" />
    </div>
  )
}

interface BoundaryProps {
  /** 路由变化即重置：换到别的页面时不再显示上一页的错误 */
  resetKey: string
  children: ReactNode
}

interface BoundaryState {
  error: unknown
  key: string
}

class ScreenBoundary extends Component<BoundaryProps, BoundaryState> {
  state: BoundaryState = { error: null, key: this.props.resetKey }

  // 不实现 componentDidCatch：React 在开发期已把错误与组件栈打到控制台，生产环境没有上报通道
  static getDerivedStateFromError(error: unknown): Partial<BoundaryState> {
    return { error: error ?? new Error('unknown error') }
  }

  static getDerivedStateFromProps(props: BoundaryProps, state: BoundaryState): Partial<BoundaryState> | null {
    return props.resetKey === state.key ? null : { error: null, key: props.resetKey }
  }

  render() {
    const { error } = this.state
    if (error === null) return this.props.children
    const stale = isChunkLoadError(error)
    return (
      <Empty
        title="页面出错了"
        description={stale ? '页面资源已更新，刷新后即可继续。' : '这个页面遇到意外错误，其它页面不受影响。可以重试，或先去别的页面。'}
        action={
          <Button variant="secondary" size="sm" onClick={() => (stale ? window.location.reload() : this.setState({ error: null }))}>
            {stale ? '刷新页面' : '重试'}
          </Button>
        }
      />
    )
  }
}

export function ScreenFrame({ resetKey, children }: BoundaryProps) {
  return (
    <ScreenBoundary resetKey={resetKey}>
      <Suspense fallback={<ScreenFallback />}>{children}</Suspense>
    </ScreenBoundary>
  )
}
