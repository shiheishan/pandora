import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { PageActions, PageActionsProvider, PageCount } from './PageActions'

// 服务端渲染不支持 portal，这里只核对「没有插槽时什么都不画」；有插槽时的落位由浏览器用例覆盖
describe('PageActions / PageCount', () => {
  it('renders nothing outside a provider', () => {
    expect(renderToStaticMarkup(<PageActions><button type="button">新建</button></PageActions>)).toBe('')
    expect(renderToStaticMarkup(<PageCount value={48} />)).toBe('')
  })

  it('renders nothing when the shell has no page head (slots are null)', () => {
    const out = renderToStaticMarkup(
      <PageActionsProvider slots={{ actions: null, count: null }}>
        <PageActions>
          <button type="button">新建</button>
        </PageActions>
        <PageCount value={48} />
        <p>正文</p>
      </PageActionsProvider>,
    )
    expect(out).toBe('<p>正文</p>')
  })
})
