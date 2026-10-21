import { ArrowUpDownIcon, PlusSignIcon } from '@hugeicons-pro/core-stroke-rounded'
import { useMutation } from '@tanstack/react-query'
import { memo, useCallback, useEffect, useMemo, useState } from 'react'
import { formatBytes, formatCount } from '../../../core/format'
import { navigate, parseHash, useHashLocation } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { BulkAction, BulkBar, PageActions, useNow } from '../../kit'
import { Button, Checkbox, Empty, Menu, Pager, QueryView, SearchField, Segmented, Tag, useToast } from '../../../ui'
import {
  addressLabel,
  batchPlan,
  filterNodes,
  moveItem,
  nodeCounts,
  nodeState,
  onlineUsersView,
  orderItems,
  parseNodeFilter,
  protocolLabel,
  sameNodeLine,
  tagOptions,
  type NodeFilter,
  type NodeLineData,
} from './logic'
import { runState, certBadge, sameRunState, type RunState } from './runtime'
import { NodeBatchEdit } from './NodeBatchEdit'
import { NodeCreateWizard } from './NodeForm'
import css from './nodes.module.css'
import s from './NodesTab.module.css'
import { NODE_PAGE, useFailure, useIntentKey, useInvalidateNodes, useNodePage, useNodes, useServers } from './queries'
import { batchStatusResponse, okUpdated, type NodeRow } from './schemas'
import { NODE_BATCH_EDIT_MAX, trafficPercent, trafficUsageLabel } from './xb'

const FILTERS: ReadonlyArray<{ value: NodeFilter; label: string }> = [
  { value: 'all', label: '全部' },
  { value: 'online', label: '在线' },
  { value: 'offline', label: '离线' },
  { value: 'disabled', label: '已停用' },
  { value: 'retired', label: '已退役' },
]

/** 服务端搜索的防抖：节点超过一页、搜索交给服务端时，停手 300 毫秒再发请求 */
const SEARCH_DEBOUNCE_MS = 300

function useDebounced<T>(value: T, ms: number): T {
  const [settled, setSettled] = useState(value)
  useEffect(() => {
    const timer = window.setTimeout(() => setSettled(value), ms)
    return () => window.clearTimeout(timer)
  }, [value, ms])
  return settled
}

/** 筛选 / 搜索 / 标签写回地址，默认项省略查询键 */
function listQueryParams(f: NodeFilter, q: string, tag: string) {
  return {
    f: f === 'all' ? null : f,
    q: q || null,
    tag: tag || null,
  }
}

