import { useState } from 'react'
import { formatMoney } from '../../../core/format'
import { Empty, Segmented, Skeleton } from '../../../ui'
import { useRevenue, type RevenueCurrency, type RevenueDays } from './api'
import css from './Dash.module.css'
import { formatPercent, revenueSummary } from './model'
import { CardError, isForbidden, toneText } from './parts'

const CURRENCIES = [
  { value: 'CNY', label: 'CNY' },
  { value: 'USD', label: 'USD' },
] as const
const RANGES = [
  { value: '7', label: '7 天' },
  { value: '30', label: '30 天' },
  { value: '90', label: '90 天' },
] as const

export function RevenueTrend() {
  const [cur, setCur] = useState<RevenueCurrency>('CNY')
  const [days, setDays] = useState<RevenueDays>(30)
  const q = useRevenue(true, cur, days)
  if (q.isError && isForbidden(q.error)) return null

  const data = q.data
  const summary = data ? revenueSummary(data.points, data.currency, data.previous_total) : null

  return (
    <section className={`${css.panel} ${css.wide}`} aria-labelledby="dash-revenue">
      <div className={css.panelHead}>
        <h2 id="dash-revenue" className={css.panelTitle}>
          收入趋势
        </h2>
        <div className={css.spacer} />
        <Segmented size="sm" label="币种" options={CURRENCIES} value={cur} onChange={setCur} />
        <Segmented size="sm" label="区间" options={RANGES} value={String(days) as '7' | '30' | '90'} onChange={(v) => setDays(Number(v) as RevenueDays)} />
      </div>
      {q.isError && !data ? (
        <CardError what="收入趋势" error={q.error} onRetry={() => void q.refetch()} />
      ) : !data || !summary ? (
        <div className={css.panelBody}>
          <Skeleton height={228} />
        </div>
      ) : (
        <>
          <div className={css.stats}>
            <Stat label="区间合计" value={formatMoney(summary.total, data.currency)} />
            <Stat label="日均" value={formatMoney(summary.average, data.currency)} />
            <Stat
              label="较上一区间"
              value={summary.delta === null ? '—' : formatPercent(summary.delta)}
              className={summary.delta === null ? undefined : toneText(summary.delta >= 0 ? 'ok' : 'danger')}
            />
          </div>
          {summary.empty ? (
            <Empty bare title={`近 ${data.days} 天没有收入`} description="有订单支付或登记收入调整后，这里按天显示净收入。" />
          ) : (
            <>
              <div className={`${css.bars} ${data.points.length > 30 ? css.dense : ''}`} role="img" aria-label={`近 ${data.days} 天每日净收入柱图`}>
                {summary.bars.map((b) => (
                  <div key={b.key} className={b.today ? `${css.bar} ${css.barToday}` : css.bar} style={{ height: `${b.height}%` }} title={b.tip} />
                ))}
              </div>
              <div className={css.axis}>
                <span>{data.days} 天前</span>
                <span>今天</span>
              </div>
            </>
          )}
        </>
      )}
    </section>
  )
}

function Stat({ label, value, className }: { label: string; value: string; className?: string }) {
  return (
    <div>
      <div className={css.hint}>{label}</div>
      <div className={className ? `${css.statValue} ${className}` : css.statValue}>{value}</div>
    </div>
  )
}
