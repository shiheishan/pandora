import { useCallback } from 'react'
import { navigate, useHashLocation } from '../../../core/router'
import { useAdminMe } from '../../me'
import type { AdminScreenProps } from '../index'
import { useCan } from '../../actions'
import { Detail } from './Detail'
import { isFilter, queueParams, type QueueFilter } from './model'
import { Queue } from './Queue'
import css from './Tickets.module.css'

export default function Tickets({ rest }: AdminScreenProps) {
  const location = useHashLocation()
  const me = useAdminMe()
  const can = useCan()
  const rawFilter = location.query.get('f')
  const filter: QueueFilter = isFilter(rawFilter) ? rawFilter : 'active'
  const q = location.query.get('q') ?? ''
  const selected = rest[0] ?? null

  // 筛选与搜索回写地址（replace，不留历史），保留当前选中的工单
  const setQuery = useCallback(
    (next: { f?: QueueFilter; q?: string }) => {
      const f = next.f ?? filter
      navigate(selected ? `/tickets/${encodeURIComponent(selected)}` : '/tickets', {
        replace: true,
        query: { f: f === 'active' ? undefined : f, q: next.q ?? q },
      })
    },
    [filter, q, selected],
  )

  return (
    <div className={css.page}>
      <Queue
        filter={filter}
        query={q}
        params={queueParams(filter, me.data?.user_id, q)}
        selected={selected}
        canWrite={can('ops.ticket.write')}
        onFilter={(f) => setQuery({ f })}
        onQuery={(text) => setQuery({ q: text })}
      />
      <Detail id={selected} />
    </div>
  )
}
