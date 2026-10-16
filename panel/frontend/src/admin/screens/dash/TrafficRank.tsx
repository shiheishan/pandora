import { InformationCircleIcon, Sorting05Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import type { UseQueryResult } from '@tanstack/react-query'
import { useState } from 'react'
import { formatBytes } from '../../../core/format'
import { href } from '../../../core/router'
import { useUserTraffic, type NodeTraffic } from './api'
import css from './Ops.module.css'
import { ArrowLink, CardEmpty, CardError, PanelHead, PanelSkeleton, TextTabs, ToolSep } from './parts'
import { NEXT_SORT, RANK_SORT_LABEL, rankView, type RankRow, type RankSort } from './rank'

type Tab = 'nodes' | 'users'

const TAB_OPTIONS = [
  { value: 'nodes' as const, label: '节点' },
  { value: 'users' as const, label: '用户' },
]

export function TrafficRank({ nodes }: { nodes: UseQueryResult<NodeTraffic> }) {
  const [tab, setTab] = useState<Tab>('nodes')
  const [sort, setSort] = useState<RankSort>('total')

  const users = useUserTraffic(tab === 'users', nodes.data?.snapshot_at)
  const q = tab === 'nodes' ? nodes : users
  const view = q.data ? rankView(q.data, sort) : null

  const fallbackMore =
    tab === 'nodes'
      ? { module: 'nodes' as const, tab: 'nodes', label: '查看全部节点' }
      : { module: 'users' as const, tab: 'list', label: '查看全部用户' }

  const moreLink = view ? view.more : fallbackMore

  const headHint = (
    <>
      近 24 小时
      {view && (
        <span
          tabIndex={0}
          aria-label="数据说明"
          aria-describedby="dash-traffic-notes"
          className={css.notesWrapper}
        >
          <HugeiconsIcon icon={InformationCircleIcon} size={13} strokeWidth={2} aria-hidden="true" />
          <span role="tooltip" id="dash-traffic-notes" className={css.notesTooltip}>
            {view.notes.map((ln, i) => (
              <span key={i} className={css.notesLine}>
                {ln}
              </span>
            ))}
          </span>
        </span>
      )}
    </>
  )

  return (
    <section className={css.trafficSection} aria-labelledby="dash-traffic">
      <PanelHead id="dash-traffic" title="流量排行" hint={headHint}>
        <button
          type="button"
          onClick={() => setSort(NEXT_SORT[sort])}
          title="切换排序方式"
          className={css.sortBtn}
        >
          <HugeiconsIcon icon={Sorting05Icon} size={13} strokeWidth={2} aria-hidden="true" />
          {RANK_SORT_LABEL[sort]}
        </button>
        <ToolSep />
        <TextTabs label="排行对象" options={TAB_OPTIONS} value={tab} onChange={setTab} />
      </PanelHead>

      {q.isError && !q.data ? (
        <CardError what="流量排行" error={q.error} onRetry={() => void q.refetch()} />
      ) : !view ? (
        <PanelSkeleton rows={6} height={22} />
      ) : view.empty ? (
        view.allUnattributed ? (
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
        )
      ) : (
        <>
          <ol className={css.rankOl}>
            {view.rows.map((row) => (
              <li key={row.key} className={css.rankItem}>
                <RankRowItem row={row} avgPct={view.avgPct} />
              </li>
            ))}
          </ol>

          <div className={css.totalRow}>
            <span aria-hidden="true" className={css.totalSigma}>
              Σ
            </span>
            <div className={css.totalMiddle}>
              <span className={css.totalLabel}>{view.total.label}</span>
              <span className={css.totalSub}>{view.total.sub}</span>
            </div>
            <div className={css.totalRight}>
              <span className={css.totalVal}>{view.total.value}</span>
              <span className={css.totalOf}>{view.total.of}</span>
            </div>
            <div className={css.totalSpacer} />
          </div>
        </>
      )}

      <span className={css.moreLinkWrapper}>
        <ArrowLink to={{ module: moreLink.module, tab: moreLink.tab }}>{moreLink.label}</ArrowLink>
      </span>
    </section>
  )
}

function RankRowItem({ row, avgPct }: { row: RankRow; avgPct: number }) {
  const content = (
    <>
      <span aria-hidden="true" className={css.rankInitials}>
        {row.initials}
      </span>
      <div className={css.rankMiddle}>
        <span className={css.rankRowName}>{row.name}</span>
        <span className={css.rankBarTrack}>
          <span className={css.rankBarFill} style={{ width: `${row.widthPct}%` }} />
          <span className={css.rankAvgTick} style={{ left: `${avgPct}%` }} />
        </span>
      </div>
      <div className={css.rankRightVal}>
        <span className={css.rankMainVal}>{row.value}</span>
        <span className={css.rankShareVal}>{row.share}</span>
      </div>
      <div className={`${css.rankRightDelta} ${row.above ? css.deltaAbove : css.deltaBelow}`}>
        {row.delta}
      </div>

      <div className={css.rowTip} aria-hidden="true">
        <div className={css.rowTipTitle}>{row.tip.title}</div>
        <div className={css.rowTipMain}>{row.tip.main}</div>
        <div className={css.rowTipSub}>{row.tip.sub}</div>
      </div>
    </>
  )

  const ariaLabel = `${row.name} ${row.tip.main}`

  if (row.userId) {
    return (
      <a
        href={href(`/users/list/${encodeURIComponent(row.userId)}`)}
        className={css.rankItemContent}
        aria-label={ariaLabel}
      >
        {content}
      </a>
    )
  }

  return (
    <div tabIndex={0} className={css.rankItemContent} aria-label={ariaLabel}>
      {content}
    </div>
  )
}
