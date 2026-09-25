/**
 * [INPUT]: 依赖 react 的 useCallback / useEffect / useState，依赖 ../../../core/router 的 href / navigate / useHashLocation，依赖 ../../actions 的 useCan，依赖 ../Placeholder，依赖 ../index 的 AdminScreenProps，依赖 ./UserList、./UserDrawer，依赖 ./model 的 isStatusFilter
 * [OUTPUT]: 默认导出 Users 页面组件（登记表 React.lazy 的目标）
 * [POS]: admin/screens/users 的入口：用户（后台-03）。「用户列表」标签是列表 + 抽屉：#/users/list/<用户 id>/<抽屉标签>?f=&g=&q=&o=，筛选、翻页、打开的用户与标签全在地址上；用户组、批量运营、设备策略、流量重置四个标签在第 ④ 步接入，目前是占位
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useCallback, useEffect, useState } from 'react'
import { href, navigate, useHashLocation } from '../../../core/router'
import { useCan } from '../../actions'
import type { AdminScreenProps } from '../index'
import { Placeholder } from '../Placeholder'
import { isStatusFilter } from './model'
import { isDrawerTab, UserDrawer, type DrawerTab } from './UserDrawer'
import { UserList, type ListState } from './UserList'

export default function Users({ tab, rest }: AdminScreenProps) {
  if (tab !== 'list') return <Placeholder module="users" tab={tab} rest={rest} />
  return <ListTab rest={rest} />
}

function ListTab({ rest }: { rest: string[] }) {
  const location = useHashLocation()
  const can = useCan()
  const now = useNow()
  const f = location.query.get('f')
  const state: ListState = {
    filter: isStatusFilter(f) ? f : 'all',
    group: location.query.get('g') ?? '',
    q: location.query.get('q') ?? '',
    offset: Math.max(0, Number.parseInt(location.query.get('o') ?? '', 10) || 0),
  }
  const selected = rest[0] ?? null
  const drawerTab: DrawerTab = isDrawerTab(rest[1]) ? rest[1] : 'profile'

  // 查询串：默认值不写进地址
  const query = useCallback(
    (s: ListState) => ({ f: s.filter === 'all' ? undefined : s.filter, g: s.group || undefined, q: s.q || undefined, o: s.offset || undefined }),
    [],
  )
  const path = (id: string | null, t?: DrawerTab) => (id ? `/users/list/${encodeURIComponent(id)}${t && t !== 'profile' ? `/${t}` : ''}` : '/users/list')

  const onChange = useCallback(
    (next: Partial<ListState>) => navigate(path(selected, drawerTab), { replace: true, query: query({ ...state, ...next }) }),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- state 由地址派生，按其原始值比较
    [selected, drawerTab, state.filter, state.group, state.q, state.offset, query],
  )

  return (
    <>
      <UserList
        state={state}
        onChange={onChange}
        linkFor={(id) => href(path(id), query(state))}
        bulkHref={can('iam.user.write') ? href('/users/bulk') : null}
        now={now}
      />
      <UserDrawer
        id={selected}
        tab={drawerTab}
        onTab={(t) => navigate(path(selected, t), { replace: true, query: query(state) })}
        onClose={() => navigate(path(null), { query: query(state) })}
        now={now}
      />
    </>
  )
}

/** 相对时间与到期天数每分钟走一次 */
function useNow(intervalMs = 60_000): Date {
  const [now, setNow] = useState(() => new Date())
  useEffect(() => {
    const timer = setInterval(() => setNow(new Date()), intervalMs)
    return () => clearInterval(timer)
  }, [intervalMs])
  return now
}
