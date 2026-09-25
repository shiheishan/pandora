/**
 * [INPUT]: 依赖 react 的 ReactNode，依赖 ../core/api 的 isApiError，依赖 ./Button、./Empty、./Skeleton，依赖 ./QueryView.module.css
 * [OUTPUT]: 对外提供 QueryView、QueryViewProps、QueryLike
 * [POS]: ui 的查询三态容器：把一个 react-query 结果渲染成加载（骨架行）/ 错误（接口 404 按「无权限或不存在」、其它给重试）/ 空 / 正文之一，统一第 10.4 节「列表与卡片都有加载、空、错误三种状态」。只认结果对象的形状，不依赖 react-query 本身
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { ReactNode } from 'react'
import { isApiError } from '../core/api'
import { Button } from './Button'
import { Empty } from './Empty'
import { Skeleton } from './Skeleton'
import css from './QueryView.module.css'

/** useQuery 结果里用到的那几项 */
export interface QueryLike<T> {
  data: T | undefined
  isPending: boolean
  isError: boolean
  error: unknown
  refetch: () => unknown
}

export interface QueryViewProps<T> {
  query: QueryLike<T>
  /** 数据为空时渲染：一句现状 + 一句能做什么 */
  empty: ReactNode
  /** 判断空；不传时数组按长度，其它一律非空 */
  isEmpty?: (data: T) => boolean
  /** 加载骨架行数，默认 3 */
  rows?: number
  children: (data: T) => ReactNode
}

export function QueryView<T>({ query, empty, isEmpty, rows = 3, children }: QueryViewProps<T>) {
  if (query.isPending) {
    return (
      <div className={css.loading} role="status" aria-label="加载中">
        {Array.from({ length: rows }, (_, i) => (
          <Skeleton key={i} height={40} />
        ))}
      </div>
    )
  }
  if (query.isError || query.data === undefined) {
    if (isApiError(query.error) && query.error.status === 404) return <Empty bare title="无权限或不存在" description="当前账号没有查看这部分数据的权限。" />
    return (
      <Empty
        bare
        title="读取失败"
        description={isApiError(query.error) ? query.error.message : '暂时连不上服务器。'}
        action={
          <Button size="sm" onClick={() => void query.refetch()}>
            重试
          </Button>
        }
      />
    )
  }
  const data = query.data
  const blank = isEmpty ? isEmpty(data) : Array.isArray(data) && data.length === 0
  return <>{blank ? empty : children(data)}</>
}
