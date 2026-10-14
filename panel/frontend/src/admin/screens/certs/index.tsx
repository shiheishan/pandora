import type { AdminScreenProps } from '../index'
import { AcmeTab } from './AcmeTab'
import { CertsTab } from './CertsTab'
import { DnsTab } from './DnsTab'

export default function Certs({ tab, rest }: AdminScreenProps) {
  switch (tab) {
    case 'dns':
      return <DnsTab />
    case 'acme':
      return <AcmeTab />
    default:
      return <CertsTab rest={rest} />
  }
}
