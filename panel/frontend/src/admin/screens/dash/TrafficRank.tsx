import type { UseQueryResult } from '@tanstack/react-query'
import { useState } from 'react'
import { formatBytes } from '../../../core/format'
import { href } from '../../../core/router'
import { Card, Segmented } from '../../../ui'
import { useUserTraffic, type NodeTraffic } from './api'
import type { Target } from './model'
import { ArrowLink, CardEmpty, CardError, PanelSkeleton } from './parts'
import { rankView } from './rank'
import css from './Rank.module.css'

type Tab = 'nodes' | 'users'

const TAB_OPTIONS = [
  { value: 'nodes' as const, label: '节点' },
  { value: 'users' as const, label: '用户' },
]

const DEFAULT_MORE: Record<Tab, { label: string; target: Target }> = {
  nodes: { label: '全部节点', target: { module: 'nodes', tab: 'nodes' } },
  users: { label: '全部用户', target: { module: 'users', tab: 'list' } },
}

export function TrafficRank({
  nodes,
  nodesOnline,
}: {
  nodes: UseQueryResult<NodeTraffic>
  nodesOnline: { online: number; total: number } | undefined
}) {
  const [tab, setTab] = useState<Tab>('nodes')

  const users = useUserTraffic(tab === 'users', nodes.data?.snapshot_at)
  const q = tab === 'nodes' ? nodes : users
  const view = q.data ? rankView(q.data, nodesOnline) : null

  const more = view?.more ?? DEFAULT_MORE[tab]

  return (
    <Card
      flush
      title="流量排行"
      subtitle="近 24 小时"
      extra={
        <Segmented
          size="sm"
          label="排行对象"
          options={TAB_OPTIONS}
          value={tab}
          onChange={setTab}
        />
      }
      footer={
        <>
          <span className={css.footText}>{view?.footer ?? ''}</span>
          <span className={css.footSpacer} />
          <ArrowLink to={more.target}>{more.label}</ArrowLink>
        </>
      }
    >
      <div className={css.body}>
        {q.isError && !q.data ? (
          <CardError what="流量排行" error={q.error} onRetry={() => void q.refetch()} />
        ) : !view ? (
          <div className={css.skeletonWrap}>
            <PanelSkeleton rows={8} height={22} />
          </div>
        ) : view.empty ? (
          <div className={css.emptyWrap}>
            {view.allUnattributed ? (
              <CardEmpty
                title={`有 ${formatBytes(q.data!.totals.reported_bytes)} 流量，但都无法归属到用户`}
                text="上报的订阅已不存在或没有当前用户，可以到节点排行里看这些流量来自哪台节点。"
              />
            ) : (
              <CardEmpty
                title="还没有流量"
                text="节点上线并有用户连接后，这里按节点或用户排出用量"
                link={{ label: '去添加节点', to: { module: 'nodes', tab: 'nodes' } }}
              />
            )}
          </div>
        ) : (
          <>
            <div className={css.headerRow}>
              <span>{tab === 'nodes' ? '节点' : '用户'}</span>
              <span className={css.alignRight}>流量</span>
              <span className={css.alignRight}>占比</span>
            </div>
            <ol className={css.rankList}>
              {view.rows.map((row) => (
                <li key={row.key} className={css.rankItem}>
                  <a href={href(row.path)} className={css.rowLink}>
                    <span className={css.barNameCell}>
                      <i className={css.bar} style={{ width: `${row.barPct}%` }} />
                      <span className={css.nameText}>{row.name}</span>
                    </span>
                    <span className={css.metricVal}>{row.value}</span>
                    <span className={css.percentVal}>{row.share}</span>
                  </a>
                </li>
              ))}
            </ol>
          </>
        )}
      </div>
    </Card>
  )
}