export function NodesTab() {
  const location = useHashLocation()
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const batchIntent = useIntentKey()
  // 证书剩余天数按分钟走，不在渲染里读时钟
  const now = useNow().getTime()

  const filter = parseNodeFilter(location.query.get('f'))
  const preselect = location.query.get('new')
  const urlQuery = location.query.get('q') ?? ''
  const tag = location.query.get('tag') ?? ''
  // 搜索框是本地文字，停手 300 毫秒再回写地址（照工单页的 SearchBox）：每个键都 replace 地址会让受控输入框跳光标
  const [query, setQuery] = useState(urlQuery)
  const [syncedQuery, setSyncedQuery] = useState(urlQuery)
  if (urlQuery !== syncedQuery) {
    // 地址被外部改了（前进后退、从详情页返回）：跟着地址走
    setSyncedQuery(urlQuery)
    setQuery(urlQuery)
  }

  const [offset, setOffset] = useState(0)
  const [picked, setPicked] = useState<ReadonlySet<string>>(new Set())
  const [sorting, setSorting] = useState<NodeRow[] | null>(null)
  const [creating, setCreating] = useState(false)
  const [createKey, setCreateKey] = useState(0)
  const [batchEditing, setBatchEditing] = useState(false)

  const list = useNodes()
  const servers = useServers()
  const serverMode = !!list.data && list.data.total > list.data.nodes.length
  const serverQuery = useDebounced(query.trim(), SEARCH_DEBOUNCE_MS)
  const page = useNodePage({ state: filter, q: serverQuery, tag, offset }, serverMode)
  const source = serverMode ? page : list
  const loaded = source.data?.nodes

  const rows = useMemo(
    () => sorting ?? (serverMode ? (loaded ?? []) : list.data ? filterNodes(list.data.nodes, filter, query, tag, now) : []),
    [sorting, serverMode, loaded, list.data, filter, query, tag, now],
  )
  const pickedRows = (loaded ?? []).filter((n) => picked.has(n.id))
  const allTags = useMemo(() => tagOptions(list.data?.nodes ?? []), [list.data?.nodes])
  const counts = useMemo(() => nodeCounts(list.data?.nodes ?? []), [list.data?.nodes])

  const setParams = useCallback(
    (patch: { f?: NodeFilter; q?: string; tag?: string }) => {
      const nextF = patch.f ?? filter
      const nextQ = patch.q ?? query
      const nextTag = patch.tag ?? tag
      if (patch.f !== undefined || patch.tag !== undefined) setPicked(new Set())
      setOffset(0)
      navigate('/nodes/nodes', { replace: true, query: listQueryParams(nextF, nextQ, nextTag) })
    },
    [filter, query, tag],
  )

  useEffect(() => {
    if (query === urlQuery) return
    const timer = window.setTimeout(() => setParams({ q: query }), SEARCH_DEBOUNCE_MS)
    return () => window.clearTimeout(timer)
  }, [query, urlQuery, setParams])

  const batch = useMutation({
    mutationFn: (to: 'active' | 'disabled') => {
      const plan = batchPlan(pickedRows, to)
      if (!plan.items.length) return Promise.resolve({ skipped: plan.skipped, updated: 0, to })
      const body = { items: plan.items, serving_status: to }
      return api
        .post('v1/nodes/status:batch', batchStatusResponse, { body, idempotencyKey: batchIntent.keyFor(body) })
        .then((r) => ({ skipped: plan.skipped, updated: r.updated, to }))
    },
    onSuccess: ({ skipped, updated, to }) => {
      batchIntent.reset()
      setPicked(new Set())
      void invalidate()
      const verb = to === 'active' ? '启用' : '停用'
      toast(
        updated ? `已${verb} ${updated} 个节点${skipped ? `，跳过 ${skipped} 个（当前状态不能${verb}${to === 'active' ? '或没绑服务器' : ''}）` : ''}` : `选中的节点当前状态都不能${verb}`,
        updated ? 'ok' : 'danger',
      )
    },
    onError: (e) => {
      fail(e, { intent: batchIntent })
      void invalidate()
    },
  })

  const saveOrder = useMutation({
    mutationFn: (items: ReturnType<typeof orderItems>) => api.put('v1/nodes/order', okUpdated, { body: { items } }),
    onSuccess: () => {
      setSorting(null)
      void invalidate()
      toast('排序已保存，客户端下次拉取订阅生效')
    },
    onError: (e) => {
      fail(e)
      setSorting(null)
      void invalidate()
    },
  })

  const toggleSort = () => {
    if (!sorting) return setSorting(loaded ? loaded.filter((n) => n.serving_status !== 'retired') : null)
    const items = orderItems(sorting)
    if (!items.length) return setSorting(null)
    if (items.length > 200) return toast('一次最多调整 200 个节点的顺序，请分批', 'danger')
    saveOrder.mutate(items)
  }

  const togglePick = useCallback((id: string, on: boolean) => {
    setPicked((prev) => {
      const next = new Set(prev)
      if (on) next.add(id)
      else next.delete(id)
      return next
    })
  }, [])

  const openNode = useCallback((id: string) => {
    navigate(`/nodes/nodes/${id}`, { query: Object.fromEntries(parseHash(window.location.hash).query) })
  }, [])

  const moveNode = useCallback((index: number, d: -1 | 1) => setSorting((prev) => (prev ? moveItem(prev, index, d) : prev)), [])

  const allPicked = rows.length > 0 && rows.every((n) => picked.has(n.id))
  const overBatchLimit = pickedRows.length > NODE_BATCH_EDIT_MAX
  const showFooter = !source.isLoading && !source.isError && source.data != null

  const openBatchEdit = () => {
    if (overBatchLimit) {
      toast('一次最多修改 100 个节点', 'danger')
      return
    }
    setBatchEditing(true)
  }

  return (
    <div className={css.stack}>
      <PageActions>
        <Button
          variant="primary"
          icon={PlusSignIcon}
          disabled={!!sorting}
          onClick={() => {
            setCreateKey((k) => k + 1)
            setCreating(true)
          }}
        >
          新建节点
        </Button>
      </PageActions>

      {picked.size > 0 && !sorting ? (
        <BulkBar count={picked.size} onClear={() => setPicked(new Set())}>
          <BulkAction busy={batch.isPending && batch.variables === 'active'} onClick={() => batch.mutate('active')}>
            启用
          </BulkAction>
          <BulkAction busy={batch.isPending && batch.variables === 'disabled'} onClick={() => batch.mutate('disabled')}>
            停用
          </BulkAction>
          <BulkAction onClick={openBatchEdit}>批量修改</BulkAction>
        </BulkBar>
      ) : (
        <div className={s.filters}>
          <SearchField
            aria-label="搜索节点"
            placeholder="节点名、地区、服务器或协议"
            value={query}
            disabled={!!sorting}
            width={280}
            onChange={(e) => {
              setQuery(e.target.value)
              setOffset(0)
            }}
          />
          <Segmented<NodeFilter>
            label="按状态筛选"
            size="sm"
            value={filter}
            onChange={(v) => setParams({ f: v })}
            options={FILTERS.map((f) => ({ ...f }))}
            className={sorting ? s.dimmed : undefined}
          />
          {filter === 'cert' && (
            <button
              type="button"
              className={`${s.chipf} ${s.chipfSet}`}
              aria-label="清除「证书异常」筛选"
              onClick={() => setParams({ f: 'all' })}
            >
              证书异常 <span aria-hidden="true">×</span>
            </button>
          )}
          {allTags.length > 0 && (
            <Menu
              label="按标签筛选"
              trigger={tag ? <>标签 · <b>{tag}</b></> : <>＋ 标签</>}
              triggerLabel={tag ? `按标签筛选，当前 ${tag}` : '按标签筛选'}
              triggerClassName={tag ? `${s.chipf} ${s.chipfSet}` : s.chipf}
              entries={[
                { key: '', label: '全部标签', current: !tag, onSelect: () => setParams({ tag: '' }) },
                ...allTags.map((t) => ({
                  key: t,
                  label: t,
                  current: t === tag,
                  onSelect: () => setParams({ tag: t }),
                })),
              ]}
            />
          )}
          <span className={s.spacer} />
          {filter !== 'retired' && (
            <Button size="sm" icon={sorting ? undefined : ArrowUpDownIcon} className={s.hideNarrow} onClick={toggleSort} busy={saveOrder.isPending} variant={sorting ? 'primary' : 'secondary'}>
              {sorting ? '完成排序' : '调整排序'}
            </Button>
          )}
          {sorting && (
            <Button size="sm" variant="ghost" onClick={() => setSorting(null)} disabled={saveOrder.isPending}>
              取消
            </Button>
          )}
        </div>
      )}

      <div className={s.box}>
        <div className={`${s.grid} ${s.head}`}>
          <span>
            {!sorting && (
              <Checkbox
                small
                aria-label="全选当前列表"
                checked={allPicked}
                indeterminate={!allPicked && rows.some((n) => picked.has(n.id))}
                onChange={(e) => setPicked(e.target.checked ? new Set(rows.map((n) => n.id)) : new Set())}
              />
            )}
          </span>
          <span>节点</span>
          <span className={s.colHideMobile}>协议</span>
          <span className={s.colHideMobile}>服务器</span>
          <span className={`${css.right} ${s.colHideMobile}`}>在线</span>
          <span className={s.colHideMobile}>负载</span>
          <span className={`${css.right} ${s.colHideMobile}`}>24h 流量</span>
          <span>状态</span>
        </div>
        <QueryView
          query={source}
          rows={6}
          isEmpty={() => rows.length === 0}
          empty={
            filter === 'cert' ? (
              <Empty bare title="没有证书要处理的节点" description="在役节点的证书都正常。" />
            ) : list.data?.nodes.some((n) => n.serving_status !== 'retired') || filter === 'retired' ? (
              <Empty bare title="没有匹配的节点" description="换个筛选条件或搜索词试试。" />
            ) : (
              <Empty bare title="还没有节点" description="新建节点后，用一键安装命令把它接入面板。" />
            )
          }
        >
          {() =>
            rows.map((n, i) => (
              <NodeLine
                key={n.id}
                node={n}
                run={runState(n)}
                now={now}
                index={i}
                picked={picked.has(n.id)}
                sorting={!!sorting}
                onPick={togglePick}
                onOpen={openNode}
                onMove={moveNode}
                first={i === 0}
                last={i === rows.length - 1}
              />
            ))
          }
        </QueryView>
        {showFooter && (
          <div className={s.tfoot}>
            {serverMode && !sorting && page.data ? (
              <>
                <span>共 {page.data.total} 个匹配 · 筛选与搜索在服务端进行</span>
                <span className={s.spacer} />
                <Pager
                  inline
                  total={page.data.total}
                  limit={NODE_PAGE}
                  offset={offset}
                  onChange={(o) => {
                    setOffset(o)
                    setPicked(new Set())
                  }}
                />
              </>
            ) : (
              <span>共 {counts.total} 个节点 · {counts.online} 个在线</span>
            )}
          </div>
        )}
      </div>

      <NodeBatchEdit rows={pickedRows} open={batchEditing} onClose={() => setBatchEditing(false)} onDone={() => setPicked(new Set())} />
      <NodeCreateWizard
        key={`${createKey}-${preselect ?? ''}`}
        open={creating || !!preselect}
        initialServerId={preselect ?? undefined}
        onClose={() => {
          setCreating(false)
          if (preselect) navigate('/nodes/nodes', { replace: true, query: listQueryParams(filter, query, tag) })
        }}
        onCreated={(saved) => {
          setCreating(false)
          const server = saved.server_id ? (servers.data ?? []).find((sv) => sv.id === saved.server_id) : undefined
          let msg: string
          if (!saved.server_id) {
            msg = '节点已创建为草稿，还没绑服务器：要上线时在详情「身份与操作」里绑定服务器'
          } else if (!server || !server.enrolled) {
            msg = '节点已创建为草稿。所在服务器还没接入：到「服务器」页签发安装令牌，在机器上用 root 执行后再回来上线'
          } else {
            msg = '节点已创建为草稿，所在服务器已接入：在节点详情右上角点「上线」'
          }
          toast(msg, 'ok')
          if (preselect) navigate('/nodes/nodes', { replace: true, query: listQueryParams(filter, query, tag) })
          navigate(`/nodes/nodes/${saved.id}/proto`)
        }}
      />
    </div>
  )
}

