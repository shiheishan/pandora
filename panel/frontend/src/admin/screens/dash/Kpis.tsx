/**
 * [INPUT]: 依赖 ../../../core/format 的 formatMoney，依赖 ../../../ui 的 Skeleton，依赖 ../../modules 的 Permissions，依赖 ./api 的 Overview / NodeTraffic 与查询结果类型，依赖 ./model 的 kpiRevenueDelta / reachable / formatBytes / formatCount，依赖 ./parts，依赖 ./Dash.module.css
 * [OUTPUT]: 对外提供 Kpis
 * [POS]: 仪表盘第二块「经营」四格 KPI：收入按币种各一格（GET v1/overview）、有效订阅、近 24 小时流量（节点流量排行查询的 totals，与排行卡共用一次请求）；待补·前端的调账、试用、即将到期、待支付放进格子 tooltip
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { UseQueryResult } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { formatMoney } from '../../../core/format'
import { Skeleton } from '../../../ui'
import type { Permissions } from '../../modules'
import type { NodeTraffic, Overview } from './api'
import css from './Dash.module.css'
import { formatBytes, formatCount, kpiRevenueDelta, reachable, type Target, type Tone } from './model'
import { CardError, isForbidden, targetHref, toneText } from './parts'

interface Tile {
  key: string
  label: string
  value: string
  sub: string
  subTone?: Tone
  tip?: string
  target: Target
}

function signedMoney(amount: number, currency: string): string {
  return `${amount > 0 ? '+' : ''}${formatMoney(amount, currency)}`
}

function overviewTiles(o: Overview): Tile[] {
  const tiles: Tile[] = o.revenue.map((r) => {
    const delta = kpiRevenueDelta(r.today, r.yesterday)
    const tip = [
      r.adjustment_today !== 0 ? `含调整 ${signedMoney(r.adjustment_today, r.currency)}` : '',
      `近 7 天 ${formatMoney(r.last_7_days, r.currency)}`,
      `近 30 天 ${formatMoney(r.last_30_days, r.currency)}`,
      `今日已付订单 ${formatCount(o.orders.paid_today)} · 待支付 ${formatCount(o.orders.pending)} · 今日失败 ${formatCount(o.orders.failed_today)}`,
    ]
      .filter(Boolean)
      .join('\n')
    return {
      key: `rev-${r.currency}`,
      label: `收入 · ${r.currency}`,
      value: formatMoney(r.today, r.currency),
      sub: r.adjustment_today !== 0 ? `${delta.text} · 含调整` : delta.text,
      subTone: delta.tone,
      tip,
      target: { module: 'billing', tab: 'orders' },
    }
  })
  const s = o.subscriptions
  tiles.push({
    key: 'subs',
    label: '有效订阅',
    value: formatCount(s.active),
    sub: s.new_7_days !== undefined ? `本周新增 ${formatCount(s.new_7_days)}` : `试用中 ${formatCount(s.trialing)}`,
    tip: `试用中 ${formatCount(s.trialing)} · 7 天内到期 ${formatCount(s.expiring_7_days)} · 已过期 ${formatCount(s.expired)}`,
    target: { module: 'users', tab: 'list' },
  })
  return tiles
}

function trafficTile(traffic: NodeTraffic | undefined, nodes: Overview['nodes'] | undefined): Tile {
  return {
    key: 'traffic',
    label: '流量 · 近 24 小时',
    value: traffic ? formatBytes(traffic.totals.reported_bytes) : '—',
    sub: nodes ? `${formatCount(nodes.online)} / ${formatCount(nodes.total)} 节点在线` : '',
    tip: '节点原始上报的合计，不是计费账本',
    target: { module: 'nodes', tab: 'nodes' },
  }
}

export function Kpis({
  perms,
  overview,
  traffic,
  canOverview,
  canTraffic,
}: {
  perms: Permissions
  overview: UseQueryResult<Overview>
  traffic: UseQueryResult<NodeTraffic>
  canOverview: boolean
  canTraffic: boolean
}) {
  const showOverview = canOverview && !(overview.isError && isForbidden(overview.error))
  const showTraffic = canTraffic && !(traffic.isError && isForbidden(traffic.error))
  if (!showOverview && !showTraffic) return null

  const loading = (showOverview && overview.isPending) || (showTraffic && traffic.isPending)
  const tiles: Tile[] = []
  if (showOverview && overview.data) tiles.push(...overviewTiles(overview.data))
  if (showTraffic || overview.data?.nodes) tiles.push(trafficTile(traffic.data, overview.data?.nodes))

  // 流量查询失败不在这里报：同一条查询的错误与重试由流量排行卡给出，这一格显示 —
  let content: ReactNode
  if (loading) {
    content = <Skeleton height={82} radius="var(--radius-lg)" />
  } else {
    content = (
      <>
        {showOverview && overview.isError && <CardError boxed what="经营总览" error={overview.error} onRetry={() => void overview.refetch()} />}
        {tiles.length > 0 && (
          <div className={css.kpis}>
            {tiles.map((t) => (
              <KpiTile key={t.key} tile={t} perms={perms} />
            ))}
          </div>
        )}
      </>
    )
  }

  return (
    <section className={css.section} aria-labelledby="dash-kpis">
      <div className={css.sectionHead}>
        <h2 id="dash-kpis" className={css.sectionTitle}>
          经营
        </h2>
        <span className={css.hint}>今日 · 币种分开计算，不做汇率折算</span>
      </div>
      {content}
    </section>
  )
}

function KpiTile({ tile, perms }: { tile: Tile; perms: Permissions }) {
  const body = (
    <>
      <span className={css.kpiLabel}>{tile.label}</span>
      <span className={css.kpiValue}>{tile.value}</span>
      <span className={`${css.kpiSub} ${(tile.subTone && toneText(tile.subTone)) ?? ''}`}>{tile.sub || ' '}</span>
    </>
  )
  const target = reachable(tile.target, perms)
  return target ? (
    <a className={css.kpi} href={targetHref(target, perms)} title={tile.tip}>
      {body}
    </a>
  ) : (
    <div className={css.kpi} title={tile.tip}>
      {body}
    </div>
  )
}
