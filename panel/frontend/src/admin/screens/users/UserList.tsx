import { useEffect, useState } from 'react'
import { formatMoney, relativeTime } from '../../../core/format'
import { Avatar, PageCount, StatusDot } from '../../kit'
import {
  Button,
  Empty,
  QueryView,
  SearchField,
  Select,
  Table,
  Toolbar,
  type TableColumn,
} from '../../../ui'
import { useUserGroups, useUsers, USERS_PAGE, type UserRow } from './api'
import { dotTone, listStatusView } from './detail'
import {
  deviceView,
  groupOptionLabel,
  initial,
  isStatusFilter,
  listParams,
  shortId,
  STATUS_FILTERS,
  subExpiryView,
  trafficView,
  userLabel,
  type StatusFilter,
} from './model'
import listCss from './List.module.css'

export interface ListState {
  filter: StatusFilter
  group: string
  q: string
  offset: number
}

const statusSelectOptions = STATUS_FILTERS.filter(([k]) => k !== 'all').map(([value, label]) => ({ value, label }))

export function UserList({
  state,
  onChange,
  openUser,
  now,
}: {
  state: ListState
  onChange: (next: Partial<ListState>) => void
  openUser: (id: string) => void
  now: Date
}) {
  const users = useUsers(listParams(state.filter, state.group, state.q, state.offset))
  const groups = useUserGroups()
  const total = users.data?.total ?? 0
  const pageFrom = total === 0 ? 0 : state.offset + 1
  const pageTo = total === 0 ? 0 : Math.min(state.offset + USERS_PAGE, total)
  const noPrev = total === 0 || state.offset <= 0
  const noNext = total === 0 || state.offset + USERS_PAGE >= total

  const groupOptions = [
    { value: 'none', label: '未分组（默认）' },
    ...(groups.data ?? []).map((g) => ({ value: g.id, label: groupOptionLabel(g) })),
  ]

  const tableFooter = (
    <div className={listCss.foot}>
      <span>共 {total.toLocaleString('en-US')} 位</span>
      <span className={listCss.footSpacer} />
      <span className={listCss.range}>第 {pageFrom}–{pageTo} 条</span>
      <Button size="sm" disabled={noPrev} onClick={() => onChange({ offset: Math.max(0, state.offset - USERS_PAGE) })}>
        上一页
      </Button>
      <Button size="sm" disabled={noNext} onClick={() => onChange({ offset: state.offset + USERS_PAGE })}>
        下一页
      </Button>
    </div>
  )

  return (
    <>
      <PageCount value={users.data?.total ?? null} />
      <Toolbar
        end={<span className={listCss.sortHint}>注册时间 · 新的在前</span>}
      >
        <div className={listCss.search}>
          <SearchBox value={state.q} onChange={(q) => onChange({ q, offset: 0 })} />
        </div>
        <Select
          size="sm"
          aria-label="账号状态"
          emptyOption="全部状态"
          options={statusSelectOptions}
          value={state.filter === 'all' ? '' : state.filter}
          fieldClassName={listCss.filterSelect}
          onChange={(e) => {
            const v = e.target.value
            onChange({ filter: v === '' || !isStatusFilter(v) ? 'all' : v, offset: 0 })
          }}
        />
        <Select
          size="sm"
          aria-label="用户组"
          emptyOption="全部用户组"
          options={groupOptions}
          value={state.group}
          fieldClassName={listCss.filterSelect}
          onChange={(e) => onChange({ group: e.target.value, offset: 0 })}
        />
      </Toolbar>
      <QueryView
        query={users}
        rows={8}
        isEmpty={(d) => d.users.length === 0}
        empty={
          <Empty
            bare
            title={state.q || state.filter !== 'all' || state.group ? '没有符合条件的用户' : '还没有用户'}
            description={
              state.q || state.filter !== 'all' || state.group
                ? '换个关键词或筛选条件再看看。'
                : '用户在用户端注册后会出现在这里。'
            }
          />
        }
      >
        {(d) => (
          <Table
            boxed
            label="用户列表"
            columns={columns(now)}
            rows={d.users}
            rowKey={(u) => u.id}
            onRowClick={(u) => openUser(u.id)}
            footer={tableFooter}
          />
        )}
      </QueryView>
    </>
  )
}

