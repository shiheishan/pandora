/**
 * [INPUT]: 依赖 ../index 的 AdminScreenProps，依赖 ./ChannelsTab、./TemplatesTab、./HooksTab
 * [OUTPUT]: 默认导出 System 页面组件（登记表 React.lazy 的目标）
 * [POS]: admin/screens/system 的入口：通知与插件（后台-09 前半），归后台前端二。三个标签：通知渠道（读挂 security.audit.read）、邮件模板（ops.notification.read）、Webhook 钩子（platform.plugin.read），读权限已由 Shell 判过，写权限在各标签里按 useCan 判
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { AdminScreenProps } from '../index'
import { ChannelsTab } from './ChannelsTab'
import { HooksTab } from './HooksTab'
import { TemplatesTab } from './TemplatesTab'

export default function System({ tab }: AdminScreenProps) {
  switch (tab) {
    case 'templates':
      return <TemplatesTab />
    case 'hooks':
      return <HooksTab />
    default:
      return <ChannelsTab />
  }
}
