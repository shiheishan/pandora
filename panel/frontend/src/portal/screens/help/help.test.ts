/**
 * [INPUT]: 依赖 vitest，依赖 ./api 的 contentPageSchema / contentDetailSchema，依赖 ./model 的纯映射
 * [OUTPUT]: 无（测试）
 * [POS]: 第 ⑥ 步帮助中心的单元测试：文章 schema（omitempty 字段缺席、空正文归一）、按分类分组（只留 kb_article / tutorial、空分类归「其他」排最后）、正文按段落与「## 」小标题拆块
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it } from 'vitest'
import { contentDetailSchema, contentPageSchema } from './api'
import { articleBlocks, helpGroups } from './model'

const page = (slug: string, kind: string, category?: string) => ({ slug, kind, category })

describe('帮助中心', () => {
  it('文章 schema：omitempty 字段缺席可通过，target_platforms 必须是数组', () => {
    const p = contentPageSchema.parse({ slug: 'a', kind: 'kb_article', version: 1, title: 't', locale: 'zh-CN', target_platforms: [] })
    expect(p.category).toBeUndefined()
    expect(() => contentPageSchema.parse({ slug: 'a', kind: 'kb_article', version: 1, title: 't', locale: 'zh-CN', target_platforms: null })).toThrow()
    expect(() => contentPageSchema.parse({ slug: 'a', kind: 'faq', version: 1, title: 't', locale: 'zh-CN', target_platforms: [] })).toThrow()
  })

  it('正文为空时后端省略 body，归一为空串', () => {
    const d = contentDetailSchema.parse({ page: { slug: 'a', kind: 'tutorial', version: 2, title: 't', locale: 'zh-CN', target_platforms: ['ios'] } })
    expect(d.page.body).toBe('')
  })

  it('只留知识库与教程，按分类出现顺序分组，空分类归「其他」排最后', () => {
    const groups = helpGroups([
      page('a', 'kb_article', ''),
      page('b', 'tutorial', '客户端教程'),
      page('c', 'legal', '条款'),
      page('d', 'kb_article', '快速上手'),
      page('e', 'tutorial', '客户端教程'),
      page('f', 'page'),
      page('g', 'kb_article'),
    ])
    expect(groups.map((g) => [g.category, g.items.map((i) => i.slug)])).toEqual([
      ['客户端教程', ['b', 'e']],
      ['快速上手', ['d']],
      ['其他', ['a', 'g']],
    ])
  })

  it('正文：空行分段、段内换行保留、「## 」行作小标题', () => {
    expect(articleBlocks('第一段\n续行\n\n## 选择节点\r\n打开客户端\n\n\n##不是标题\n## ')).toEqual([
      { kind: 'paragraph', text: '第一段\n续行' },
      { kind: 'heading', text: '选择节点' },
      { kind: 'paragraph', text: '打开客户端' },
      { kind: 'paragraph', text: '##不是标题' },
    ])
    expect(articleBlocks('')).toEqual([])
  })
})
