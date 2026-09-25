/**
 * [INPUT]: 依赖 ../index 的 AdminScreenProps，依赖 ./AuditTab、./AccessTab、./RiskTab、./SwitchesTab
 * [OUTPUT]: 默认导出 Security 页面组件（登记表 React.lazy 的目标）
 * [POS]: admin/screens/security 的入口：安全与运维（后台-09 后半），归后台前端二。四个标签的读都挂 security.audit.read（Shell 已判过），写权限在各标签里按 useCan 判：审计导出 ops.export、风控处置 security.risk.review（停用另要 iam.user.write）、开关切换 platform.settings.write
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { AdminScreenProps } from '../index'
import { AccessTab } from './AccessTab'
import { AuditTab } from './AuditTab'
import { RiskTab } from './RiskTab'
import { SwitchesTab } from './SwitchesTab'

export default function Security({ tab }: AdminScreenProps) {
  switch (tab) {
    case 'access':
      return <AccessTab />
    case 'risk':
      return <RiskTab />
    case 'switches':
      return <SwitchesTab />
    default:
      return <AuditTab />
  }
}
