import { useMemo, useState, type KeyboardEvent, type PointerEvent } from 'react'
import { formatCount } from '../../../core/format'
import { Skeleton } from '../../../ui'
import { useActivity } from './api'
import { activityChart, hoverIndex, ACTIVITY_VIEW } from './chart'
import css from './Charts.module.css'
import { CardEmpty, CardError, PanelHead } from './parts'

function bubbleShift(xPct: number): string {
  if (xPct > 85) return 'translateX(-90%)'
  if (xPct < 15) return 'translateX(-10%)'
  return 'translateX(-50%)'
}

export function Activity() {
  const q = useActivity()
  const [hover, setHover] = useState<number | null>(null)

  const chart = useMemo(() => (q.data ? activityChart(q.data.points) : null), [q.data])

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
    <section className={css.panel} aria-labelledby="dash-activity">
      <PanelHead id="dash-activity" title="新用户与日活" hint="近 14 天" />

      {q.isError && !chart ? (
        <CardError what="新用户与日活" error={q.error} onRetry={() => void q.refetch()} />
      ) : !chart ? (
        <div className={css.loading}>
          <Skeleton height={240} />
        </div>
      ) : chart.empty ? (
        <CardEmpty
          title="还没有用户"
          text="有人注册或登录后，这里会显示每天的新用户与日活"
          link={{ label: '去设置注册与邀请', to: { module: 'marketing', tab: 'signup' } }}
        />
      ) : (
        <>
          <div className={css.statsActivity}>
            <div className={css.actStat}>
              <span className={css.actLegend}>
                <span className={css.legendLineReg} aria-hidden="true" />
                14 日注册
              </span>
              <span className={css.actNum}>{formatCount(chart.registeredTotal)}</span>
            </div>
            <div className={css.actStat}>
              <span className={css.actLegend}>
                <span className={css.legendLineAct} aria-hidden="true" />
                日活均值
              </span>
              <span className={css.actNum}>{formatCount(chart.activeAverage)}</span>
            </div>
          </div>

          <div
            className={css.plotActivity}
            tabIndex={0}
            role="img"
            aria-label="近 14 天每日注册与活跃趋势"
            onPointerMove={onPointerMove}
            onPointerLeave={() => setHover(null)}
            onKeyDown={onKeyDown}
            onBlur={() => setHover(null)}
          >
            <div className={css.gridLineTop} aria-hidden="true" />
            <div className={css.gridLineMid} aria-hidden="true" />
            <svg
              className={css.svg}
              viewBox={`0 0 ${ACTIVITY_VIEW.width} ${ACTIVITY_VIEW.height}`}
              preserveAspectRatio="none"
              aria-hidden="true"
            >
              <path
                d={chart.activePath}
                fill="none"
                stroke="var(--border-hover)"
                strokeWidth={2}
                strokeLinecap="round"
                strokeLinejoin="round"
                vectorEffect="non-scaling-stroke"
              />
              <path
                d={chart.registeredPath}
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
                  style={{ left: `${activeMark.xPct}%`, top: `${activeMark.actYPct}%` }}
                  aria-hidden="true"
                >
                  <span className={css.dotAct} />
                </span>
                <span
                  className={css.markWrap}
                  style={{ left: `${activeMark.xPct}%`, top: `${activeMark.regYPct}%` }}
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

          <div className={css.axis}>
            <span>{chart.axisStart}</span>
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
