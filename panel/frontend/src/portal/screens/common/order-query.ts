/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useMutation / useQueryClient，依赖 zod，依赖 ../../../core/api 的 ApiError / isApiError，依赖 ../../../shell/runtime 的 useApi，依赖 ./orders 的 ORDER_STATUSES / PAID_STATUSES
 * [OUTPUT]: 对外提供 orderQuerySchema / OrderQueryResult、QueryOutcome、queryOutcome、queryFailure、useQueryOrderPayment
 * [POS]: portal/screens/common 的「我已支付，刷新状态」数据层（POST v1/orders/{id}/query，与后台同一个领域用例）：结果归成三种——已到账、渠道尚未确认、查询失败——由纯函数映射文案，有单元测试；订单页的待支付卡片与明细消费
 */
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { z } from 'zod'
import { isApiError } from '../../../core/api'
import { useApi } from '../../../shell/runtime'
import { ORDER_STATUSES, PAID_STATUSES } from './orders'

/** 与 Go billing.OrderPaymentQuery 的 json tag 一一对应；quarantine_kind 带 omitempty */
export const orderQuerySchema = z.object({
  order_id: z.string(),
  order_no: z.string(),
  provider_code: z.string(),
  channel_status: z.enum(['paid', 'unpaid', 'not_found']),
  reconciled: z.boolean(),
  already_recorded: z.boolean(),
  quarantine_kind: z.string().optional(),
  order_status: z.enum(ORDER_STATUSES),
})
export type OrderQueryResult = z.output<typeof orderQuerySchema>

/** 三种结果：settled 已到账、pending 渠道尚未确认、failed 查询失败 */
export interface QueryOutcome {
  kind: 'settled' | 'pending' | 'failed'
  text: string
}

export function queryOutcome(r: OrderQueryResult): QueryOutcome {
  if (r.channel_status !== 'paid') {
    return {
      kind: 'pending',
      text: r.channel_status === 'not_found' ? '支付渠道还没有这笔付款的记录。刚付完的话，请过一两分钟再刷新。' : '支付渠道尚未确认收到付款。刚付完的话，请过一两分钟再刷新。',
    }
  }
  if (r.quarantine_kind) {
    // 钱到了但订单已关闭（例如超时取消之后才付）：款项单独记着，等人工处理
    return { kind: 'settled', text: '款项已到账，但订单已关闭，没有开通。这笔钱已单独记录，请提交工单，我们会转入余额或退回。' }
  }
  if (PAID_STATUSES.has(r.order_status)) return { kind: 'settled', text: '已到账，订单已完成。' }
  return { kind: 'pending', text: '支付渠道显示已付款，正在入账，请稍后刷新。' }
}

export function queryFailure(e: unknown): QueryOutcome {
  if (isApiError(e, 'network_error')) return { kind: 'failed', text: '查询失败：网络不通，请检查网络后重试。' }
  if (isApiError(e, 'rate_limited')) return { kind: 'failed', text: '查询太频繁了，请稍等一分钟再试。' }
  // 渠道停用 / 不支持查单 / 渠道查询失败 / 从未发起过支付：服务端给的是中文原因，原样显示
  return { kind: 'failed', text: `查询失败：${e instanceof Error ? e.message : '请稍后重试'}` }
}

/** POST v1/orders/{id}/query：无 body、不要幂等键（同一笔钱只会记一次）；按账号限流每分钟 6 次 */
export function useQueryOrderPayment() {
  const api = useApi()
  const client = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.post(`v1/orders/${encodeURIComponent(id)}/query`, orderQuerySchema),
    // 补记了就是订单、余额、订阅都可能变了；没补记也重拉一次，看到的是最新状态
    onSettled: () => void client.invalidateQueries({ queryKey: ['portal'] }),
  })
}
