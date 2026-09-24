/**
 * [INPUT]: 依赖 react 的 ReactNode，依赖 ../../../core/api 的 isApiError，依赖 ../../../ui 的 Button / Empty / Skeleton，依赖 ./marketing.module.css
 * [OUTPUT]: 对外提供 StatStrip（四格统计）、Pager（上一页 / 下一页）、QueryView（加载 / 错误 / 空三态）
 * [POS]: admin/screens/marketing 的局部小件，只在本模块用；别的模块也要时报告协调会话再决定是否提升到 ui/
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { ReactNode } from 'react'
import { isApiError } from '../../../core/api'
import { Button, Empty, Skeleton } from '../../../ui'
import css from './marketing.module.css'

export function StatStrip({ items, label }: { items: ReadonlyArray<{ label: string; value: string }> | undefined; label: string }) {
  return (
    <div className={css.stats} role="group" aria-label={label}>
      {(items ?? [0, 1, 2, 3].map(() => null)).map((item, i) => (
        <div key={item?.label ?? i} className={css.stat}>
          {item ? (
            <>
              <div className={css.statLabel}>{item.label}</div>
              <div className={css.statValue}>{item.value}</div>
            </>
          ) : (
            <>
              <Skeleton width={56} height={12} />
              <Skeleton width={96} height={22} className={css.statValue} />
            </>
          )}
        </div>
      ))}
    </div>
  )
}

export function Pager({ total, limit, offset, onChange }: { total: number; limit: number; offset: number; onChange: (offset: number) => void }) {
  if (total <= limit) return null
  const pages = Math.ceil(total / limit)
  const current = Math.floor(offset / limit) + 1
  return (
    <div className={css.pager}>
      <span>
        第 {current} / {pages} 页 · 共 {total} 条
      </span>
      <Button size="xs" disabled={offset === 0} onClick={() => onChange(Math.max(0, offset - limit))}>
        上一页
      </Button>
      <Button size="xs" disabled={offset + limit >= total} onClick={() => onChange(offset + limit)}>
        下一页
      </Button>
    </div>
  )
}

interface QueryLike<T> {
  data: T | undefined
  isPending: boolean
  isError: boolean
  error: unknown
  refetch: () => unknown
}

/**
 * 列表与卡片的三态：加载给骨架，接口 404 按「无权限或不存在」，其它错误给重试，空数据给 empty。
 * isEmpty 不传时按数组长度判断。
 */
export function QueryView<T>({
  query,
  empty,
  isEmpty,
  rows = 3,
  children,
}: {
  query: QueryLike<T>
  empty: ReactNode
  isEmpty?: (data: T) => boolean
  rows?: number
  children: (data: T) => ReactNode
}) {
  if (query.isPending) {
    return (
      <div className={css.stack} role="status" aria-label="加载中">
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