function columns(now: Date): TableColumn<UserRow>[] {
  return [
    {
      key: 'user',
      header: '用户',
      render: (u) => (
        <div className={listCss.who}>
          <Avatar text={u.status === 'deleted' ? '#' : initial(u.email, u.display_name)} />
          <div className={listCss.whoText}>
            <span className={listCss.name}>{userLabel(u)}</span>
            <span className={listCss.id}>{shortId(u.id)}</span>
          </div>
        </div>
      ),
    },
    {
      key: 'status',
      header: '状态',
      width: '88px',
      render: (u) => {
        const v = listStatusView(u)
        return <StatusDot tone={dotTone(v.tone)}>{v.label}</StatusDot>
      },
    },
    {
      key: 'plan',
      header: '套餐',
      width: '140px',
      render: (u) => {
        const s = u.current_subscription
        if (!s) return <span className={listCss.muted}>无套餐</span>
        const exp = subExpiryView(s.current_period_end, now)
        return (
          <div className={listCss.stack}>
            <span>{s.plan_name}</span>
            <span className={listCss.small}>{exp.text}</span>
          </div>
        )
      },
    },
    {
      key: 'traffic',
      header: '本期流量',
      width: '160px',
      render: (u) => {
        const s = u.current_subscription
        if (!s) return <span className={listCss.muted}>—</span>
        const t = trafficView(s.traffic.limit, s.traffic.consumed)
        const fillClass =
          t.tone === 'danger' || t.tone === 'warn' ? listCss.fillWarn : listCss.fill
        return (
          <div className={listCss.stack}>
            <span className={listCss.trafficText}>{t.text}</span>
            <span className={listCss.track} aria-hidden="true">
              <span className={fillClass} style={{ width: `${t.percent}%` }} />
            </span>
          </div>
        )
      },
    },
    {
      key: 'balance',
      header: '余额',
      width: '96px',
      align: 'right',
      mono: true,
      render: (u) => formatMoney(u.balance),
    },
    {
      key: 'devices',
      header: '设备',
      width: '72px',
      align: 'right',
      mono: true,
      render: (u) => {
        const s = u.current_subscription
        if (!s) return <span className={listCss.muted}>—</span>
        const d = deviceView(s.online_devices, s.device_limit)
        const toneClass = d.tone === 'danger' ? listCss.toneDanger : d.tone === 'warn' ? listCss.toneWarn : undefined
        return <span className={toneClass}>{d.text}</span>
      },
    },
    {
      key: 'group',
      header: '用户组',
      width: '96px',
      render: (u) => <span className={listCss.muted}>{u.group_name || '默认'}</span>,
    },
    {
      key: 'seen',
      header: '最近登录',
      width: '96px',
      align: 'right',
      render: (u) => (
        <span className={listCss.muted}>{u.last_login_at ? relativeTime(u.last_login_at, now) : '从未'}</span>
      ),
    },
  ]
}

/** 搜索框：输入 300ms 后才回写地址；地址被外部改了（前进后退）跟着地址走 */
function SearchBox({ value, onChange }: { value: string; onChange: (q: string) => void }) {
  const [text, setText] = useState(value)
  const [synced, setSynced] = useState(value)
  if (value !== synced) {
    setSynced(value)
    setText(value)
  }
  useEffect(() => {
    if (text === value) return
    const timer = setTimeout(() => onChange(text), 300)
    return () => clearTimeout(timer)
  }, [text, value, onChange])
  return (
    <SearchField
      aria-label="搜索用户"
      placeholder="邮箱、用户 ID 或订阅令牌"
      width={240}
      value={text}
      onChange={(e) => setText(e.target.value)}
    />
  )
}