interface NodeLineProps {
  node: NodeLineData
  run: RunState | null
  now: number
  index: number
  picked: boolean
  sorting: boolean
  onPick: (id: string, on: boolean) => void
  onOpen: (id: string) => void
  onMove: (index: number, d: -1 | 1) => void
  first: boolean
  last: boolean
}

const NodeLine = memo(function NodeLine({ node: n, run, now, index, picked, sorting, onPick, onOpen, onMove, first, last }: NodeLineProps) {
  const state = nodeState(n)
  const cert = certBadge(n.cert, now)
  // 运行状态已说清的不重复：「等待证书」配「证书申请中」、两边都是「证书已过期」；申请失败照样标出来
  const showCertTag =
    cert &&
    cert.tone !== 'ok' &&
    !(run?.label === '等待证书' && cert.label === '证书申请中') &&
    !(run?.label === '证书已过期' && cert.label === '证书已过期')
  const cpu = n.cpu_percent
  const tagList = n.tags
  const visibleTags = tagList.slice(0, 3)
  const extraTagCount = tagList.length - 3
  const allTagsTitle = tagList.join('、')
  const showPeriodTraffic = n.traffic.limit_bytes !== null
  const percent = trafficPercent(n.traffic)
  const isTrafficWarn = percent !== null && percent >= 90
  const isTrafficExceeded = n.traffic.exceeded_at !== null
  const online = onlineUsersView(n)
  const mobileSummary = `${protocolLabel(n.node_type)}${state.filter === 'online' && n.online_users ? ` · 在线 ${formatCount(n.online_users)}` : ''}${online.note ? ` · ${online.note}` : ''} · ${formatBytes(n.traffic_bytes_24h)}`

  return (
    <div className={`${s.grid} ${s.line} ${picked ? s.picked : ''} ${n.serving_status === 'retired' ? s.retired : ''}`}>
      <span>{!sorting && <Checkbox small aria-label={`选择 ${n.name}`} checked={picked} onChange={(e) => onPick(n.id, e.target.checked)} />}</span>
      <button type="button" className={s.nameCell} onClick={() => onOpen(n.id)} disabled={sorting}>
        <div className={s.nameBlock}>
          <div className={s.nameRow}>
            <span className={s.cc}>{n.country_code ?? '—'}</span>
            <span className={s.nameLine}>{n.name}</span>
          </div>
          <div className={s.nsub}>
            <span className={s.nsubAddr}>{addressLabel(n)}</span>
            {visibleTags.map((t) => (
              <Tag key={t} tone="neutral">
                {t}
              </Tag>
            ))}
            {extraTagCount > 0 && (
              <Tag tone="neutral" title={allTagsTitle}>
                +{extraTagCount}
              </Tag>
            )}
            {n.relay_count > 0 && <span className={s.relayCount}>+{n.relay_count} 中转</span>}
            {n.ech_enabled && <Tag tone="neutral">ECH</Tag>}
          </div>
          <div className={s.mobileSummary}>{mobileSummary}</div>
        </div>
      </button>
      <span className={`${css.muted} ${s.colHideMobile}`}>{protocolLabel(n.node_type)}</span>
      <span className={`${css.muted} ${css.ellipsis} ${s.colHideMobile}`}>{n.server_name ?? '未绑定'}</span>
      <span className={`${css.mono} ${css.right} ${s.colHideMobile}`} title={online.note ?? `在线 IP ${n.online_ips}`}>
        {online.value}
        {online.note && (
          <span className={`${s.trafficLine} ${s.trafficFaint}`}>{online.note}</span>
        )}
      </span>
      <span className={`${s.load} ${s.colHideMobile}`}>
        <span className={s.loadBar}>
          <span
            className={`${s.loadFill} ${cpu !== null && cpu > 85 ? s.hot : cpu !== null && cpu > 65 ? s.warm : ''}`}
            style={{ width: `${Math.min(100, cpu ?? 0)}%` }}
          />
        </span>
        <span className={s.loadPct}>{cpu === null ? '—' : `${Math.round(cpu)}%`}</span>
      </span>
      <span className={`${css.mono} ${css.right} ${s.colHideMobile}`}>
        <span className={s.trafficLine}>{formatBytes(n.traffic_bytes_24h)}</span>
        {showPeriodTraffic && (
          <span className={`${s.trafficLine} ${s.trafficFaint} ${isTrafficWarn ? s.trafficWarn : ''}`} title="本周期用量 / 上限">
            {trafficUsageLabel(n.traffic)}
          </span>
        )}
      </span>
      <span className={s.stateCell}>
        {sorting ? (
          <>
            <Button size="xs" variant="ghost" aria-label={`${n.name} 上移`} disabled={first} onClick={() => onMove(index, -1)}>
              ↑
            </Button>
            <Button size="xs" variant="ghost" aria-label={`${n.name} 下移`} disabled={last} onClick={() => onMove(index, 1)}>
              ↓
            </Button>
          </>
        ) : (
          <>
            <span className={css.state}>
              <span className={`${css.dot} ${css[`dot_${state.tone}`]}`} aria-hidden="true" />
              {state.label}
            </span>
            {state.note && <Tag tone={state.note === '排空中' ? 'warn' : 'neutral'}>{state.note}</Tag>}
            {isTrafficExceeded ? (
              <Tag tone="danger" title={n.delivery_note}>
                超限
              </Tag>
            ) : (
              state.filter === 'online' &&
              !n.delivered_to_users && (
                <Tag tone="warn" title={n.delivery_note}>
                  不下发
                </Tag>
              )
            )}
            {run && run.tone !== 'ok' && (
              <Tag tone={run.tone} title={run.detail || run.label} className={s.runTag}>
                {run.label}
              </Tag>
            )}
            {showCertTag && (
              <Tag tone={cert.tone} title={cert.detail || cert.label} className={s.runTag}>
                {cert.label}
              </Tag>
            )}
          </>
        )}
      </span>
    </div>
  )
}, sameLineProps)

function sameLineProps(a: NodeLineProps, b: NodeLineProps): boolean {
  return (
    a.index === b.index &&
    a.now === b.now &&
    a.picked === b.picked &&
    a.sorting === b.sorting &&
    a.first === b.first &&
    a.last === b.last &&
    a.onPick === b.onPick &&
    a.onOpen === b.onOpen &&
    a.onMove === b.onMove &&
    sameRunState(a.run, b.run) &&
    sameNodeLine(a.node, b.node)
  )
}
