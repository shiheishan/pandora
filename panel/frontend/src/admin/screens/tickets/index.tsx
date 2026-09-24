/**
 * [INPUT]: 依赖 ../Placeholder，依赖 ../index 的 AdminScreenProps
 * [OUTPUT]: 默认导出 Tickets 页面组件（登记表 React.lazy 的目标）
 * [POS]: admin/screens/tickets 的入口：工单（后台-02），归后台前端一；目前渲染占位页
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { Placeholder } from '../Placeholder'
import type { AdminScreenProps } from '../index'

export default function Tickets(props: AdminScreenProps) {
  return <Placeholder module="tickets" {...props} />
}
