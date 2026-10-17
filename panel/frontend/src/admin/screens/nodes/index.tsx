import type { AdminScreenProps } from '../index'
import { DETAIL_TABS, NodeDetailPage } from './NodeDetailPage'
import { NodesTab } from './NodesTab'
import { PoolsTab } from './PoolsTab'
import { RoutingTab } from './RoutingTab'
import { ServerDetailPage } from './ServerDetailPage'
import { ServersTab } from './ServersTab'
import { SubscriptionTab } from './SubscriptionTab'

export default function Nodes({ tab, rest }: AdminScreenProps) {
  switch (tab) {
    case 'servers':
      return rest[0] ? <ServerDetailPage id={rest[0]} /> : <ServersTab />
    case 'pools':
      return <PoolsTab />
    case 'routing':
      return <RoutingTab rest={rest} />
    case 'subscription':
      return <SubscriptionTab />
    default:
      return rest[0] ? (
        <NodeDetailPage id={rest[0]} tab={DETAIL_TABS.find(([k]) => k === rest[1])?.[0] ?? 'metrics'} />
      ) : (
        <NodesTab />
      )
  }
}
