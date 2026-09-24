/**
 * [INPUT]: 依赖 ../Placeholder，依赖 ../index 的 AdminScreenProps
 * [OUTPUT]: 默认导出 Security 页面组件（登记表 React.lazy 的目标）
 * [POS]: admin/screens/security 的入口：安全与运维（后台-09），归后台前端二；目前渲染占位页
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { Placeholder } from '../Placeholder'
import type { AdminScreenProps } from '../index'

export default function Security(props: AdminScreenProps) {
  return <Placeholder module="security" {...props} />
}
