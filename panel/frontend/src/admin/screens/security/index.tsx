import type { AdminScreenProps } from '../index'
import { SignupSecurity } from '../users/SignupSecurity'
import { AccessTab } from './AccessTab'
import { AuditTab } from './AuditTab'
import { RiskTab } from './RiskTab'
import { SwitchesTab } from './SwitchesTab'
import { TraceTab } from './TraceTab'

export default function Security({ tab }: AdminScreenProps) {
  switch (tab) {
    case 'access':
      return <AccessTab />
    case 'risk':
      return <RiskTab />
    case 'trace':
      return <TraceTab />
    case 'signup':
      return <SignupSecurity />
    case 'switches':
      return <SwitchesTab />
    default:
      return <AuditTab />
  }
}
