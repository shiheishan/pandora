/**
 * [INPUT]: 依赖 ../../../core/router 的 href，依赖 ../../../ui 的 Card / Skeleton / Empty，依赖 ./subscriptions 的 useSubscriptionUsage，依赖 ./traffic 的 buildUsageBars / projectUsage / formatGB / TrafficSummary，依赖 ./Blocks 的 LoadError
 * [OUTPUT]: 对外提供 UsageCard
 * [POS]: portal/screens/common 的「本期用量」卡（门户-01 设计稿）：按日柱状图 + 日均 / 今天 + 「按目前的速度…」预测，不够用时给「买流量包 →」；概览放在主卡下方，我的订阅放在页尾看选中那条订阅
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { href } from '../../../core/router'
import { Card, Empty, Skeleton } from '../../../ui'
import { LoadError } from './Blocks'
import css from './common.module.css'
import { useSubscriptionUsage } from './subscriptions'
import { buildUsageBars, formatGB, projectUsage, type TrafficSummary } from './traffic'

interface UsageCardProps {
  subscriptionId: string
  /** 主卡算好的流量摘要（含流量包）；没有流量额度时为 null，只画柱不预测 */
  summary: TrafficSummary | null
  resetAt: string | null
}

export function UsageCard({ subscriptionId, summary, resetAt }: UsageCardProps) {
  const usage = useSubscriptionUsage(subscriptionId)

  if (usage.isPending) {
    return (
      <Card aria-busy="true" className={css.usage}>
        <Skeleton width={180} height={18} />
        <Skeleton height={120} radius="var(--radius-md)" />
      </Card>
    )
  }
  if (usage.isError) {
    return (
      <Card title="本期用量" className={css.usage}>
        <LoadError error={usage.error} onRetry={() => void usage.refetch()} what="用量" />
      </Card>
    )
  }

  const report = usage.data
  const chart = buildUsageBars(report)
  const projection = summary ? projectUsage(summary, report.avg_daily_bytes, resetAt) : null
  const dense = chart.bars.length > 40

  return (
    <Card className={css.usage}>
      <div className={css.usageHead}>
        <div className={css.usageTitle}>
          <div className={css.usageTitleRow}>
            <span className={css.usageName}>本期用量</span>
            {chart.range && <span className={css.usageRange}>{chart.range}</span>}
          </div>
          {projection && (
            <div className={projection.short ? css.projectionShort : css.projection}>
              {projection.text}
              {projection.short && (
                <a className={css.inlineLink} href={href('/plans', { tab: 'packs' })}>
                  买流量包 →
                </a>
              )}
            </div>
          )}
        </div>
        <dl className={css.usageStats}>
          <div>
            <dt>日均</dt>
            <dd>
              {formatGB(report.avg_daily_bytes, 1)}
              <span> GB</span>
            </dd>
          </div>
          <div>
            <dt>今天</dt>
            <dd>
              {formatGB(report.today_bytes, 1)}
              <span> GB</span>
            </dd>
          </div>
        </dl>
      </div>
      {chart.bars.length > 0 ? (
        <div className={css.chart}>
          <div className={dense ? css.barsDense : css.bars} role="img" aria-label={`本期每日用量，${chart.range}，日均 ${formatGB(report.avg_daily_bytes, 1)} GB`}>
            {chart.bars.map((b) => (
              <div
                key={b.date}
                className={b.bytes === null ? css.barFuture : b.today ? css.barToday : css.bar}
                style={b.bytes === null ? undefined : { height: `max(4px, ${b.height}%)` }}
                title={b.bytes === null ? `${b.date.slice(5)} · 未到` : `${b.date.slice(5)}${b.today ? '（今天）' : ''} · ${formatGB(b.bytes, 1)} GB`}
              />
            ))}
          </div>
          <div className={css.axis}>
            <span>{chart.start}</span>
            <span>{chart.endLabel}</span>
          </div>
        </div>
      ) : (
        <Empty bare title="本期还没有用量记录" description="客户端连上节点后，每天的用量会显示在这里。" />
      )}
    </Card>
  )
}
