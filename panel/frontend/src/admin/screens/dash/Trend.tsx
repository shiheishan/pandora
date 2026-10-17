import { useMemo, useState, useId, type KeyboardEvent, type PointerEvent } from 'react'
import { formatCount, formatMoney } from '../../../core/format'
import { Card, Segmented, Skeleton } from '../../../ui'
import { useRevenue, useActivity, type RevenueDays } from './api'
import {
  activityChart,
  barHoverIndex,
  hoverIndex,
  revenueChart,
  REVENUE_VIEW,
  ACTIVITY_VIEW,
} from './chart'
import css from './Charts.module.css'
import { CardEmpty, CardError, Section } from './parts'

const DAY_OPTIONS = [
  { value: '7', label: '7 天' },
  { value: '30', label: '30 天' },
  { value: '90', label: '90 天' },
] as const

type Metric = 'rev' | 'reg'

type DayValue = '7' | '30' | '90'

function bubbleShift(xPct: number): string {
  if (xPct > 85) return 'translateX(-90%)'
  if (xPct < 15) return 'translateX(-10%)'
  return 'translateX(-50%)'
}

function DaysSegmented({ days, onDays }: { days: RevenueDays; onDays: (d: RevenueDays) => void }) {
  return (
    <Segmented
      size="sm"
      label="区间"
      options={DAY_OPTIONS}
      value={String(days) as DayValue}
      onChange={(v) => onDays(Number(v) as RevenueDays)}
    />
  )
}

