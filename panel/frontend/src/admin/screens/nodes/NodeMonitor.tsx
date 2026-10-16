import { useMemo, useState } from 'react'
import { formatBytes, formatCount } from '../../../core/format'
import { QueryView } from '../../../ui'
import css from './detail.module.css'
import { bandwidthBuckets, heartbeatLabel } from './logic'
import { useNodeMetrics } from './queries'
import type { NodeRow } from './schemas'

const W = 240
const H = 100
const BUCKET_N = 24

/** Y 轴「好看的步长」：1 / 2 / 2.5 / 5 / 10 × 10^n */
function niceTicks(maxMbps: number): { top: number; step: number } {
  const max = Math.max(maxMbps * 1.1, 0.1)
  const raw = max / 3
  const mag = 10 ** Math.floor(Math.log10(raw))
  const nm = raw / mag
  const step = (nm <= 1 ? 1 : nm <= 2 ? 2 : nm <= 2.5 ? 2.5 : nm <= 5 ? 5 : 10) * mag
  const top = Math.ceil(max / step) * step
  return { top, step }
}

/** 单调三次样条折线（设计稿 ch 算法） */
function smoothPath(points: ReadonlyArray<readonly [number, number]>): string {
  if (points.length === 0) return ''
  if (points.length === 1) return `M${points[0][0]},${points[0][1]}`
  const m = points.slice(1).map((p, i) => (p[1] - points[i][1]) / (p[0] - points[i][0] || 1))
  const t = points.map((_, i) => (i === 0 ? m[0]! : i === points.length - 1 ? m[i - 1]! : m[i - 1]! * m[i]! <= 0 ? 0 : 2 / (1 / m[i - 1]! + 1 / m[i]!)))
  let path = `M${points[0][0]},${points[0][1]}`
  for (let i = 0; i < points.length - 1; i++) {
    const dx = (points[i + 1][0] - points[i][0]) / 3
    path += `C${points[i][0] + dx},${points[i][1] + t[i]! * dx} ${points[i + 1][0] - dx},${points[i + 1][1] - t[i + 1]! * dx} ${points[i + 1][0]},${points[i + 1][1]}`
  }
  return path
}

function bucketMbps(b: { mbps: number | null }): number {
  return b.mbps ?? 0
}

function hoverLabel(index: number): string {
  const hoursAgo = BUCKET_N - 1 - index
  if (hoursAgo === 0) return '最近 1 小时'
  return `${hoursAgo} 小时前`
}

