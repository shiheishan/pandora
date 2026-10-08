import { isApiError } from '../../../core/api'
import { href } from '../../../core/router'
import { Card, Empty, Skeleton, Tag } from '../../../ui'
import { usePageHead } from '../../head'
import { LoadError } from '../common/Blocks'
import { protocolLabel, rateLabel } from '../common/clients'
import { flowCss } from '../common/Flow'
import { useHoldings } from '../common/holdings'
import { usePlanTraffic, useSubscriptionNodes, type Subscription } from '../common/subscriptions'
import { UsageCard } from '../common/UsageCard'
import css from './Subs.module.css'

/** 节点与每天用量（原型没画）：从卡片「节点和每天用量 ›」进来 */
export function Detail({ id }: { id: string }) {
  usePageHead('节点和每天用量', '/subs')
  const h = useHoldings()
  if (h.subs.isPending) return <Skeleton height={200} />
  if (h.subs.isError) return <LoadError error={h.subs.error} onRetry={() => void h.subs.refetch()} what="套餐" />
  const sub = h.held.find((s) => s.id === id)
  if (!sub) return <Empty title="找不到这一份" description="可能已经停用了。" action={<a href={href('/subs')}>回到我的套餐</a>} />
  return (
    <div className={css.page}>
      {h.naming.multi && <p className={flowCss.lead}>{h.naming.dn(sub)}</p>}
      <div className={css.grid}>
        <NodesCard subscriptionId={sub.id} />
        <Usage sub={sub} />
      </div>
    </div>
  )
}

function Usage({ sub }: { sub: Subscription }) {
  const { summary, resetAt } = usePlanTraffic(sub)
  return <UsageCard subscriptionId={sub.id} summary={summary} resetAt={resetAt} />
}

// 保留规则 3：只有名称 / 协议 / 倍率；这一份不在可用状态时接口回 404
function NodesCard({ subscriptionId }: { subscriptionId: string }) {
  const nodes = useSubscriptionNodes(subscriptionId)
  const unavailable = nodes.isError && isApiError(nodes.error, 'not_found')
  return (
    <Card flush title="可用节点" extra={nodes.data ? <span className={css.cardNote}>{nodes.data.count} 个 · App 里可以测速</span> : undefined} className={css.listCard}>
      {nodes.isPending ? (
        <div className={css.listSkeleton}>
          <Skeleton height={16} />
          <Skeleton height={16} />
          <Skeleton width="60%" height={16} />
        </div>
      ) : unavailable ? (
        <Empty bare title="现在没有可用节点" description="这一份待续费或已暂停时节点不会下发，续费后自动恢复。" />
      ) : nodes.isError ? (
        <LoadError error={nodes.error} onRetry={() => void nodes.refetch()} what="节点" />
      ) : nodes.data.nodes.length === 0 ? (
        <Empty bare title="这个套餐暂时没有节点" description="节点上线后会自动出现，在 App 里更新一次就能看到。" />
      ) : (
        <ul className={css.list}>
          {nodes.data.nodes.map((n, i) => {
            const rate = rateLabel(n.traffic_rate)
            return (
              <li key={`${n.name}-${i}`} className={css.nodeRow}>
                <span className={css.nodeName}>{n.name}</span>
                <span className={css.nodeProto}>{protocolLabel(n.protocol)}</span>
                <span className={css.nodeRate}>{rate && <Tag tone={n.traffic_rate > 1 ? 'warn' : 'ok'}>{rate}</Tag>}</span>
              </li>
            )
          })}
        </ul>
      )}
    </Card>
  )
}
