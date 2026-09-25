/**
 * [INPUT]: 依赖 ../index 的 AdminScreenProps，依赖 ./NodesTab、./ServersTab、./PoolsTab、./RoutingTab
 * [OUTPUT]: 默认导出 Nodes 页面组件（登记表 React.lazy 的目标）
 * [POS]: admin/screens/nodes 的入口：节点与服务器（后台-07），归后台前端二。四个标签全部接入：节点（rest = [节点 id, 抽屉标签]）、服务器（rest = [服务器 id, 抽屉标签]）、节点池、路由。标签读权限已由 Shell 判过
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
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
      return <RoutingTab />
    default:
      return <NodesTab rest={rest} />
  }
}
