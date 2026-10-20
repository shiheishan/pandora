import { useState } from 'react'
import { formatDateTime, relativeTime } from '../../../core/format'
import { href } from '../../../core/router'
import { Button, Empty, FilterTabs, QueryView, Segmented, StatStrip, Table, Tag, type TableColumn } from '../../../ui'
import { useFailure, useTraceAccounts, useTraceIPs, useTraceReveal, useTraceSubscribers } from './queries'
import {
  accountsText,
  datacenterShare,
  distinctIpText,
  networkLabel,
  sortByRegions,
  traceUserLabel,
  TRACE_VIEWS,
  TRACE_WINDOWS,
  type AccSort,
  type SubSort,
  type TraceAccount,
  type TraceIP,
  type TraceOverview,
  type TraceSubscriber,
  type TraceView,
  type TraceWindow,
} from './trace'
import sec from './security.module.css'
import css from './trace.module.css'

const VIEW_ITEMS = TRACE_VIEWS.map(([value, label]) => ({ value, label }))
const WIN_OPTIONS = TRACE_WINDOWS.map(([value, label]) => ({ value, label }))

export function TraceTab() {
  const [win, setWin] = useState<TraceWindow>('24h')
  const [view, setView] = useState<TraceView>('ips')
  const ips = useTraceIPs(win)
  const since = ips.data?.since

  return (
    <div className={sec.stack}>
      <div className={sec.toolbar}>
        <Segmented label="时间窗" options={WIN_OPTIONS} value={win} onChange={setWin} />
        {since && (
          <p className={`${css.note} ${css.toolbarNote}`}>
            自 {formatDateTime(since)} 起
            {win === '30d' && ' · 拉取日志只保留 31 天，最早一天可能不全'}
          </p>
        )}
      </div>

      <Overview overview={ips.data?.overview} />

      <FilterTabs label="排行" items={VIEW_ITEMS} value={view} onChange={setView} />

      {view === 'ips' && <IPRanking query={ips} />}
      {view === 'subscribers' && <SubscriberRanking win={win} />}
      {view === 'accounts' && <AccountRanking win={win} />}
    </div>
  )
}

function Overview({ overview }: { overview: TraceOverview | undefined }) {
  const o = overview
  const items = o
    ? [
        { label: '独立 IP', value: distinctIpText(o) },
        {
          label: '订阅拉取',
          value: (
            <>
              {o.fetches.toLocaleString('zh-CN')}
              {o.fetch_failures > 0 && <span className={css.sub}>失败 {o.fetch_failures.toLocaleString('zh-CN')}</span>}
            </>
          ),
        },
        {
          label: '登录',
          value: (
            <>
              {o.logins.toLocaleString('zh-CN')}
              {o.login_failures > 0 && <span className={css.sub}>失败 {o.login_failures.toLocaleString('zh-CN')}</span>}
            </>
          ),
        },
        { label: '机房 IP 占比', value: datacenterShare(o) },
      ]
    : undefined

  return (
    <>
      <StatStrip label="溯源概览" items={items} count={4} />
      {o && (
        <section className={css.regions} aria-label="来源地区 Top 5">
          <div className={css.regionsTitle}>来源地区 Top 5</div>
          {o.top_regions.length === 0 ? (
            <p className={css.note}>这段时间没有来源记录</p>
          ) : (
            o.top_regions.map((r) => (
              <div key={r.region} className={css.regionRow}>
                <span className={css.regionName}>{r.region}</span>
                <span className={css.regionMeta}>
                  {r.ips} 个 IP · {r.hits.toLocaleString('zh-CN')} 次
                </span>
              </div>
            ))
          )}
        </section>
      )}
    </>
  )
}

