/**
 * [INPUT]: 依赖 react 的 useCallback，依赖 ../../../core/router 的 navigate / useHashLocation，依赖 ../../me 的 useAdminMe，依赖 ../index 的 AdminScreenProps，依赖 ./actions 的 useCan，依赖 ./model 的 isFilter / queueParams，依赖 ./Queue、./Detail，依赖 ./Tickets.module.css
 * [OUTPUT]: 默认导出 Tickets 页面组件（登记表 React.lazy 的目标）
 * [POS]: admin/screens/tickets 的入口：工单（后台-02），左队列右详情的双栏。状态全在地址上——#/tickets/<工单 id>?f=<筛选>&q=<搜索>，刷新、分享、前进后退都落在同一处；读权限 ops.ticket.read 由外框先判，写权限 ops.ticket.write 决定能否处理
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useCallback } from 'react'
import { navigate, useHashLocation } from '../../../core/router'
import { useAdminMe } from '../../me'
import type { AdminScreenProps } from '../index'
import { useCan } from './actions'
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
