/**
 * [INPUT]: 依赖 ../../ui 的 Empty，依赖 ../modules 的 MODULES / ModuleKey，依赖 ./index 的 AdminScreenProps
 * [OUTPUT]: 对外提供 Placeholder
 * [POS]: admin/screens 的占位页：模块还没写时各目录的 index.tsx 渲染它（即第 2 阶段 Shell 里的空状态）；十个模块都换成真页面后删除
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { Empty } from '../../ui'
import { MODULES, type ModuleKey } from '../modules'
import type { AdminScreenProps } from './index'

export function Placeholder({ module, tab }: AdminScreenProps & { module: ModuleKey }) {
  const def = MODULES[module]
  const tabTitle = def.tabs?.find(([k]) => k === tab)?.[1]
  return <Empty title="这里还是空的" description={`「${def.title}${tabTitle ? ` / ${tabTitle}` : ''}」的页面将在第 3 阶段接入。`} />
}
