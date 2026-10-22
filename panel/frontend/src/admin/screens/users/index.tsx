import { useCallback } from 'react'
import { useNow } from '../../kit'
import { href, navigate, useHashLocation } from '../../../core/router'
import type { AdminScreenProps } from '../index'
import { BulkTab } from './BulkTab'
import { GroupsTab } from './GroupsTab'
import { isStatusFilter } from './model'
import { isDetailTab, UserDetail, type DetailTab } from './UserDetail'
import { UserList, type ListState } from './UserList'

export default function Users({ tab, rest }: AdminScreenProps) {
  const now = useNow()
  if (tab === 'groups') return <GroupsTab />
  if (tab === 'bulk') return <BulkTab now={now} />
  return <ListTab rest={rest} now={now} />
}

function ListTab({ rest, now }: { rest: string[]; now: Date }) {
  const location = useHashLocation()
  const f = location.query.get('f')
  const state: ListState = {
    filter: isStatusFilter(f) ? f : 'all',
    group: location.query.get('g') ?? '',
    q: location.query.get('q') ?? '',
    offset: Math.max(0, Number.parseInt(location.query.get('o') ?? '', 10) || 0),
  }
  const selected = rest[0] ?? null
  const detailTab: DetailTab = isDetailTab(rest[1]) ? rest[1] : 'profile'

  // 查询串：默认值不写进地址
  const query = useCallback(
    (s: ListState) => ({ f: s.filter === 'all' ? undefined : s.filter, g: s.group || undefined, q: s.q || undefined, o: s.offset || undefined }),
    [],
  )
  const path = (id: string | null, t?: DetailTab) => (id ? `/users/list/${encodeURIComponent(id)}${t && t !== 'profile' ? `/${t}` : ''}` : '/users/list')

  const onChange = useCallback(
    (next: Partial<ListState>) => navigate('/users/list', { replace: true, query: query({ ...state, ...next }) }),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- state 由地址派生，按其原始值比较
    [state.filter, state.group, state.q, state.offset, query],
  )

  if (selected) {
    return (
      <UserDetail
        id={selected}
        tab={detailTab}
        onTab={(t) => navigate(path(selected, t), { replace: true, query: query(state) })}
        backHref={href('/users/list', query(state))}
        now={now}
      />
    )
  }

  return (
    <UserList
      state={state}
      onChange={onChange}
      openUser={(id) => navigate(path(id), { query: query(state) })}
      now={now}
    />
  )
}

