/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../core/format 的 formatMoney，依赖 ../../../ui 的 Empty / Segmented / Skeleton，依赖 ./api 的 useRevenue 与币种 / 区间类型，依赖 ./model 的 revenueSummary / formatPercent，依赖 ./parts，依赖 ./Dash.module.css
 * [OUTPUT]: 对外提供 RevenueTrend
 * [POS]: 仪表盘「收入趋势」面板：GET v1/revenue/timeseries（CNY/USD × 7/30/90 天），区间合计、日均、较上一区间（previous_total 待补·后端，缺失显示 —），手写柱图，最后一根是今天；切换时保留上一张图不闪骨架
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
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
