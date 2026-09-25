/**
 * [INPUT]: 依赖 ../index 的 AdminScreenProps，依赖 ./AnnounceTab、./KbTab、./ThemeTab
 * [OUTPUT]: 默认导出 Content 页面组件（登记表 React.lazy 的目标）
 * [POS]: admin/screens/content 的入口：内容与外观（后台-08），归后台前端二。三个标签：公告（rest = [公告 id | new]）、知识库（rest = [slug | new]）、主题与插槽。标签读权限已由 Shell 判过（公告、知识库没有读权限，按写权限判）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { AdminScreenProps } from '../index'
import { AnnounceTab } from './AnnounceTab'
import { KbTab } from './KbTab'
import { ThemeTab } from './ThemeTab'

export default function Content({ tab, rest }: AdminScreenProps) {
  switch (tab) {
    case 'kb':
      return <KbTab rest={rest} />
    case 'theme':
      return <ThemeTab />
    default:
      return <AnnounceTab rest={rest} />
  }
}
