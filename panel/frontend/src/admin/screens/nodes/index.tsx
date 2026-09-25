/**
 * [INPUT]: 依赖 ../index 的 AdminScreenProps，依赖 ../Placeholder，依赖 ./NodesTab
 * [OUTPUT]: 默认导出 Nodes 页面组件（登记表 React.lazy 的目标）
 * [POS]: admin/screens/nodes 的入口：节点与服务器（后台-07），归后台前端二。第 ② 步接入「节点」标签（rest = [节点 id, 抽屉标签]）；服务器、节点池、路由三个标签是第 ③ 步，暂渲染占位页。标签读权限已由 Shell 判过
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { AdminScreenProps } from '../index'
import { Placeholder } from '../Placeholder'
import { NodesTab } from './NodesTab'

export default function Nodes(props: AdminScreenProps) {
  if (props.tab === 'nodes') return <NodesTab rest={props.rest} />
  return <Placeholder module="nodes" {...props} />
}
