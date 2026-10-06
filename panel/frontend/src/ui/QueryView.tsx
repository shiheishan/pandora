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