function IPRanking({ query }: { query: ReturnType<typeof useTraceIPs> }) {
  const now = new Date()
  const reveal = useTraceReveal()
  const fail = useFailure()
  const [revealed, setRevealed] = useState<Record<string, string>>({})
  const [open, setOpen] = useState<Record<string, boolean>>({})
  const [pending, setPending] = useState<Record<string, boolean>>({})

  const onReveal = (token: string) => {
    if (!token || pending[token]) return
    setPending((m) => ({ ...m, [token]: true }))
    void reveal(token)
      .then((x) => setRevealed((m) => ({ ...m, [token]: x.ip })))
      .catch((e) => fail(e))
      .finally(() => setPending((m) => ({ ...m, [token]: false })))
  }

  const columns: TableColumn<TraceIP>[] = [
    {
      key: 'ip',
      header: 'IP',
      mono: true,
      render: (r) => {
        const token = r.token
        const shown = token && revealed[token] ? revealed[token] : r.ip_masked
        const canReveal = Boolean(token) && !revealed[token]
        return (
          <>
            {shown}
            {canReveal && (
              <Button size="sm" variant="link" disabled={pending[token]} onClick={() => onReveal(token)}>
                显示
              </Button>
            )}
          </>
        )
      },
    },
    {
      key: 'geo',
      header: '归属地',
      hideOnMobile: true,
      render: (r) => {
        const label = networkLabel(r.network_kind)
        return (
          <>
            {r.geo || '—'}
            {label ? ' ' : null}
            {label ? <Tag tone={r.network_kind === 'datacenter' ? 'warn' : 'neutral'}>{label}</Tag> : null}
          </>
        )
      },
    },
    {
      key: 'hits',
      header: '次数',
      align: 'right',
      render: (r) => <span title={`拉取 ${r.fetches} · 行为 ${r.events}`}>{r.hits.toLocaleString('zh-CN')}</span>,
    },
    {
      key: 'accounts',
      header: '关联账号',
      align: 'right',
      render: (r) => {
        const expanded = open[r.token]
        if (r.users.length === 0) return accountsText(r)
        return (
          <div>
            <Button size="sm" variant="link" onClick={() => setOpen((m) => ({ ...m, [r.token]: !m[r.token] }))}>
              {accountsText(r)}
            </Button>
            {expanded && (
              <div className={css.userList}>
                {r.users.map((u) => (
                  <a key={u.id} href={href(`/users/list/${u.id}`)}>
                    {traceUserLabel(u)}
                  </a>
                ))}
              </div>
            )}
          </div>
        )
      },
    },
    { key: 'ua_kinds', header: 'UA 种类', align: 'right', hideOnMobile: true, render: (r) => r.ua_kinds },
    {
      key: 'last_seen',
      header: '最近出现',
      align: 'right',
      render: (r) => (
        <span className={css.muted} title={formatDateTime(r.last_seen)}>
          {relativeTime(r.last_seen, now)}
        </span>
      ),
    },
  ]

  return (
    <QueryView
      query={query}
      rows={6}
      isEmpty={(d) => d.ips.length === 0}
      empty={<Empty bare title="这段时间没有访问记录" />}
    >
      {(d) => (
        <>
          <Table
            boxed
            label="IP 访问排行"
            rows={d.ips}
            rowKey={(r) => r.token || r.ip_masked + r.last_seen}
            columns={columns}
          />
          <p className={css.note}>最多列出 100 个 IP；IP 默认打码，点「显示」查看完整地址并写入审计。</p>
        </>
      )}
    </QueryView>
  )
}

