import type { UseQueryResult } from '@tanstack/react-query'
import { Skeleton } from '../../../ui'
import type { NodeTraffic, Overview } from './api'
import css from './Dash.module.css'
import { kpiTiles } from './model'
import { CardError, PanelHead, targetHref, toneText } from './parts'

export function Kpis({ overview, traffic }: { overview: UseQueryResult<Overview>; traffic: UseQueryResult<NodeTraffic> }) {
  const loading = overview.isPending || traffic.isPending
  const tiles = kpiTiles(overview.data, traffic.data)

  return (
    <section aria-labelledby="dash-kpis">
      <PanelHead id="dash-kpis" title="今日概况" hint="今日" />
      {overview.isError && (
        <CardError what="今日概况" error={overview.error} onRetry={() => void overview.refetch()} />
      )}
      {loading ? (
        <Skeleton height={92} />
      ) : (
        <div className={css.kpiGrid}>
          {tiles.map((t) => (
            <a key={t.key} className={css.kpiTile} href={targetHref(t.target)} title={t.tip}>
              <span className={css.kpiLabel}>{t.label}</span>
              <span className={t.muted ? `${css.kpiValue} ${css.kpiValueMuted}` : css.kpiValue}>{t.value}</span>
              <span className={`${css.kpiSub} ${(t.subTone && toneText(t.subTone)) ?? css.kpiSubMuted}`}>
                {t.sub || ' '}
              </span>
            </a>
          ))}
        </div>
      )}
    </section>
  )
}
