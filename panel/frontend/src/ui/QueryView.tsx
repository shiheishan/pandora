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
    // 接口 404 是正常结果（地址里的 id 已被删除或从未存在），不当作报错
    if (isApiError(query.error) && query.error.status === 404) return <Empty bare title="不存在或已删除" description="要查看的数据不存在，或已被删除。" />
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
