/**
 * [INPUT]: 依赖 react 的 Component / Suspense，依赖 ../ui 的 Button / Empty / Skeleton，依赖 ./ScreenFrame.module.css
 * [OUTPUT]: 对外提供 ScreenFrame、ScreenFallback、NotFoundScreen、isChunkLoadError
 * [POS]: shell 的页面容器：两个外框的内容区都用它包住登记表里的懒加载页面——Suspense 以骨架兜底，错误边界把单个页面的崩溃关在内容区里，外框照常可用；另给出「无权限或不存在」的整页状态
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { Component, Suspense, type ReactNode } from 'react'
import { Button, Empty, Skeleton } from '../ui'
import css from './ScreenFrame.module.css'

// ---------------------------------------------------------------------------
// 懒加载的页面块在发版后可能已被新哈希替换（旧标签页里点进一个没加载过的模块），
// React.lazy 会缓存这次失败，重置边界无济于事，只能整页刷新拿新入口。
// ---------------------------------------------------------------------------
export function isChunkLoadError(error: unknown): boolean {
  if (!(error instanceof Error)) return false
  return /dynamically imported module|Importing a module script failed|error loading dynamically imported module/i.test(error.message)
}

/** 页面块加载中的骨架：一行工具栏 + 一块内容，300ms 内不显现（ui/Skeleton 自带延迟）。 */
export function ScreenFallback() {
  return (
    <div className={css.fallback} role="status" aria-label="加载中">
      <Skeleton width={220} height={28} />
      <Skeleton height={240} radius="var(--radius-card)" />
    </div>
  )
}

/** 缺权限的地址与接口 404 统一显示成这一页，不当作报错。 */
export function NotFoundScreen() {
  return <Empty title="无权限或不存在" description="这个页面不存在，或当前账号没有查看它的权限。" />
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
