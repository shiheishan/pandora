/**
 * [INPUT]: 依赖 react 的 useEffect / useState，依赖 ../../../core/format 的 formatMoney / relativeTime，依赖 ../../../ui 的 Empty / Pager / QueryView / Segmented / Select / Table / Tag，依赖 ./api 的 useUsers / useUserGroups，依赖 ./model，依赖 ./Users.module.css
 * [OUTPUT]: 对外提供 UserList 与 ListState
 * [POS]: 用户页「用户列表」标签：搜索（邮箱 / 显示名 / 用户 ID / 订阅令牌反查，防抖后交给后端 q）、状态分段、用户组下拉、计数、表格（用户、用户组、套餐 · 到期、本期流量、余额、设备、状态、最近登录）与分页；行点击或聚焦后 Enter / 空格（ui/Table 行级激活）打开 #/users/list/<id> 的抽屉，筛选全在查询串上；有写权限时右侧给「批量生成」入口
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useEffect, useState } from 'react'
import { formatMoney, relativeTime } from '../../../core/format'
import { Empty, Pager, QueryView, Segmented, Select, Table, Tag, type TableColumn } from '../../../ui'
import { useUserGroups, useUsers, USERS_PAGE, type UserRow } from './api'
import { deviceView, expiryView, initial, listParams, shortId, STATUS_FILTERS, trafficView, USER_STATUS_VIEW, type StatusFilter } from './model'
import css from './Users.module.css'

export interface ListState {
  filter: StatusFilter
  group: string
  q: string
  offset: number
}

export function UserList({
  state,
  onChange,
  linkFor,
  bulkHref,
  now,
}: {
  state: ListState
  onChange: (next: Partial<ListState>) => void
  /** 打开某个用户抽屉的地址（保留当前筛选） */
  linkFor: (id: string) => string
  /** 「批量生成」入口；无写权限时不给 */
  bulkHref: string | null
  now: Date
}) {
  const users = useUsers(listParams(state.filter, state.group, state.q, state.offset))
  const groups = useUserGroups()
  const total = users.data?.total ?? 0
  const shown = users.data?.users.length ?? 0

  // 「全部用户组」是可选回去的空值项，不用 Select 的 placeholder（那是 disabled 的，选过别的组就回不来）
  const groupOptions = [
    { value: '', label: '全部用户组' },
    { value: 'none', label: '未分组（默认）' },
    ...(groups.data ?? []).map((g) => ({ value: g.id, label: g.name })),
  ]

  return (
    <div className={css.list}>
      <div className={css.toolbar}>
        <SearchBox value={state.q} onChange={(q) => onChange({ q, offset: 0 })} />
        <Segmented size="sm" label="账号状态" options={STATUS_FILTERS.map(([value, label]) => ({ value, label }))} value={state.filter} onChange={(filter) => onChange({ filter, offset: 0 })} />
        <Select
          size="sm"
          aria-label="用户组"
          options={groupOptions}
          value={state.group}
          onChange={(e) => onChange({ group: e.target.value, offset: 0 })}
          fieldClassName={css.groupSelect}
        />
        <div className={css.spacer} />
        {users.data && (
          <span className={css.hint}>
            显示 {shown} / 共 {total.toLocaleString('en-US')}
          </span>
        )}
        {bulkHref && (
          <a className={css.linkButton} href={bulkHref}>
            批量生成
          </a>
        )}
      </div>
      <div className={css.tableBox}>
        <QueryView
          query={users}
          rows={8}
          isEmpty={(d) => d.users.length === 0}
          empty={
            <Empty
              bare
              title={state.q || state.filter !== 'all' || state.group ? '没有符合条件的用户' : '还没有用户'}
              description={state.q || state.filter !== 'all' || state.group ? '换个关键词或筛选条件再看看。' : '用户在门户注册后会出现在这里。'}
            />
          }
        >
          {(d) => <Table label="用户列表" columns={columns(now)} rows={d.users} rowKey={(u) => u.id} onRowClick={(u) => window.location.assign(linkFor(u.id))} />}
        </QueryView>
      </div>
      <Pager total={total} limit={USERS_PAGE} offset={state.offset} onChange={(offset) => onChange({ offset })} />
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
            {initial(u.email, u.display_name)}
          </span>
          <span className={css.whoText}>
            <span className={css.email}>{u.email}</span>
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
        const exp = expiryView(s.current_period_end, now)
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
    { key: 'balance', header: '余额', width: '88px', align: 'right', mono: true, render: (u) => formatMoney(u.balance, u.currency) },
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
  return <input className={css.search} type="search" value={text} onChange={(e) => setText(e.target.value)} placeholder="搜索邮箱、用户 ID、订阅令牌" aria-label="搜索用户" />
}
