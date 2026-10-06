import { useEffect, useState } from 'react'
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

function useNow(intervalMs = 60_000): Date {
  const [now, setNow] = useState(() => new Date())
  useEffect(() => {
    const timer = setInterval(() => setNow(new Date()), intervalMs)
    return () => clearInterval(timer)
  }, [intervalMs])
  return now
}
