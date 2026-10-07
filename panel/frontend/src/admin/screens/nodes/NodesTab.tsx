import { useMutation } from '@tanstack/react-query'
import { memo, useCallback, useEffect, useMemo, useState } from 'react'
import { formatBytes, formatCount } from '../../../core/format'
import { navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Checkbox, Empty, IconClose, Input, Modal, Pager, QueryView, Segmented, Tag, useToast } from '../../../ui'
import { addressLabel, batchPlan, filterNodes, moveItem, nodeState, orderItems, protocolLabel, sameNodeLine, type NodeFilter, type NodeLineData } from './logic'
import { DRAWER_TABS, NodeDrawer, type DrawerTab } from './NodeDrawer'
import { NodeForm } from './NodeForm'
import css from './nodes.module.css'
import { NODE_PAGE, useCan, useFailure, useIntentKey, useInvalidateNodes, useNodeDetail, useNodePage, useNodes } from './queries'
import { batchStatusResponse, okUpdated, type NodeRow } from './schemas'

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

export function NodesTab({ rest }: { rest: string[] }) {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const batchIntent = useIntentKey()
  const [filter, setFilter] = useState<NodeFilter>('all')
  const [query, setQuery] = useState('')
  const [offset, setOffset] = useState(0)
  const [picked, setPicked] = useState<ReadonlySet<string>>(new Set())
  const [sorting, setSorting] = useState<NodeRow[] | null>(null)
  const [creating, setCreating] = useState(false)
  const list = useNodes()

  // 节点总数超过一页（1000）：筛选、搜索、翻页交给服务端，第 1001 个以后的节点也找得到
  const serverMode = !!list.data && list.data.total > list.data.nodes.length
  const serverQuery = useDebounced(query.trim(), SEARCH_DEBOUNCE_MS)
  const page = useNodePage({ state: filter, q: serverQuery, offset }, serverMode)
  const source = serverMode ? page : list
  const loaded = source.data?.nodes

  const rows = useMemo(() => sorting ?? (serverMode ? (loaded ?? []) : list.data ? filterNodes(list.data.nodes, filter, query) : []), [sorting, serverMode, loaded, list.data, filter, query])
  const openId = rest[0] ?? null
  const tab = (DRAWER_TABS.find(([k]) => k === rest[1])?.[0] ?? 'metrics') as DrawerTab
  // 抽屉：先在已加载的行里找；找不到（地址直接指到没加载的节点）用单取的结果。单取同时给协议表单带回编辑字段
  const detail = useNodeDetail(openId)
  const open = loaded?.find((n) => n.id === openId) ?? list.data?.nodes.find((n) => n.id === openId) ?? detail.data ?? null
  const pickedRows = (loaded ?? []).filter((n) => picked.has(n.id))

  const batch = useMutation({
    mutationFn: (to: 'active' | 'disabled') => {
      const plan = batchPlan(pickedRows, to)
      if (!plan.items.length) return Promise.resolve({ skipped: plan.skipped, updated: 0, to })
      const body = { items: plan.items, serving_status: to }
      return api.post('v1/nodes/status:batch', batchStatusResponse, { body, idempotencyKey: batchIntent.keyFor(body) }).then((r) => ({ skipped: plan.skipped, updated: r.updated, to }))
    },
    onSuccess: ({ skipped, updated, to }) => {
      batchIntent.reset()
      setPicked(new Set())
      void invalidate()
      const verb = to === 'active' ? '启用' : '停用'
      toast(updated ? `已${verb} ${updated} 个节点${skipped ? `，跳过 ${skipped} 个（当前状态不能${verb}）` : ''}` : `选中的节点当前状态都不能${verb}`, updated ? 'ok' : 'danger')
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

  // 行的回调保持稳定，配合 NodeLine 的 memo：一次刷新只重渲染显示内容真的变了的行
  const togglePick = useCallback((id: string, on: boolean) => {
    setPicked((prev) => {
      const next = new Set(prev)
      if (on) next.add(id)
      else next.delete(id)
      return next
    })
  }, [])
  const openNode = useCallback((id: string) => navigate(`/nodes/nodes/${id}`), [])
  const moveNode = useCallback((index: number, d: -1 | 1) => setSorting((s) => (s ? moveItem(s, index, d) : s)), [])
  const allPicked = rows.length > 0 && rows.every((n) => picked.has(n.id))

  return (
    <div className={css.stack}>
      <div className={css.toolbar}>
        <Input aria-label="搜索节点" size="sm" value={query} disabled={!!sorting} onChange={(e) => {
            setQuery(e.target.value)
            setOffset(0)
          }}
          placeholder="节点名、地区、服务器或协议" fieldClassName={css.search} />
        <Segmented<NodeFilter>
          label="按状态筛选"
          size="sm"
          value={filter}
          onChange={(v) => {
            setFilter(v)
            setOffset(0)
            setPicked(new Set())
          }}
          options={FILTERS.map((f) => ({ ...f }))}
          className={sorting ? css.dimmed : undefined}
        />
        {picked.size > 0 && can('node.lifecycle') && (
          <span className={css.batchBar}>
            <span>已选 {picked.size}</span>
            <Button size="xs" busy={batch.isPending && batch.variables === 'active'} onClick={() => batch.mutate('active')}>
              启用
            </Button>
            <Button size="xs" busy={batch.isPending && batch.variables === 'disabled'} onClick={() => batch.mutate('disabled')}>
              停用
            </Button>
            <Button size="xs" variant="ghost" aria-label="取消选择" onClick={() => setPicked(new Set())}>
              <IconClose />
            </Button>
          </span>
        )}
        <span className={css.spacer} />
        {can('node.write') && filter !== 'retired' && (
          <Button onClick={toggleSort} busy={saveOrder.isPending} variant={sorting ? 'outline' : 'secondary'}>
            {sorting ? '完成排序' : '调整排序'}
          </Button>
        )}
        {sorting && (
          <Button variant="ghost" onClick={() => setSorting(null)} disabled={saveOrder.isPending}>
            取消
          </Button>
        )}
        {can('node.provision') && !sorting && (
          <Button variant="primary" onClick={() => setCreating(true)}>
            新建节点
          </Button>
        )}
      </div>

      <div className={css.panel}>
        <div className={`${css.grid} ${css.head}`}>
          <span>{!sorting && <Checkbox small aria-label="全选当前列表" checked={allPicked} indeterminate={!allPicked && rows.some((n) => picked.has(n.id))} onChange={(e) => setPicked(e.target.checked ? new Set(rows.map((n) => n.id)) : new Set())} />}</span>
          <span>节点</span>
          <span>协议</span>
          <span>服务器</span>
          <span className={css.right}>在线</span>
          <span>负载</span>
          <span className={css.right}>24h 流量</span>
          <span>状态</span>
        </div>
        <QueryView
          query={source}
          rows={6}
          isEmpty={() => rows.length === 0}
          empty={
            list.data?.nodes.some((n) => n.serving_status !== 'retired') || filter === 'retired' ? (
              <Empty bare title="没有匹配的节点" description="换个筛选条件或搜索词试试。" />
            ) : (
              <Empty bare title="还没有节点" description={can('node.provision') ? '新建节点后，用一键安装命令把它接入面板。' : '有节点部署权限的同事可以在这里新建节点。'} />
            )
          }
        >
          {() => (
            <>
              {rows.map((n, i) => (
                <NodeLine key={n.id} node={n} index={i} picked={picked.has(n.id)} sorting={!!sorting} onPick={togglePick} onOpen={openNode} onMove={moveNode} first={i === 0} last={i === rows.length - 1} />
              ))}
              {serverMode && !sorting && page.data && (
                <div className={css.more}>
                  共 {list.data?.total} 个节点，筛选与搜索在服务端进行
                  <Pager
                    total={page.data.total}
                    limit={NODE_PAGE}
                    offset={offset}
                    onChange={(o) => {
                      setOffset(o)
                      setPicked(new Set())
                    }}
                  />
                </div>
              )}
            </>
          )}
        </QueryView>
      </div>

      <NodeDrawer node={open} detail={detail} tab={tab} onClose={() => navigate('/nodes/nodes')} />
      <Modal open={creating} onClose={() => setCreating(false)} size="lg" title="新建节点" eyebrow="建成草稿，签发安装令牌并启用后才会下发">
        {creating && (
          <NodeForm
            node={null}
            onCancel={() => setCreating(false)}
            onSaved={(saved) => {
              setCreating(false)
              toast('节点已创建为草稿，签发安装令牌并启用后才会下发')
              navigate(`/nodes/nodes/${saved.id}/identity`)
            }}
          />
        )}
      </Modal>
    </div>
  )
}

interface NodeLineProps {
  node: NodeLineData
  index: number
  picked: boolean
  sorting: boolean
  onPick: (id: string, on: boolean) => void
  onOpen: (id: string) => void
  onMove: (index: number, d: -1 | 1) => void
  first: boolean
  last: boolean
}

/**
 * 列表一行。react-query 的结构共享让没变的节点保持同一个对象，但心跳时刻这类不显示的字段
 * 几乎每次刷新都在变，所以 memo 按行上实际显示的字段比（logic.ts 的 NODE_LINE_FIELDS）
 */
const NodeLine = memo(function NodeLine({ node: n, index, picked, sorting, onPick, onOpen, onMove, first, last }: NodeLineProps) {
  const state = nodeState(n)
  const cpu = n.cpu_percent
  return (
    <div className={`${css.grid} ${css.line} ${picked ? css.picked : ''} ${n.serving_status === 'retired' ? css.retired : ''}`}>
      <span>{!sorting && <Checkbox small aria-label={`选择 ${n.name}`} checked={picked} onChange={(e) => onPick(n.id, e.target.checked)} />}</span>
      <button type="button" className={css.nameCell} onClick={() => onOpen(n.id)} disabled={sorting}>
        <span className={css.cc}>{n.country_code ?? '—'}</span>
        <span className={css.nameText}>
          <span className={css.ellipsis}>{n.name}</span>
          <span className={`${css.mono} ${css.faint} ${css.ellipsis}`}>{addressLabel(n)}</span>
        </span>
      </button>
      <span className={css.muted}>{protocolLabel(n.node_type)}</span>
      <span className={`${css.muted} ${css.ellipsis}`}>{n.server_name ?? '—'}</span>
      <span className={`${css.mono} ${css.right}`} title={`在线 IP ${n.online_ips}`}>
        {n.online_users ? formatCount(n.online_users) : '—'}
      </span>
      <span className={css.load}>
        <span className={css.loadBar}>
          <span className={`${css.loadFill} ${cpu !== null && cpu > 85 ? css.hot : cpu !== null && cpu > 65 ? css.warm : ''}`} style={{ width: `${Math.min(100, cpu ?? 0)}%` }} />
        </span>
        <span className={`${css.mono} ${css.faint}`}>{cpu === null ? '—' : `${Math.round(cpu)}%`}</span>
      </span>
      <span className={`${css.mono} ${css.right}`}>{formatBytes(n.traffic_bytes_24h)}</span>
      <span className={css.stateCell}>
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
            {state.filter === 'online' && !n.delivered_to_users && (
              <Tag tone="warn" title={n.delivery_note}>
                不下发
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
    a.picked === b.picked &&
    a.sorting === b.sorting &&
    a.first === b.first &&
    a.last === b.last &&
    a.onPick === b.onPick &&
    a.onOpen === b.onOpen &&
    a.onMove === b.onMove &&
    sameNodeLine(a.node, b.node)
  )
}
