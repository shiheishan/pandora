/**
 * [INPUT]: 依赖 @tanstack/react-query 的 UseQueryResult，依赖 react 的 useState，依赖 ../../../core/format 的 formatBytes，依赖 ../../../core/router 的 href，依赖 ../../../ui 的 Empty / Segmented，依赖 ../../modules 的 Permissions / canRead / modulePath，依赖 ./api 的 useUserTraffic 与 NodeTraffic，依赖 ./model 的 trafficRows / trafficNotes，依赖 ./parts，依赖 ./Dash.module.css
 * [OUTPUT]: 对外提供 TrafficRank
 * [POS]: 仪表盘「流量排行 · 近 24 小时」面板：节点 / 用户两个页签（按 metering.read + node.read / iam.user.read 分别出现），前 5 名。节点页签的数据与「流量」KPI 共用一条查询；用户页签以节点查询的 snapshot_at 为锚点（DASH-01）。用户只显示 email_masked（待决 D-A-2 未决前），行点进用户详情；底部小字给未归属流量与质量计数
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { formatBytes } from '../../../core/format'
import type { UseQueryResult } from '@tanstack/react-query'
import { useState } from 'react'
import { href } from '../../../core/router'
import { Empty, Segmented } from '../../../ui'
import { canRead, modulePath, type Permissions } from '../../modules'
import { useUserTraffic, type NodeTraffic, type UserTraffic } from './api'
import css from './Dash.module.css'
import { trafficNotes, trafficRows } from './model'
import { CardError, isForbidden, PanelSkeleton } from './parts'

type Tab = 'nodes' | 'users'

export function TrafficRank({ perms, nodes, canNodes, canUsers }: { perms: Permissions; nodes: UseQueryResult<NodeTraffic>; canNodes: boolean; canUsers: boolean }) {
  const nodesVisible = canNodes && !(nodes.isError && isForbidden(nodes.error))
  const [picked, setPicked] = useState<Tab>(nodesVisible ? 'nodes' : 'users')
  const tab: Tab = picked === 'nodes' && !nodesVisible ? 'users' : picked === 'users' && !canUsers ? 'nodes' : picked
  const users = useUserTraffic(canUsers && tab === 'users', nodes.data?.snapshot_at)
  const usersVisible = canUsers && !(users.isError && isForbidden(users.error))
  if (!nodesVisible && !usersVisible) return null

  const options = [
    ...(nodesVisible ? [{ value: 'nodes' as const, label: '节点' }] : []),
    ...(usersVisible ? [{ value: 'users' as const, label: '用户' }] : []),
  ]
  const q = tab === 'nodes' ? nodes : users
  const more = tab === 'nodes' ? { module: 'nodes' as const, tab: 'nodes', label: '查看全部节点' } : { module: 'users' as const, tab: 'list', label: '查看全部用户' }

  return (
    <section className={css.panel} aria-labelledby="dash-traffic">
      <div className={css.panelHead}>
        <h2 id="dash-traffic" className={css.panelTitle}>
          流量排行
        </h2>
        <span className={css.hint}>近 24 小时</span>
        <div className={css.spacer} />
        {options.length > 1 && <Segmented size="sm" label="排行对象" options={options} value={tab} onChange={setPicked} />}
      </div>
      {q.isError && !q.data ? (
        <CardError what="流量排行" error={q.error} onRetry={() => void q.refetch()} />
      ) : !q.data ? (
        <PanelSkeleton rows={5} height={22} />
      ) : (
        <RankBody data={q.data} perms={perms} />
      )}
      {canRead(more.module, more.tab, perms) && (
        <a className={css.more} href={href(modulePath(more.module, more.tab, perms))}>
          {more.label}
        </a>
      )}
    </section>
  )
}

function RankBody({ data, perms }: { data: NodeTraffic | UserTraffic; perms: Permissions }) {
  const rows = trafficRows(data)
  const { notes, allUnattributed } = trafficNotes(data)
  const userLinks = canRead('users', 'list', perms)
  return (
    <>
      {rows.length === 0 ? (
        allUnattributed ? (
          <Empty bare title={`有 ${formatBytes(data.totals.reported_bytes)} 流量，但都无法归属到用户`} description="上报的订阅已不存在或没有当前用户，可以到节点排行里看这些流量来自哪台节点。" />
        ) : (
          <Empty bare title="所选时间范围暂无非零严格有效流量条目" description="节点上报流量后，这里会列出前 5 名。" />
        )
      ) : (
        <ol className={css.rankList}>
          {rows.map((r) => {
            const body = (
              <>
                <span className={css.rankIndex}>{r.rank}</span>
                <span className={css.rankMain}>
                  <span className={css.rankName}>{r.name}</span>
                  <span className={css.track}>
                    <span className={css.fill} style={{ width: `${r.width}%` }} />
                  </span>
                </span>
                <span className={css.rankValue}>{r.value}</span>
              </>
            )
            return (
              <li key={r.key}>
                {r.userId && userLinks ? (
                  <a className={css.rankRow} href={href(`/users/list/${encodeURIComponent(r.userId)}`)} title={r.tip}>
                    {body}
                  </a>
                ) : (
                  <div className={css.rankRow} title={r.tip}>
                    {body}
                  </div>
                )}
              </li>
            )
          })}
        </ol>
      )}
      <p className={css.notes}>{[...notes, '来自节点原始上报，不是计费账本'].join(' · ')}</p>
    </>
  )
}
