import { useNow } from '../../kit'
import type { AdminScreenProps } from '../index'
import { AdjustTab } from './AdjustTab'
import { ArrearsTab } from './ArrearsTab'
import { OrdersTab } from './OrdersTab'
import { ProvidersTab } from './ProvidersTab'

export default function Billing({ tab, rest }: AdminScreenProps) {
  const now = useNow()
  if (tab === 'arrears') return <ArrearsTab now={now} />
  if (tab === 'providers') return <ProvidersTab now={now} />
  if (tab === 'adjust') return <AdjustTab />
  return <OrdersTab rest={rest} now={now} />
}

