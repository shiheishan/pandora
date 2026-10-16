import { useEffect, useState } from 'react'
import { formatMoney, relativeTime } from '../../../core/format'
import { href } from '../../../core/router'
import {
  Button,
  Empty,
  FilterTabs,
  QueryView,
  SearchField,
  Select,
  Table,
  Tag,
  Toolbar,
  type TableColumn,
} from '../../../ui'
import { useUserGroups, useUsers, USERS_PAGE, type UserRow } from './api'
import {
  deviceView,
  groupOptionLabel,
  initial,
  listParams,
  shortId,
  STATUS_FILTERS,
  subExpiryView,
  trafficView,
  USER_STATUS_VIEW,
  userLabel,
  type StatusFilter,
} from './model'
import listCss from './List.module.css'
import css from './Users.module.css'

export interface ListState {
  filter: StatusFilter
  group: string
  q: string
  offset: number
}

const statusFilterItems = STATUS_FILTERS.map(([value, label]) => ({ value, label }))

export function UserList({
  state,
  onChange,
  linkFor,
  now,
}: {
  state: ListState
  onChange: (next: Partial<ListState>) => void
  /** 打开某个用户抽屉的地址（保留当前筛选） */
  linkFor: (id: string) => string
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

  return (
    <div className={listCss.list}>
      <Toolbar
        end={
          <>
            <span className={css.hint}>按注册时间</span>
            <a className={css.linkButton} href={href('/users/bulk')}>
              批量生成
            </a>
          </>
        }
      >
        <SearchBox value={state.q} onChange={(q) => onChange({ q, offset: 0 })} />
        <FilterTabs
          label="账号状态"
          items={statusFilterItems}
          value={state.filter}
          onChange={(filter) => onChange({ filter, offset: 0 })}
        />
        <Select
          size="sm"
          aria-label="用户组"
          emptyOption="全部用户组"
          options={groupOptions}
          value={state.group}
          onChange={(e) => onChange({ group: e.target.value, offset: 0 })}
          fieldClassName={css.groupSelect}
        />
      </Toolbar>
      <div className={listCss.tableBox}>
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
              stickyHeader
              label="用户列表"
              columns={columns(now)}
              rows={d.users}
              rowKey={(u) => u.id}
              onRowClick={(u) => window.location.assign(linkFor(u.id))}
            />
          )}
        </QueryView>
      </div>
      <footer className={listCss.footer}>
        <span className={listCss.footerTotal}>共 {total.toLocaleString('en-US')} 位用户</span>
        <span className={listCss.footerRange}>第 {pageFrom}–{pageTo} 条</span>
        <Button size="xs" disabled={noPrev} onClick={() => onChange({ offset: Math.max(0, state.offset - USERS_PAGE) })}>
          上一页
        </Button>
        <Button size="xs" disabled={noNext} onClick={() => onChange({ offset: state.offset + USERS_PAGE })}>
          下一页
        </Button>
      </footer>
    </div>
  )
}

function columns(now: Date): TableColumn<UserRow>[] {
  return [
    {
      key: 'user',
      header: '用户',
      render: (u) => (
        <span className={css.who}>
          <span className={css.avatar} aria-hidden="true">
            {u.status === 'deleted' ? '#' : initial(u.email, u.display_name)}
          </span>
          <span className={css.whoText}>
            <span className={css.email}>{userLabel(u)}</span>
            <span className={css.mono}>#{shortId(u.id)}</span>
          </span>
        </span>
      ),
    },
    { key: 'group', header: '用户组', width: '84px', render: (u) => <span className={css.muted}>{u.group_name || '默认'}</span> },
    {
      key: 'plan',
      header: '套餐 · 到期',
      width: '120px',
      render: (u) => {
        const s = u.current_subscription
        if (!s) return <span className={css.muted}>无订阅</span>
        const exp = subExpiryView(s.current_period_end, now)
        return (
          <span className={css.stack}>
            <span>{s.plan_name}</span>
            <span className={`${css.small} ${css[`tone_${exp.tone}`]}`}>{exp.text}</span>
          </span>
        )
      },
    },
    {
      key: 'traffic',
      header: '本期流量',
      width: '150px',
      render: (u) => {
        const s = u.current_subscription
        if (!s) return <span className={css.muted}>—</span>
        const t = trafficView(s.traffic.limit, s.traffic.consumed)
        return (
          <span className={css.stack}>
            <span className={css.mono}>{t.text}</span>
            <span className={css.track} aria-hidden="true">
              <span className={`${css.fill} ${css[`fill_${t.tone}`]}`} style={{ width: `${t.percent}%` }} />
            </span>
          </span>
        )
      },
    },
    { key: 'balance', header: '余额', width: '88px', align: 'right', mono: true, render: (u) => formatMoney(u.balance) },
    {
      key: 'devices',
      header: '设备',
      width: '64px',
      align: 'right',
      mono: true,
      render: (u) => {
        const s = u.current_subscription
        if (!s) return <span className={css.muted}>—</span>
        const d = deviceView(s.online_devices, s.device_limit)
        return <span className={css[`tone_${d.tone}`]}>{d.text}</span>
      },
    },
    {
      key: 'status',
      header: '状态',
      width: '72px',
      render: (u) => {
        const v = USER_STATUS_VIEW[u.status]
        return <Tag tone={v.tone}>{v.label}</Tag>
      },
    },
    {
      key: 'seen',
      header: '最近登录',
      width: '84px',
      align: 'right',
      render: (u) => <span className={css.muted}>{u.last_login_at ? relativeTime(u.last_login_at, now) : '从未'}</span>,
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
