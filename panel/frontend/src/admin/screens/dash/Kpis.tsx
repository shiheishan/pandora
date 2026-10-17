import type { UseQueryResult } from '@tanstack/react-query'
import { Card } from '../../../ui'
import type { Overview } from './api'
import css from './Dash.module.css'
import { asOfText, kpiTiles } from './model'
import { ArrowLink, CardError, PanelSkeleton, Section } from './parts'

export function Kpis({ overview }: { overview: UseQueryResult<Overview> }) {
  const { data, isPending, isError, error, refetch, dataUpdatedAt } = overview

  return (
    <Section
      id="dash-today"
      title="今天"
      sub={data ? asOfText(new Date(dataUpdatedAt)) : undefined}
      extra={<ArrowLink to={{ module: 'billing', tab: 'orders' }}>看订单</ArrowLink>}
    >
      <Card flush>
        {isPending ? (
          <PanelSkeleton rows={2} height={28} />
        ) : isError && !data ? (
          <CardError what="今日概况" error={error} onRetry={() => void refetch()} />
        ) : data ? (
          <div className={css.kpis}>
            {kpiTiles(data).map((tile) => (
              <div key={tile.key} className={css.kp}>
                <div className={css.l}>{tile.label}</div>
                <div className={css.v}>
                  {tile.value}
                  {tile.cents != null && <small>{tile.cents}</small>}
                </div>
                <div className={css.d}>
                  {tile.compare.label} <b>{tile.compare.value}</b>
                </div>
              </div>
            ))}
          </div>
        ) : null}
      </Card>
    </Section>
  )
}
