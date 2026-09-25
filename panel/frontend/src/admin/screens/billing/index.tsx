/**
 * [INPUT]: 依赖 react 的 useEffect / useState，依赖 ../index 的 AdminScreenProps，依赖 ./OrdersTab、./ArrearsTab、./ProvidersTab、./AdjustTab
 * [OUTPUT]: 默认导出 Billing 页面组件（登记表 React.lazy 的目标）
 * [POS]: admin/screens/billing 的入口：订单与收款（后台-05），按标签分发——订单（#/billing/orders/<订单 id>?s=&q=&o=&user_id=&new=）、挂账（?s=&o=）、支付渠道、收入调整（?c=）；相对时间与账龄的 now 每分钟走一次、各标签共用
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
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
