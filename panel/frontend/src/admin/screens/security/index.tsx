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
