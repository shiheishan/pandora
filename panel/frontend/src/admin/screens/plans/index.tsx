/**
 * [INPUT]: 依赖 ../index 的 AdminScreenProps，依赖 ./CatalogTab 的 CatalogTab，依赖 ./PacksTab 的 PacksTab
 * [OUTPUT]: 默认导出 Plans 页面组件（登记表 React.lazy 的目标）
 * [POS]: admin/screens/plans 的入口：套餐（后台-04），按标签分发——「套餐」#/plans/catalog/<套餐 id>（左栏卡片 + 右侧详情，选中的套餐在地址上），「流量包」#/plans/packs?s=<状态>（后端有、设计稿缺，按设计稿风格补，修订 R73）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { AdminScreenProps } from '../index'
import { CatalogTab } from './CatalogTab'
import { PacksTab } from './PacksTab'

export default function Plans({ tab, rest }: AdminScreenProps) {
  if (tab === 'packs') return <PacksTab />
  return <CatalogTab selected={rest[0] ?? null} />
}
