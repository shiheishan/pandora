import { ArrowDownRight01Icon, ArrowUpRight01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useMemo, useState, type KeyboardEvent, type PointerEvent } from 'react'
import { formatMoney } from '../../../core/format'
import { Skeleton } from '../../../ui'
import { useRevenue, type RevenueDays } from './api'
import { hoverIndex, revenueChart, REVENUE_VIEW } from './chart'
import css from './Charts.module.css'
import { CardEmpty, CardError, PanelHead, TextTabs, toneText } from './parts'

const DAY_OPTIONS = [
  { value: '7', label: '7 天' },
  { value: '30', label: '30 天' },
  { value: '90', label: '90 天' },
] as const

function bubbleShift(xPct: number): string {
  if (xPct > 85) return 'translateX(-90%)'
  if (xPct < 15) return 'translateX(-10%)'
  return 'translateX(-50%)'
}

export function RevenueTrend() {
  const [days, setDays] = useState<RevenueDays>(30)
  const q = useRevenue(days)
  const [hover, setHover] = useState<number | null>(null)

  const chart = useMemo(() => (q.data ? revenueChart(q.data.points, q.data.previous_total) : null), [q.data])

  const marks = chart?.marks ?? []
  const activeMark = hover !== null && hover >= 0 && hover < marks.length ? marks[hover] : null

  const onPointerMove = (e: PointerEvent<HTMLDivElement>) => {
    const rect = e.currentTarget.getBoundingClientRect()
    setHover(hoverIndex(e.clientX - rect.left, rect.width, marks.length))
  }

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const n = marks.length
    if (n === 0) return
    if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
      e.preventDefault()
      setHover((prev) => {
        if (prev === null) return n - 1
        const next = e.key === 'ArrowRight' ? prev + 1 : prev - 1
        return Math.max(0, Math.min(n - 1, next))
      })
    } else if (e.key === 'Escape') {
      setHover(null)
    }
  }

  return (
    <section className={css.panel} aria-labelledby="dash-revenue">
      <PanelHead id="dash-revenue" title="每日净收入">
        <TextTabs
          label="区间"
          options={DAY_OPTIONS}
          value={String(days) as '7' | '30' | '90'}
          onChange={(v) => setDays(Number(v) as RevenueDays)}
        />
      </PanelHead>

      {q.isError && !chart ? (
        <CardError what="每日净收入" error={q.error} onRetry={() => void q.refetch()} />
      ) : !chart ? (
        <div className={css.loading}>
          <Skeleton height={260} />
        </div>
      ) : chart.empty ? (
        <CardEmpty
          title="还没有收入"
          text="第一笔订单支付后，这里会画出每天的净收入"
          link={{ label: '去配置套餐', to: { module: 'plans', tab: null } }}
        />
      ) : (
        <>
          <div className={css.statsRevenue}>
            <div className={css.mainStat}>
              <span className={css.statLabel}>区间合计 · 近 {q.data?.days ?? days} 天</span>
              <div className={css.bigNumWrap}>
                <span className={css.bigNum}>{formatMoney(chart.total)}</span>
                <span
                  title="较上一区间"
                  className={chart.delta === null ? css.delta : `${css.delta} ${toneText(chart.up ? 'ok' : 'danger') ?? ''}`}
                >
                  {chart.delta !== null && (
                    <HugeiconsIcon
                      icon={chart.up ? ArrowUpRight01Icon : ArrowDownRight01Icon}
                      size={12}
                      strokeWidth={2.25}
                      aria-hidden="true"
                    />
                  )}
                  {chart.deltaText}
                </span>
              </div>
              <span className={css.subText}>上一区间 {formatMoney(chart.previousTotal)}</span>
            </div>

            <span className={css.spacer} />

            <div className={css.subStats}>
              <div>
                <div className={css.statLabel}>日均</div>
                <div className={css.subStatVal}>{formatMoney(chart.average)}</div>
              </div>
              <div>
                <div className={css.statLabel}>峰值 · {chart.peak.label}</div>
                <div className={css.subStatVal}>{formatMoney(chart.peak.value)}</div>
              </div>
            </div>
          </div>

          <div className={css.plotWrapRevenue}>
            {chart.ticks.map((t) => (
              <div key={t.topPct} className={css.tickRow} style={{ top: `${t.topPct}%` }}>
                <span className={css.tickLine} />
                <span className={css.tickLabel}>{t.label}</span>
              </div>
            ))}
            <div
              className={css.plotArea}
              tabIndex={0}
              role="img"
              aria-label={`近 ${days} 天每日净收入趋势`}
              onPointerMove={onPointerMove}
              onPointerLeave={() => setHover(null)}
              onKeyDown={onKeyDown}
              onBlur={() => setHover(null)}
            >
              <svg
                className={css.svg}
                viewBox={`0 0 ${REVENUE_VIEW.width} ${REVENUE_VIEW.height}`}
                preserveAspectRatio="none"
                aria-hidden="true"
              >
                <path
                  d={chart.path}
                  fill="none"
                  stroke="var(--text)"
                  strokeWidth={2}
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  vectorEffect="non-scaling-stroke"
                />
              </svg>
              {activeMark && (
                <>
                  <span className={css.guide} style={{ left: `${activeMark.xPct}%` }} aria-hidden="true" />
                  <span
                    className={css.markWrap}
                    style={{ left: `${activeMark.xPct}%`, top: `${activeMark.yPct}%` }}
                    aria-hidden="true"
                  >
                    <span className={css.dotMain} />
                    <span className={css.bubble} style={{ transform: bubbleShift(activeMark.xPct) }}>
                      {activeMark.label}
                    </span>
                  </span>
                </>
              )}
            </div>
          </div>

          <div className={`${css.axis} ${css.axisRevenue}`}>
            <span>{chart.axis[0]}</span>
            <span>{chart.axis[1]}</span>
            <span>今天</span>
          </div>

          <span className={css.srOnly} aria-live="polite">
            {activeMark ? activeMark.label : ''}
          </span>
        </>
      )}
    </section>
  )
}
