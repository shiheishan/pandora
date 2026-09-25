/**
 * [INPUT]: 依赖 ../../../core/router 的 navigate / useHashLocation，依赖 ../Placeholder，依赖 ../common/PayFlow 的 PaymentModal，依赖 ../index 的 PortalScreenProps
 * [OUTPUT]: 默认导出 Orders 页面组件（登记表 React.lazy 的目标）
 * [POS]: portal/screens/orders 的入口：我的订单（门户-04）。第 ② 步只接收银台回跳——#/orders/<订单 id>?paid=1 时弹支付弹窗轮询确认，关掉后抹掉 paid 参数；列表与明细在第 ③ 步接入，目前其余部分渲染占位页
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { navigate, useHashLocation } from '../../../core/router'
import { PaymentModal } from '../common/PayFlow'
import type { PortalScreenProps } from '../index'
import { Placeholder } from '../Placeholder'

export default function Orders(props: PortalScreenProps) {
  const { query } = useHashLocation()
  const orderId = props.rest[0]
  const returning = orderId !== undefined && query.get('paid') === '1'
  return (
    <>
      <Placeholder page="orders" {...props} />
      <PaymentModal state={returning ? { phase: 'confirm', orderId } : null} onClose={() => navigate(`/orders/${encodeURIComponent(orderId ?? '')}`, { replace: true })} />
    </>
  )
}
