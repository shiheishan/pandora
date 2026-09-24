/**
 * [INPUT]: 依赖 ../Placeholder，依赖 ../index 的 PortalScreenProps
 * [OUTPUT]: 默认导出 Tickets 页面组件（登记表 React.lazy 的目标）
 * [POS]: portal/screens/tickets 的入口：工单支持（门户-07），归门户前端；目前渲染占位页
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { Placeholder } from '../Placeholder'
import type { PortalScreenProps } from '../index'

export default function Tickets(props: PortalScreenProps) {
  return <Placeholder page="tickets" {...props} />
}
