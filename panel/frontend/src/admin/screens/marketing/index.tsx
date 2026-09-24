/**
 * [INPUT]: 依赖 ../index 的 AdminScreenProps，依赖 ./Coupons、./Gifts、./Commission
 * [OUTPUT]: 默认导出 Marketing 页面组件（登记表 React.lazy 的目标）
 * [POS]: admin/screens/marketing 的入口：营销（后台-06），归后台前端二；按标签切三块，rest 只有礼品卡用（#/marketing/gifts/<templates|batches|usages>[/<批次 id>]）。标签的读权限已由 Shell 判过
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { AdminScreenProps } from '../index'
import { Commission } from './Commission'
import { Coupons } from './Coupons'
import { Gifts } from './Gifts'

export default function Marketing({ tab, rest }: AdminScreenProps) {
  if (tab === 'gifts') return <Gifts rest={rest} />
  if (tab === 'commission') return <Commission />
  return <Coupons />
}
