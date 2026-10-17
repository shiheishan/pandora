import type { ReactElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { FilterTabs, SearchField, Toolbar } from './index'

const html = (node: ReactElement) => renderToStaticMarkup(node)
const noop = () => {}

describe('FilterTabs', () => {
  const items = [
    { value: 'all', label: '全部', count: 3 },
    { value: 'sub', label: '订阅套餐', count: 2 },
    { value: 'onetime', label: '一次性套餐' },
  ] as const

  it('renders role="radiogroup" with radio items and roving tabindex', () => {
    const out = html(<FilterTabs label="按类型筛选" items={items} value="all" onChange={noop} />)
    expect(out).toContain('role="radiogroup"')
    expect(out).toContain('aria-label="按类型筛选"')
    expect(out).toMatch(/role="radio" aria-checked="true" tabindex="0"/)
    expect(out).toMatch(/role="radio" aria-checked="false" tabindex="-1"/)
  })

  it('renders counts when provided and omits count element when absent', () => {
    const out = html(<FilterTabs label="按类型筛选" items={items} value="all" onChange={noop} />)
    expect(out).toContain('>3</span>')
    expect(out).toContain('>2</span>')
    // count 元素有对应的 class，3 项里只有前 2 项有 count
    const countMatches = out.match(/<span class="[^"]*count[^"]*">/g)
    expect(countMatches?.length).toBe(2)
  })
})

describe('Toolbar and SearchField', () => {
  it('Toolbar renders children and end slot when provided', () => {
    const withoutEnd = html(
      <Toolbar>
        <div>左侧内容</div>
      </Toolbar>,
    )
    expect(withoutEnd).toContain('左侧内容')
    expect(withoutEnd).not.toContain('操作')

    const withEnd = html(
      <Toolbar end={<button type="button">新建</button>}>
        <div>左侧内容</div>
      </Toolbar>,
    )
    expect(withEnd).toContain('左侧内容')
    expect(withEnd).toContain('新建')
  })

  it('SearchField renders type="search" and aria-label', () => {
    const out = html(<SearchField aria-label="搜索套餐" placeholder="名称或代码" />)
    expect(out).toContain('type="search"')
    expect(out).toContain('aria-label="搜索套餐"')
    expect(out).toContain('placeholder="名称或代码"')
  })
})
