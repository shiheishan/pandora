/**
 * [INPUT]: 依赖 ../../../ui 的 Empty，依赖 ./api 的 useActivity，依赖 ./model 的 activitySummary / formatCount，依赖 ./parts，依赖 ./Dash.module.css
 * [OUTPUT]: 对外提供 Activity
 * [POS]: 仪表盘「注册与活跃 · 近 14 天」面板：GET v1/stats/timeseries?days=14，注册柱与活跃柱成对（active_users 待补·后端，未上时只画注册柱、日活均值显示 —），tooltip 附登录、订单、独立 IP（待补·前端）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { Empty } from '../../../ui'
import { useActivity } from './api'
import css from './Dash.module.css'
import { activitySummary, formatCount } from './model'
import { CardError, isForbidden, PanelSkeleton } from './parts'

export function Activity() {
  const q = useActivity(true)
  if (q.isError && isForbidden(q.error)) return null
  const summary = q.data ? activitySummary(q.data.points) : null

  return (
    <section className={css.panel} aria-labelledby="dash-activity">
      <div className={css.panelHead}>
        <h2 id="dash-activity" className={css.panelTitle}>
          注册与活跃
        </h2>
        <span className={css.hint}>近 14 天</span>
        <div className={css.spacer} />
        <div className={css.legend} aria-hidden="true">
          <span className={css.legendItem}>
            <span className={`${css.swatch} ${css.swatchRegistered}`} />
            注册
          </span>
          {summary && summary.activeAverage !== null && (
            <span className={css.legendItem}>
              <span className={`${css.swatch} ${css.swatchActive}`} />
              活跃
            </span>
          )}
        </div>
      </div>
      {q.isError && !summary ? (
        <CardError what="注册与活跃" error={q.error} onRetry={() => void q.refetch()} />
      ) : !summary ? (
        <PanelSkeleton rows={1} height={196} />
      ) : summary.empty ? (
        <Empty bare title="近 14 天没有注册和登录" description="有用户注册或登录后，这里按天显示注册与活跃人数。" />
      ) : (
        <>
          <div className={css.pairBars} role="img" aria-label="近 14 天每日注册与活跃柱图">
            {summary.bars.map((b) => (
              <div key={b.key} className={css.pairSlot} title={b.tip}>
                {b.active !== null && <div className={css.barActive} style={{ height: `${b.active}%` }} />}
                <div className={css.barRegistered} style={{ height: `${b.registered}%` }} />
              </div>
            ))}
          </div>
          <div className={`${css.stats} ${css.statsBottom}`}>
            <div>
              <div className={css.hint}>14 日注册</div>
              <div className={css.statValue}>{formatCount(summary.registeredTotal)}</div>
            </div>
            <div>
              <div className={css.hint}>日活均值</div>
              <div className={css.statValue}>{summary.activeAverage === null ? '—' : formatCount(summary.activeAverage)}</div>
            </div>
          </div>
        </>
      )}
    </section>
  )
}
