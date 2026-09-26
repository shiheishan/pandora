/**
 * [INPUT]: 无
 * [OUTPUT]: 对外提供 HELP_KINDS、HelpGroup / helpGroups、ArticleBlock / articleBlocks、SEARCH_MAX
 * [POS]: portal/screens/help 的纯映射（契约门户-09）：列表只留 kb_article / tutorial 并按 category 出现顺序分组（空分类归「其他」、排最后），正文按行拆成段落与「## 」小标题，交给页面渲染成纯文本节点（不用 innerHTML）；有单元测试
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

/** 帮助中心只收这两类；legal 与 page 不进来（契约门户-09 列表映射） */
export const HELP_KINDS: ReadonlySet<string> = new Set(['kb_article', 'tutorial'])

/** 后端 q 上限 100 字，超了回 422 fields.q */
export const SEARCH_MAX = 100

const OTHER = '其他'

export interface HelpGroup<T> {
  category: string
  items: T[]
}

/** 列表已按 published_at 倒序；分组按分类第一次出现的顺序，空分类归「其他」放最后 */
export function helpGroups<T extends { kind: string; category?: string }>(pages: readonly T[]): HelpGroup<T>[] {
  const groups = new Map<string, T[]>()
  for (const page of pages) {
    if (!HELP_KINDS.has(page.kind)) continue
    const category = page.category?.trim() || OTHER
    const items = groups.get(category)
    if (items) items.push(page)
    else groups.set(category, [page])
  }
  const out = [...groups].map(([category, items]) => ({ category, items }))
  return [...out.filter((g) => g.category !== OTHER), ...out.filter((g) => g.category === OTHER)]
}

export type ArticleBlock = { kind: 'heading' | 'paragraph'; text: string }

/**
 * 正文是纯文本 / Markdown 源码，后端不渲染（契约门户-09）。这里只认「## 」开头的行作小标题，
 * 空行分段，段内换行保留；其余 Markdown 记号原样显示。
 */
export function articleBlocks(body: string): ArticleBlock[] {
  const blocks: ArticleBlock[] = []
  let para: string[] = []
  const flush = () => {
    if (para.length) blocks.push({ kind: 'paragraph', text: para.join('\n') })
    para = []
  }
  for (const raw of body.replace(/\r\n?/g, '\n').split('\n')) {
    const line = raw.trimEnd()
    const heading = /^##(?:\s+(.*))?$/.exec(line)
    if (heading) {
      flush()
      const text = heading[1]?.trim()
      if (text) blocks.push({ kind: 'heading', text })
    } else if (line.trim() === '') {
      flush()
    } else {
      para.push(line)
    }
  }
  flush()
  return blocks
}
