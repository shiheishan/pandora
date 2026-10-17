import { useCallback } from 'react'
import { href, navigate, useHashLocation } from '../../../core/router'
import type { AdminScreenProps } from '../index'
import { isDetailTab, isKindFilter, isStatusFilter, type DetailTab } from './model'
import { PlanDetailPage } from './PlanDetail'
import { PlanList, type ListState } from './PlanList'

/** 地址：/plans、/plans/<id>、/plans/<id>/<prices|pools|versions>；查询串 kind、status、q */
export default function Plans({ rest }: AdminScreenProps) {
  const location = useHashLocation()
  const kind = location.query.get('kind')
  const status = location.query.get('status')
  const state: ListState = {
    kind: isKindFilter(kind) ? kind : 'all',
    status: isStatusFilter(status) ? status : 'all',
    q: location.query.get('q') ?? '',
  }
  const selected = rest[0] ?? null
  const tab: DetailTab = isDetailTab(rest[1]) ? rest[1] : 'overview'

  // 查询串：默认值不写进地址
  const query = useCallback(
    (s: ListState) => ({
      kind: s.kind === 'all' ? undefined : s.kind,
      status: s.status === 'all' ? undefined : s.status,
      q: s.q || undefined,
    }),
    [],
  )
  const path = (id: string | null, t?: DetailTab) =>
    id ? `/plans/${encodeURIComponent(id)}${t && t !== 'overview' ? `/${t}` : ''}` : '/plans'

  const onChange = useCallback(
    (next: Partial<ListState>) => navigate(path(selected, tab), { replace: true, query: query({ ...state, ...next }) }),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- state 由地址派生，按其原始值比较
    [selected, tab, state.kind, state.status, state.q, query],
  )

  // rest[0] 非空是详情整页（外框此时不画页头与页签栏），否则是列表
  if (selected) {
    return (
      <PlanDetailPage
        id={selected}
        tab={tab}
        back={{ href: href('/plans', query(state)), label: '套餐' }}
        onTab={(t) => navigate(path(selected, t), { replace: true, query: query(state) })}
      />
    )
  }

  return (
    <PlanList
      state={state}
      onChange={onChange}
      onOpen={(id) => navigate(path(id), { query: query(state) })}
    />
  )
}