function RevenuePanel({ days, onDays }: { days: RevenueDays; onDays: (d: RevenueDays) => void }) {
  const q = useRevenue(days)
  const [hover, setHover] = useState<number | null>(null)
  const gradId = useId().replace(/:/g, '')

  const chart = useMemo(() => (q.data ? revenueChart(q.data.points, q.data.previous_total) : null), [q.data])
  const marks = chart?.marks ?? []
  const activeMark = hover !== null && hover >= 0 && hover < marks.length ? marks[hover] : null
  const showStats = chart != null && !chart.empty

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

  const lastMark = marks.length > 0 ? marks[marks.length - 1]! : null

  return (
    <>
      <div className={css.summaryBar}>
        {showStats && chart && (
          <div className={css.summarySum}>
            <span>近 {days} 天净收入</span>
            <b className={css.summaryBold}>{formatMoney(chart.total)}</b>
            {chart.deltaText !== '' && (
              <span>
                较前 {days} 天{' '}
                <span className={chart.up ? css.deltaUp : css.deltaDown}>{chart.deltaText}</span>
              </span>
            )}
          </div>
        )}
        <span className={css.barSpacer} />
        <DaysSegmented days={days} onDays={onDays} />
      </div>

      {q.isError && !chart ? (
        <CardError what="收入趋势" error={q.error} onRetry={() => void q.refetch()} />
      ) : !chart ? (
        <div className={css.loading}>
          <Skeleton height={280} />
        </div>
      ) : chart.empty ? (
        <CardEmpty
          title="还没有收入"
          text="第一笔订单支付后，这里会画出每天的净收入"
          link={{ label: '去配置套餐', to: { module: 'plans', tab: null } }}
        />
      ) : (
        <div className={css.chartWrap}>
          <div className={css.plotBlock}>
            {chart.ticks.map((t) => (
              <div key={t.topPct} className={css.tickRow} style={{ top: `${t.topPct}%` }}>
                <span className={css.tickLabel}>{t.label}</span>
                <span className={css.tickLine} />
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
                <defs>
                  <linearGradient id={gradId} x1="0" x2="0" y1="0" y2="1">
                    <stop offset="0%" stopColor="var(--info)" stopOpacity={0.25} />
                    <stop offset="100%" stopColor="var(--info)" stopOpacity={0} />
                  </linearGradient>
                </defs>
                <path d={chart.area} fill={`url(#${gradId})`} />
                <path
                  d={chart.path}
                  fill="none"
                  stroke="var(--info)"
                  strokeWidth={2}
                  vectorEffect="non-scaling-stroke"
                />
              </svg>
              {lastMark && hover === null && (
                <span
                  className={css.markWrap}
                  style={{ left: `${lastMark.xPct}%`, top: `${lastMark.yPct}%` }}
                  aria-hidden="true"
                >
                  <span className={css.dotInfo} />
                </span>
              )}
              {activeMark && (
                <>
                  <span className={css.guide} style={{ left: `${activeMark.xPct}%` }} aria-hidden="true" />
                  <span
                    className={css.markWrap}
                    style={{ left: `${activeMark.xPct}%`, top: `${activeMark.yPct}%` }}
                    aria-hidden="true"
                  >
                    <span className={css.dotInfo} />
                    <span className={css.bubble} style={{ transform: bubbleShift(activeMark.xPct) }}>
                      {activeMark.label}
                    </span>
                  </span>
                </>
              )}
            </div>
          </div>
          <div className={`${css.axis} ${css.axisThree}`}>
            <span>{chart.axis[0]}</span>
            <span>{chart.axis[1]}</span>
            <span>今天</span>
          </div>
          <span className={css.srOnly} aria-live="polite">
            {activeMark ? activeMark.label : ''}
          </span>
        </div>
      )}
    </>
  )
}

function ActivityPanel({ days, onDays }: { days: RevenueDays; onDays: (d: RevenueDays) => void }) {
  const q = useActivity(days)
  const [hover, setHover] = useState<number | null>(null)

  const chart = useMemo(() => (q.data ? activityChart(q.data.points) : null), [q.data])
  const marks = chart?.marks ?? []
  const activeMark = hover !== null && hover >= 0 && hover < marks.length ? marks[hover] : null
  const showStats = chart != null && !chart.empty

  const onPointerMove = (e: PointerEvent<HTMLDivElement>) => {
    const rect = e.currentTarget.getBoundingClientRect()
    setHover(barHoverIndex(e.clientX - rect.left, rect.width, marks.length))
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
    <>
      <div className={css.summaryBar}>
        {showStats && chart && (
          <div className={css.summarySum}>
            <span>近 {days} 天新注册</span>
            <b className={css.summaryBold}>{formatCount(chart.registeredTotal)}</b>
            <span>日活均值 {formatCount(chart.activeAverage)}</span>
          </div>
        )}
        <span className={css.barSpacer} />
        {showStats && (
          <>
            <span className={css.legend}>
              <i className={css.legendBox} aria-hidden="true" />
              每日注册
            </span>
            <span className={css.legend}>
              <i className={css.legendDash} aria-hidden="true" />
              日活
            </span>
          </>
        )}
        <DaysSegmented days={days} onDays={onDays} />
      </div>

      {q.isError && !chart ? (
        <CardError what="注册与日活" error={q.error} onRetry={() => void q.refetch()} />
      ) : !chart ? (
        <div className={css.loading}>
          <Skeleton height={280} />
        </div>
      ) : chart.empty ? (
        <CardEmpty
          title="还没有用户"
          text="有人注册或登录后，这里会显示每天的新用户与日活"
          link={{ label: '去设置注册与邀请', to: { module: 'marketing', tab: 'signup' } }}
        />
      ) : (
        <div className={css.chartWrap}>
          <div className={css.plotBlock}>
            {chart.ticks.map((t) => (
              <div key={t.topPct} className={css.tickRow} style={{ top: `${t.topPct}%` }}>
                <span className={css.tickLabel}>{t.label}</span>
                <span className={css.tickLine} />
              </div>
            ))}
            <div
              className={css.plotArea}
              tabIndex={0}
              role="img"
              aria-label={`近 ${days} 天每日注册与日活趋势`}
              onPointerMove={onPointerMove}
              onPointerLeave={() => setHover(null)}
              onKeyDown={onKeyDown}
              onBlur={() => setHover(null)}
            >
              {chart.bars.map((bar, i) => (
                <span
                  key={i}
                  className={bar.last ? `${css.bar} ${css.barLast}` : css.bar}
                  style={{
                    left: `${bar.xPct}%`,
                    width: `${bar.wPct}%`,
                    height: `${bar.hPct}%`,
                  }}
                  aria-hidden="true"
                />
              ))}
              <svg
                className={css.svg}
                viewBox={`0 0 ${ACTIVITY_VIEW.width} ${ACTIVITY_VIEW.height}`}
                preserveAspectRatio="none"
                aria-hidden="true"
              >
                <path
                  d={chart.activePath}
                  fill="none"
                  stroke="var(--text-3)"
                  strokeWidth={1.5}
                  strokeDasharray="5 4"
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
                    <span className={css.dotInfo} />
                    <span className={css.bubble} style={{ transform: bubbleShift(activeMark.xPct) }}>
                      {activeMark.label}
                    </span>
                  </span>
                </>
              )}
            </div>
          </div>
          <div className={css.axis}>
            <span>{chart.axisStart}</span>
            <span>今天</span>
          </div>
          <span className={css.srOnly} aria-live="polite">
            {activeMark ? activeMark.label : ''}
          </span>
        </div>
      )}
    </>
  )
}

export function Trend() {
  const [metric, setMetric] = useState<Metric>('rev')
  const [days, setDays] = useState<RevenueDays>(30)

  return (
    <Section
      id="dash-trend"
      title="趋势"
      extra={
        <Segmented
          size="sm"
          label="趋势指标"
          options={[
            { value: 'rev', label: '收入' },
            { value: 'reg', label: '注册与日活' },
          ]}
          value={metric}
          onChange={setMetric}
        />
      }
    >
      <Card flush>
        {metric === 'rev' ? <RevenuePanel days={days} onDays={setDays} /> : <ActivityPanel days={days} onDays={setDays} />}
      </Card>
    </Section>
  )
}
