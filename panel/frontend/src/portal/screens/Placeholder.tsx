/**
 * [INPUT]: 依赖 ../../ui 的 Empty，依赖 ../pages 的 PAGES / PageKey，依赖 ./index 的 PortalScreenProps
 * [OUTPUT]: 对外提供 Placeholder
 * [POS]: portal/screens 的占位页：页面还没写时各目录的 index.tsx 渲染它（即第 2 阶段 Shell 里的空状态）；十一个页面都换成真页面后删除
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { Empty } from '../../ui'
import { PAGES, type PageKey } from '../pages'
import type { PortalScreenProps } from './index'

export function Placeholder({ page }: PortalScreenProps & { page: PageKey }) {
  return <Empty title="这里还是空的" description={`「${PAGES[page][0]}」页面将在第 3 阶段接入。`} />
}
