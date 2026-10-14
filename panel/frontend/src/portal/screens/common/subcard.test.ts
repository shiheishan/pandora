import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { ToastProvider } from '../../../ui'
import { makeNaming } from './purchase'
import { SubCard } from './SubCard'
import { GIB, link, sub } from './testing'

// 用户 10-09 删掉了「升级前买的流量包可以从在用的那份挪一次」：流量包挂在哪一份就跟着哪一份，
// 只有那份彻底停用后才由我的套餐顶上的提示条转走（subs/Transfer.tsx）。卡片上不再有挪的入口。
describe('SubCard · 流量包', () => {
  const noop = () => {}
  const far = { current_period_end: '2099-01-19T12:00:00Z', renew_until: '2099-02-19T12:00:00Z' }

  it('流量包挂在在用的那份上、手上还有另一份时，卡片只写含流量包多少，没有挪走的入口', () => {
    const mine = sub({ id: 'a', label: '我的', pack_remaining_bytes: 80 * GIB, ...far })
    const mom = sub({ id: 'b', label: '妈妈的', ...far })
    const links = [link('a', 'a3f9'), link('b', 'b7c2')]
    const html = renderToStaticMarkup(
      createElement(ToastProvider, null, createElement(SubCard, { sub: mine, naming: makeNaming([mine, mom], links), link: links[0], minPack: 500, actions: { onImport: noop, onRename: noop, onRotate: noop } })),
    )
    expect(html).toContain('含流量包 80G')
    expect(html).not.toMatch(/挪|升级前/)
    expect(html).not.toContain('btn-move-')
  })
})
