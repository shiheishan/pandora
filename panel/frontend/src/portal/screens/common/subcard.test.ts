import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { ToastProvider } from '../../../ui'
import { makeNaming } from './purchase'
import { SubCard } from './SubCard'
import { link, sub } from './testing'

describe('SubCard', () => {
  const noop = () => {}
  const far = { current_period_end: '2099-01-19T12:00:00Z', renew_until: '2099-02-19T12:00:00Z' }

  it('一次性可叠加时出现「再买一份叠加」且链接带 stack=', () => {
    const once = sub({
      id: 'o1',
      plan_kind: 'one_time',
      stackable: true,
      renewable: false,
      renew_until: null,
      reset_price: null,
      ...far,
    })
    const html = renderToStaticMarkup(
      createElement(ToastProvider, null, createElement(SubCard, { sub: once, naming: makeNaming([once], []), link: link('o1', 'a3f9'), actions: { onImport: noop, onRename: noop, onRotate: noop } })),
    )
    expect(html).toContain('再买一份叠加')
    expect(html).toContain('btn-stack-o1')
    expect(html).toMatch(/stack=o1/)
  })

  it('开了付费流量重置时出现「流量不够」', () => {
    const s = sub({ id: 'r1', reset_price: 500, reset_limit: 2, resets_used: 0, ...far })
    const html = renderToStaticMarkup(
      createElement(ToastProvider, null, createElement(SubCard, { sub: s, naming: makeNaming([s], []), link: link('r1', 'b7c2'), actions: { onImport: noop, onRename: noop, onRotate: noop } })),
    )
    expect(html).toContain('流量不够')
    expect(html).toContain('btn-traffic-r1')
    expect(html).toContain('/subs/r1/traffic')
  })
})