function SubscriberRanking({ win }: { win: TraceWindow }) {
  const now = new Date()
  const [sort, setSort] = useState<SubSort | 'regions'>('fetches')
  const serverSort: SubSort = sort === 'regions' ? 'fetches' : sort
  const q = useTraceSubscribers(win, serverSort)

  const onSortChange = (key: string) => {
    if (sort === key) return
    setSort(key as SubSort | 'regions')
  }

  const columns: TableColumn<TraceSubscriber>[] = [
    {
      key: 'user',
      header: '账号',
      render: (r) => (
        <a href={href(`/users/list/${r.user_id}`)}>
          {traceUserLabel({ id: r.user_id, email: r.email, status: r.status })}
        </a>
      ),
    },
    {
      key: 'fetches',
      header: '拉取次数',
      align: 'right',
      sortable: true,
      render: (r) => r.fetches.toLocaleString('zh-CN'),
    },
    { key: 'ips', header: '来源 IP', align: 'right', sortable: true, render: (r) => r.ips },
    {
      key: 'ua_kinds',
      header: '客户端',
      align: 'right',
      hideOnMobile: true,
      sortable: true,
      render: (r) => (
        <span title={r.families.join(' / ')}>
          {r.ua_kinds}
          {r.families.length > 0 && <span className={css.sub}> {r.families.join(' / ')}</span>}
        </span>
      ),
    },
    { key: 'regions', header: '地区数', align: 'right', hideOnMobile: true, sortable: true, render: (r) => r.regions },
    {
      key: 'failures',
      header: '失败',
      align: 'right',
      sortable: true,
      render: (r) =>
        r.failures > 0 ? <span className={css.bad}>{r.failures.toLocaleString('zh-CN')}</span> : <span className={css.muted}>0</span>,
    },
    {
      key: 'last',
      header: '最近拉取',
      align: 'right',
      sortable: true,
      render: (r) => (
        <span className={css.muted} title={formatDateTime(r.last_fetch)}>
          {relativeTime(r.last_fetch, now)}
        </span>
      ),
    },
  ]

  return (
    <QueryView
      query={q}
      rows={6}
      isEmpty={(d) => d.subscribers.length === 0}
      empty={<Empty bare title="这段时间没有订阅拉取" />}
    >
      {(d) => {
        const rows = sort === 'regions' ? sortByRegions(d.subscribers) : d.subscribers
        return (
          <>
            <Table
              boxed
              label="订阅更新排行"
              rows={rows}
              rowKey={(r) => r.user_id}
              sort={{ key: sort, dir: 'desc' }}
              onSortChange={onSortChange}
              columns={columns}
            />
            <p className={css.note}>最多列出 100 个账号；「地区数」只在本页内排序。</p>
          </>
        )
      }}
    </QueryView>
  )
}

function AccountRanking({ win }: { win: TraceWindow }) {
  const now = new Date()
  const [sort, setSort] = useState<AccSort>('linked_ips')
  const q = useTraceAccounts(win, sort)

  const onSortChange = (key: string) => {
    if (sort === key) return
    setSort(key as AccSort)
  }

  const columns: TableColumn<TraceAccount>[] = [
    {
      key: 'user',
      header: '账号',
      render: (r) => (
        <a href={href(`/users/list/${r.user_id}`)}>
          {traceUserLabel({ id: r.user_id, email: r.email, status: r.status })}
        </a>
      ),
    },
    {
      key: 'linked_ips',
      header: '关联 IP',
      align: 'right',
      sortable: true,
      render: (r) => r.linked_ips,
    },
    {
      key: 'logins',
      header: '登录次数',
      align: 'right',
      sortable: true,
      render: (r) => r.logins.toLocaleString('zh-CN'),
    },
    { key: 'login_ips', header: '登录 IP', align: 'right', hideOnMobile: true, sortable: true, render: (r) => r.login_ips },
    { key: 'fetch_ips', header: '拉取 IP', align: 'right', hideOnMobile: true, sortable: true, render: (r) => r.fetch_ips },
    {
      key: 'shared_ips',
      header: '与他人共用 IP',
      align: 'right',
      sortable: true,
      render: (r) => (r.shared_ips > 0 ? <span className={css.warn}>{r.shared_ips}</span> : r.shared_ips),
    },
    { key: 'ua_kinds', header: 'UA 种类', align: 'right', hideOnMobile: true, sortable: true, render: (r) => r.ua_kinds },
    {
      key: 'datacenter_hits',
      header: '机房命中',
      align: 'right',
      hideOnMobile: true,
      sortable: true,
      render: (r) => (r.datacenter_hits > 0 ? <span className={css.warn}>{r.datacenter_hits}</span> : r.datacenter_hits),
    },
    {
      key: 'last_seen',
      header: '最近出现',
      align: 'right',
      hideOnMobile: true,
      render: (r) => (
        <span className={css.muted} title={formatDateTime(r.last_seen)}>
          {relativeTime(r.last_seen, now)}
        </span>
      ),
    },
  ]

  return (
    <QueryView
      query={q}
      rows={6}
      isEmpty={(d) => d.accounts.length === 0}
      empty={<Empty bare title="这段时间没有账号活动" />}
    >
      {(d) => (
        <>
          <Table
            boxed
            label="账号综合排行"
            rows={d.accounts}
            rowKey={(r) => r.user_id}
            sort={{ key: sort, dir: 'desc' }}
            onSortChange={onSortChange}
            columns={columns}
          />
          <p className={css.note}>关联 IP = 行为 IP 与拉取 IP 合并去重；最多列出 100 个账号。</p>
        </>
      )}
    </QueryView>
  )
}
