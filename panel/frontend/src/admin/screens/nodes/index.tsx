import type { AdminScreenProps } from '../index'
import { NodesTab } from './NodesTab'
import { PoolsTab } from './PoolsTab'
import { RoutingTab } from './RoutingTab'
import { ServersTab } from './ServersTab'

export default function Nodes({ tab, rest }: AdminScreenProps) {
  switch (tab) {
    case 'servers':
      return <ServersTab rest={rest} />
    case 'pools':
      return <PoolsTab />
    case 'routing':
      return <RoutingTab rest={rest} />
    default:
      return <NodesTab rest={rest} />
  }
}