export function NodeMonitor({ node }: { node: NodeRow }) {
  const metrics = useNodeMetrics(node.id)
  const [hover, setHover] = useState<number | null>(null)
  const latest = metrics.data?.latest
  const cpu = latest ? latest.cpu_percent : node.cpu_percent

  const buckets = useMemo(() => bandwidthBuckets(metrics.data?.points ?? []), [metrics.data?.points])
  const vals = useMemo(() => buckets.map(bucketMbps), [buckets])
  const maxVal = useMemo(() => Math.max(1, ...vals), [vals])
  const { top, step } = useMemo(() => niceTicks(maxVal), [maxVal])

  const pts = useMemo(() => {
    const x = (i: number) => (i * W) / (BUCKET_N - 1)
    const y = (v: number) => H - (v / top) * H
    return vals.map((v, i) => [x(i), y(v)] as const)
  }, [vals, top])

  const linePath = useMemo(() => smoothPath(pts), [pts])
  const areaPath = linePath ? `${linePath}L${W},${H}L0,${H}Z` : ''

  const tickCount = Math.round(top / step) + 1
  const ticks = useMemo(
    () =>
      Array.from({ length: tickCount }, (_, k) => ({
        yPct: 100 - (k * step / top) * 100,
        label: `${k * step} Mbps`,
      })),
    [tickCount, step, top],
  )

  const cur = hover ?? BUCKET_N - 1
  const curMbps = buckets[cur]?.mbps
  const curDisplay = curMbps == null ? '无数据' : `${curMbps} Mbps`

  return (
    <div>
      <div className={css.kpis}>
        <div className={css.kpi} title={`在线 IP ${formatCount(node.online_ips)}`}>
          <div className={css.kpiLabel}>在线用户</div>
          <div className={css.kpiValue}>{node.online_users ? formatCount(node.online_users) : '—'}</div>
        </div>
        <div className={css.kpi}>
          <div className={css.kpiLabel}>CPU</div>
          <div className={css.kpiValue}>{cpu == null ? '—' : `${Math.round(cpu)}%`}</div>
        </div>
        <div className={css.kpi}>
          <div className={css.kpiLabel}>24h 流量</div>
          <div className={css.kpiValue}>{formatBytes(node.traffic_bytes_24h)}</div>
        </div>
        <div className={css.kpi}>
          <div className={css.kpiLabel}>最近心跳</div>
          <div className={css.kpiValue}>{node.last_heartbeat_at ? heartbeatLabel(node.last_heartbeat_at) : '从未'}</div>
        </div>
      </div>

      <div className={css.chartBlock}>
        <div className={css.chartHead}>
          <span className={css.chartTitle}>近 24 小时带宽</span>
          <span className={css.chartCur}>
            <span className={css.chartCurLine} aria-hidden="true" />
            <span className={css.chartCurLabel}>{hoverLabel(cur)}</span>
            <span className={css.chartCurVal}>{curDisplay}</span>
          </span>
        </div>
        <QueryView query={metrics} rows={1} isEmpty={(d) => d.points.length === 0} empty={<div className={css.faint}>还没有探针数据：节点从未心跳，或探针挂在服务器的控制节点上。</div>}>
          {() => (
            <div className={css.chartGrid} role="img" aria-label="近 24 小时带宽曲线">
              <div className={css.yAxis}>
                {ticks.map((t) => (
                  <span key={t.yPct} className={css.yTick} style={{ top: `${t.yPct}%` }}>
                    {t.label}
                  </span>
                ))}
              </div>
              <div className={css.plot} onMouseLeave={() => setHover(null)}>
                {ticks.map((t) => (
                  <span key={`g-${t.yPct}`} className={css.hGrid} style={{ top: `${t.yPct}%` }} />
                ))}
                <svg className={css.plotSvg} viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none">
                  <defs>
                    <linearGradient id={`ndTrafficFill-${node.id}`} x1="0" y1="0" x2="0" y2="1">
                      <stop offset="0%" stopColor="var(--text)" stopOpacity={0.12} />
                      <stop offset="100%" stopColor="var(--text)" stopOpacity={0} />
                    </linearGradient>
                  </defs>
                  {areaPath && <path d={areaPath} fill={`url(#ndTrafficFill-${node.id})`} />}
                  {linePath && (
                    <path d={linePath} fill="none" stroke="var(--text)" strokeWidth={1.75} strokeLinecap="round" strokeLinejoin="round" vectorEffect="non-scaling-stroke" />
                  )}
                </svg>
                {hover != null && (
                  <>
                    <span className={css.hoverV} style={{ left: `${(cur / (BUCKET_N - 1)) * 100}%` }} />
                    <span className={css.hoverDot} style={{ left: `${(cur / (BUCKET_N - 1)) * 100}%`, top: `${pts[cur]?.[1] ?? 0}%` }} />
                  </>
                )}
                <div className={css.hitRow}>
                  {vals.map((_, i) => (
                    <span key={i} className={css.hitCell} onMouseEnter={() => setHover(i)} title={buckets[i]?.mbps == null ? '无数据' : `${buckets[i]!.mbps} Mbps`} />
                  ))}
                </div>
              </div>
              <span />
              <div className={css.xLabels} style={{ gridColumn: 2 }}>
                <span>-24h</span>
                <span>-18h</span>
                <span>-12h</span>
                <span>-6h</span>
                <span>现在</span>
              </div>
            </div>
          )}
        </QueryView>
      </div>
    </div>
  )
}
