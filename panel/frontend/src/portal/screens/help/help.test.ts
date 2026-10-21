import { describe, expect, it } from 'vitest'
import { contentDetailSchema, contentPageSchema } from './api'
import { articleBlocks, helpGroups } from './model'

const page = (slug: string, kind: string, category?: string) => ({ slug, kind, category })

describe('帮助中心', () => {
  it('文章 schema：omitempty 字段缺席可通过，kind 只认四种', () => {
    const p = contentPageSchema.parse({ slug: 'a', kind: 'kb_article', version: 1, title: 't' })
    expect(p.category).toBeUndefined()
    expect(() => contentPageSchema.parse({ slug: 'a', kind: 'kb_article', version: 1 })).toThrow()
    expect(() => contentPageSchema.parse({ slug: 'a', kind: 'faq', version: 1, title: 't' })).toThrow()
  })

  it('正文为空时后端省略 body，归一为空串', () => {
    const d = contentDetailSchema.parse({ page: { slug: 'a', kind: 'tutorial', version: 2, title: 't' } })
    expect(d.page.body).toBe('')
  })

  it('后端仍带 locale 等旧字段时 schema 可 parse，结果不含 locale', () => {
    const p = contentPageSchema.parse({
      slug: 'legacy',
      kind: 'kb_article',
      version: 1,
      title: 't',
      locale: 'zh-CN',
      target_platforms: ['ios'],
      min_client_version: '1.0',
      max_client_version: '2.0',
    })
    expect('locale' in p).toBe(false)
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
