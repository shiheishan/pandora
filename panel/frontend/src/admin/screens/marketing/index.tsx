/**
 * [INPUT]: 依赖 ../Placeholder，依赖 ../index 的 AdminScreenProps
 * [OUTPUT]: 默认导出 Marketing 页面组件（登记表 React.lazy 的目标）
 * [POS]: admin/screens/marketing 的入口：营销（后台-06），归后台前端二；目前渲染占位页
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { Placeholder } from '../Placeholder'
import type { AdminScreenProps } from '../index'

export default function Marketing(props: AdminScreenProps) {
  return <Placeholder module="marketing" {...props} />
}
